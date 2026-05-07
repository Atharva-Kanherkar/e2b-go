package e2b

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type retrySleepFunc func(context.Context, time.Duration) error

func defaultRetrySleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *apiClient) doHTTPWithRetry(ctx context.Context, httpClient *http.Client, method string, rawURL string, bodyBytes []byte, headers http.Header) (int, http.Header, []byte, error) {
	policy := c.config.retryPolicy()

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, nil, nil, err
		}

		var body io.Reader
		if bodyBytes != nil {
			body = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header = headers.Clone()

		resp, err := httpClient.Do(req)
		if err != nil {
			if !c.shouldRetryNetworkError(ctx, err, attempt, policy.MaxAttempts) {
				return 0, nil, nil, err
			}
			if err := c.sleepBeforeRetry(ctx, policy, attempt, 0, false); err != nil {
				return 0, nil, nil, err
			}
			continue
		}

		responseHeaders := resp.Header.Clone()
		responseBytes, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			if !c.shouldRetryNetworkError(ctx, readErr, attempt, policy.MaxAttempts) {
				return resp.StatusCode, responseHeaders, nil, readErr
			}
			if err := c.sleepBeforeRetry(ctx, policy, attempt, 0, false); err != nil {
				return resp.StatusCode, responseHeaders, nil, err
			}
			continue
		}

		if shouldRetryHTTPStatus(resp.StatusCode) && attempt < policy.MaxAttempts {
			delay, hasRetryAfter := retryAfterDelay(responseHeaders.Get("Retry-After"), time.Now())
			if err := c.sleepBeforeRetry(ctx, policy, attempt, delay, hasRetryAfter); err != nil {
				return resp.StatusCode, responseHeaders, responseBytes, err
			}
			continue
		}

		return resp.StatusCode, responseHeaders, responseBytes, nil
	}

	return 0, nil, nil, errors.New("e2b: retry attempts exhausted")
}

func (c *apiClient) shouldRetryNetworkError(ctx context.Context, err error, attempt int, maxAttempts int) bool {
	if attempt >= maxAttempts || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var netErr net.Error
	if !errors.As(err, &netErr) {
		return false
	}
	return netErr.Timeout() || netErr.Temporary()
}

func (c *apiClient) sleepBeforeRetry(ctx context.Context, policy RetryPolicy, attempt int, retryAfter time.Duration, hasRetryAfter bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	delay := retryAfter
	if !hasRetryAfter {
		delay = retryBackoff(policy, attempt)
	}
	if c.sleep == nil {
		return defaultRetrySleep(ctx, delay)
	}
	return c.sleep(ctx, delay)
}

func retryBackoff(policy RetryPolicy, attempt int) time.Duration {
	if attempt <= 1 {
		return policy.InitialBackoff
	}

	delay := policy.InitialBackoff
	for i := 1; i < attempt; i++ {
		if delay > time.Duration(math.MaxInt64/2) {
			return policy.MaxBackoff
		}
		delay *= 2
		if delay >= policy.MaxBackoff {
			return policy.MaxBackoff
		}
	}
	return delay
}

func retryAfterDelay(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0, true
		}
		return time.Duration(seconds) * time.Second, true
	}

	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if !retryAt.After(now) {
		return 0, true
	}
	return retryAt.Sub(now), true
}

func shouldRetryHTTPStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return status >= 500 && status <= 599
	}
}
