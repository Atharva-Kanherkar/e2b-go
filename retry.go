package e2b

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	defaultRetryMaxAttempts    = 3
	defaultRetryInitialBackoff = 500 * time.Millisecond
	defaultRetryMaxBackoff     = 30 * time.Second
)

// RetryPolicy controls how the client retries transient HTTP failures.
//
// A zero RetryPolicy uses conservative defaults: three total attempts, 500 ms
// initial back-off, 30 s maximum back-off.  Set MaxAttempts to 1 to disable
// retries entirely.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	// Zero means 3.  Set to 1 to disable retries.
	MaxAttempts int
	// InitialBackoff is the delay before the second attempt.
	// Zero means 500 ms.
	InitialBackoff time.Duration
	// MaxBackoff caps the per-attempt sleep during exponential back-off.
	// Zero means 30 s.
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

// isRetryableStatus reports whether statusCode should trigger a retry.
// Retryable codes: 408, 409, 425, 429, and any 5xx.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, // 408
		http.StatusConflict,        // 409
		http.StatusTooEarly,        // 425
		http.StatusTooManyRequests: // 429
		return true
	}
	return code >= 500
}

// isTransientNetworkError reports whether err is a transient network error
// that is safe to retry (currently: timeout errors only).  Context
// cancellation and deadline expiry are never considered transient.
func isTransientNetworkError(err error) bool {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return false
	}
	if errors.Is(urlErr.Err, context.Canceled) || errors.Is(urlErr.Err, context.DeadlineExceeded) {
		return false
	}
	return urlErr.Timeout()
}

// parseRetryAfter returns the duration indicated by a Retry-After response
// header.  It accepts both the integer-seconds form ("120") and the HTTP-date
// form ("Wed, 21 Oct 2015 07:28:00 GMT").  Returns 0 if the header is absent
// or unparseable.
func parseRetryAfter(header http.Header) time.Duration {
	if header == nil {
		return 0
	}
	v := header.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// sleepFunc is the function used to sleep between retries.  It is a field on
// apiClient so tests can replace it with a no-op to keep tests fast.
type sleepFunc func(ctx context.Context, d time.Duration) error

func defaultSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// doWithRetry executes fn up to policy.maxAttempts() times, sleeping between
// attempts using the provided sleep function.
//
// fn returns (statusCode, responseHeaders, retryable, err).  The loop stops
// immediately when err is nil, retryable is false, or the maximum attempt
// count is reached.  Retry-After headers are honoured and override the
// computed exponential back-off (subject to MaxBackoff).
func doWithRetry(
	ctx context.Context,
	policy RetryPolicy,
	sleep sleepFunc,
	fn func() (int, http.Header, bool, error),
) (int, http.Header, error) {
	maxAttempts := policy.maxAttempts()
	backoff := policy.initialBackoff()
	maxB := policy.maxBackoff()

	var (
		lastStatus int
		lastHeader http.Header
	)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return lastStatus, lastHeader, err
		}

		status, header, retryable, err := fn()
		lastStatus, lastHeader = status, header

		if err == nil || !retryable || attempt == maxAttempts {
			return status, header, err
		}

		wait := backoff
		if ra := parseRetryAfter(header); ra > 0 {
			wait = ra
		}
		if wait > maxB {
			wait = maxB
		}

		if sleepErr := sleep(ctx, wait); sleepErr != nil {
			return status, header, sleepErr
		}

		backoff = min(backoff*2, maxB)
	}

	// Reached only when maxAttempts is satisfied inside the loop (compiler
	// cannot prove it, so a return is required here).
	return lastStatus, lastHeader, nil
}
