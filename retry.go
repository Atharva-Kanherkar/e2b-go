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

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func shouldRetryHTTPStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	default:
		return statusCode >= 500
	}
}

func shouldRetryNetworkError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	return false
}

func retryBackoff(policy RetryPolicy, attempt int) time.Duration {
	delay := policy.InitialDelay
	if delay <= 0 {
		delay = defaultRetryInitialDelay
	}

	multiplier := policy.Multiplier
	if multiplier <= 0 {
		multiplier = defaultRetryMultiplier
	}

	for i := 0; i < attempt; i++ {
		next := float64(delay) * multiplier
		if next > float64(math.MaxInt64) {
			delay = time.Duration(math.MaxInt64)
			break
		}
		delay = time.Duration(next)
	}

	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultRetryMaxDelay
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func retryDelay(policy RetryPolicy, attempt int, header http.Header, now time.Time) time.Duration {
	if header != nil {
		if retryAfter := parseRetryAfter(header.Get("Retry-After"), now); retryAfter >= 0 {
			return retryAfter
		}
	}
	return retryBackoff(policy, attempt)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return -1
	}
	if retryAt.Before(now) {
		return 0
	}
	return retryAt.Sub(now)
}

func isIdempotentHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}
