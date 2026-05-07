package e2b

import (
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
type retryNowFunc func() time.Time

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

func (p RetryPolicy) backoff(attempt int) time.Duration {
	if attempt <= 1 {
		return p.InitialBackoff
	}

	factor := math.Pow(2, float64(attempt-1))
	delay := time.Duration(float64(p.InitialBackoff) * factor)
	if delay <= 0 || delay > p.MaxBackoff {
		return p.MaxBackoff
	}
	return delay
}

func retryDelay(header http.Header, policy RetryPolicy, attempt int, now time.Time) time.Duration {
	if delay, ok := parseRetryAfter(header.Get("Retry-After"), now); ok {
		return delay
	}
	return policy.backoff(attempt)
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(value); err == nil {
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

func isRetryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return statusCode >= 500
	}
}

func isTemporaryNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Temporary() || netErr.Timeout())
}

func retryTemporaryNetworkErrors(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func closeRetryResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512*1024))
	_ = resp.Body.Close()
}
