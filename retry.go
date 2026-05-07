package e2b

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultRetryMaxAttempts    = 3
	defaultRetryInitialBackoff = 200 * time.Millisecond
	defaultRetryMaxBackoff     = 5 * time.Second
)

// RetryPolicy controls how transient HTTP failures are retried.
// A zero RetryPolicy uses conservative defaults: 3 total attempts,
// 200 ms initial backoff, and 5 s maximum backoff.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first.
	// A value of 1 disables retries entirely. Zero applies the default of 3.
	MaxAttempts int
	// InitialBackoff is the delay before the first retry.
	// Zero applies the default of 200 ms.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponential growth of inter-retry delays.
	// Zero applies the default of 5 s.
	MaxBackoff time.Duration
}

func (p RetryPolicy) maxAttempts() int {
	if p.MaxAttempts <= 0 {
		return defaultRetryMaxAttempts
	}
	return p.MaxAttempts
}

func (p RetryPolicy) initialBackoff() time.Duration {
	if p.InitialBackoff <= 0 {
		return defaultRetryInitialBackoff
	}
	return p.InitialBackoff
}

func (p RetryPolicy) maxBackoff() time.Duration {
	if p.MaxBackoff <= 0 {
		return defaultRetryMaxBackoff
	}
	return p.MaxBackoff
}

// retrier executes HTTP operations according to a RetryPolicy.
// sleepFunc is replaceable in tests to avoid real delays.
type retrier struct {
	policy    RetryPolicy
	sleepFunc func(context.Context, time.Duration) error
}

func newRetrier(policy RetryPolicy) *retrier {
	return &retrier{policy: policy, sleepFunc: contextSleep}
}

func contextSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do calls fn up to policy.MaxAttempts times and returns nil on the first
// success. fn returns the HTTP status code (0 if no response was received),
// the raw Retry-After header value (empty if absent), and an error. The loop
// stops immediately on success, on context cancellation, on a non-retryable
// error, or once MaxAttempts attempts have been made.
func (r *retrier) do(ctx context.Context, fn func() (statusCode int, retryAfter string, err error)) error {
	maxAttempts := r.policy.maxAttempts()
	backoff := r.policy.initialBackoff()
	maxBackoff := r.policy.maxBackoff()

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		code, retryAfterHeader, err := fn()
		lastErr = err

		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if attempt == maxAttempts-1 {
			break
		}
		if !isRetryableStatus(code) && !isRetryableNetworkError(err) {
			return err
		}

		delay := backoff
		if ra := parseRetryAfter(retryAfterHeader); ra > 0 {
			delay = ra
		}
		if delay > maxBackoff {
			delay = maxBackoff
		}
		if err := r.sleepFunc(ctx, delay); err != nil {
			return err
		}

		if backoff > maxBackoff/2 {
			backoff = maxBackoff
		} else {
			backoff *= 2
		}
	}
	return lastErr
}

// isRetryableStatus reports whether code is a transient HTTP status that is
// safe to retry: 408, 409, 425, 429, or any 5xx.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408
		http.StatusConflict,        // 409
		425,                        // Too Early
		http.StatusTooManyRequests: // 429
		return true
	default:
		return code >= 500
	}
}

// isRetryableNetworkError reports whether err is a transient transport-level
// error safe to retry (connection timeout, not context cancellation).
func isRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// parseRetryAfter parses the Retry-After header as either a non-negative
// number of seconds or an HTTP-date. Returns 0 for invalid or past values.
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if s, err := strconv.ParseFloat(header, 64); err == nil && s >= 0 {
		return time.Duration(s * float64(time.Second))
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// retryableSentinel is used by Volume.doRequest to signal a retryable HTTP
// status code to the retrier without surfacing a synthetic error to callers
// of doRequest, which inspect the raw status code themselves.
type retryableSentinel struct{ statusCode int }

func (e *retryableSentinel) Error() string {
	return "e2b: transient http status " + strconv.Itoa(e.statusCode)
}
