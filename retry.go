package jev

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy controls retries after the first attempt.
// Start from [DefaultRetryPolicy] and change the fields you care about.
// A policy built from the zero value retries nothing, because Statuses is empty.
//
// The fields mirror the official SDKs' [RetryPolicy], with Go names and
// durations. The API's own advice on what to retry is under
// [handling rate limits].
//
// [RetryPolicy]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/RetryPolicy
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
type RetryPolicy struct {
	// MaxRetries is how many retries follow the first attempt. 0 disables retries.
	// SDK name: maxRetries.
	MaxRetries int
	// InitialBackoff is the first backoff delay, doubled on each retry up to MaxBackoff.
	// SDK name: backoffInitialMs.
	InitialBackoff time.Duration
	// MaxBackoff caps the computed backoff. SDK name: backoffMaxMs.
	MaxBackoff time.Duration
	// Jitter is the fraction of each backoff delay that may be subtracted, from 0 to 1.
	// SDK name: backoffJitter.
	Jitter float64
	// Statuses are the HTTP status codes that are retried. SDK name: httpStatuses.
	Statuses []int
	// RespectRetryAfter honors Retry-After-Ms and Retry-After when the delay
	// is within MaxRetryAfter. A longer server delay falls back to backoff.
	// SDK name: respectRetryAfter.
	RespectRetryAfter bool
	// MaxRetryAfter is the longest server-requested delay that is honored,
	// by retries and by the pause a [Limiter] from [NewLimiter] applies to
	// every caller. SDK name: maxRetryAfterMs.
	MaxRetryAfter time.Duration
	// RetryConnection retries connection failures. SDK name: apiConnectionError.
	RetryConnection bool
	// RetryTimeout retries an attempt that hit the per-attempt timeout.
	// SDK name: apiTimeoutError.
	RetryTimeout bool
	// MaxElapsed is a budget for the whole call, including waits between attempts.
	// Zero means there is no budget. The caller's context always applies.
	MaxElapsed time.Duration

	// randFloat is replaced in tests. nil uses math/rand/v2.
	randFloat func() float64
}

// DefaultRetryPolicy matches the official JavaScript SDK:
// 2 retries, 500ms backoff doubling to 5s, 25% jitter, and retries on
// 408, 429, and 500-599. Retry-After is honored up to 60s.
// There is no total time budget unless you set MaxElapsed.
// The defaults are listed field by field under [RetryPolicy].
//
// [RetryPolicy]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/RetryPolicy
func DefaultRetryPolicy() RetryPolicy {
	statuses := []int{http.StatusRequestTimeout, http.StatusTooManyRequests}
	for status := 500; status <= 599; status++ {
		statuses = append(statuses, status)
	}
	return RetryPolicy{
		MaxRetries:        2,
		InitialBackoff:    500 * time.Millisecond,
		MaxBackoff:        5 * time.Second,
		Jitter:            0.25,
		Statuses:          statuses,
		RespectRetryAfter: true,
		MaxRetryAfter:     time.Minute,
		RetryConnection:   true,
		RetryTimeout:      true,
	}
}

func (p RetryPolicy) clone() RetryPolicy {
	p.Statuses = slices.Clone(p.Statuses)
	return p
}

func (p RetryPolicy) validate() error {
	if p.MaxRetries < 0 {
		return fmt.Errorf("%w: MaxRetries must not be negative", ErrConfig)
	}
	if p.InitialBackoff < 0 || p.MaxBackoff < 0 || p.MaxRetryAfter < 0 || p.MaxElapsed < 0 {
		return fmt.Errorf("%w: retry delays must not be negative", ErrConfig)
	}
	if math.IsNaN(p.Jitter) || p.Jitter < 0 || p.Jitter > 1 {
		return fmt.Errorf("%w: Jitter must be between 0 and 1", ErrConfig)
	}
	for _, status := range p.Statuses {
		if status < 100 || status > 999 {
			return fmt.Errorf("%w: status %d is not an HTTP status code", ErrConfig, status)
		}
	}
	return nil
}

func (p RetryPolicy) retries(err error) bool {
	var conn *ConnectionError
	var api *APIError
	switch {
	case errors.As(err, &conn):
		if conn.Timeout > 0 {
			return p.RetryTimeout
		}
		return p.RetryConnection
	case errors.As(err, &api):
		return slices.Contains(p.Statuses, api.Status)
	default:
		return false
	}
}

// delay is the wait before a retry. attempt is zero-based: the first retry is 0.
func (p RetryPolicy) delay(attempt int, headers http.Header, now time.Time) time.Duration {
	if p.RespectRetryAfter {
		if d, ok := parseRetryAfter(headers, now); ok && d <= p.MaxRetryAfter {
			return d
		}
	}
	exp := p.InitialBackoff
	if exp < 0 {
		exp = 0
	}
	for range attempt {
		if p.MaxBackoff > 0 && exp >= p.MaxBackoff {
			exp = p.MaxBackoff
			break
		}
		if exp > time.Duration(math.MaxInt64/2) {
			exp = p.MaxBackoff
			break
		}
		exp *= 2
	}
	if p.MaxBackoff > 0 && exp > p.MaxBackoff {
		exp = p.MaxBackoff
	}
	if exp == 0 || p.Jitter == 0 {
		return exp
	}
	roll := rand.Float64
	if p.randFloat != nil {
		roll = p.randFloat
	}
	factor := 1 - roll()*p.Jitter
	return time.Duration(math.Round(float64(exp) * factor))
}

func parseRetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if h == nil {
		return 0, false
	}
	if raw := strings.TrimSpace(h.Get("Retry-After-Ms")); raw != "" {
		ms, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 {
			return 0, false
		}
		return time.Duration(ms * float64(time.Millisecond)), true
	}
	raw := strings.TrimSpace(h.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsNaN(sec) || math.IsInf(sec, 0) || sec < 0 {
			return 0, false
		}
		return time.Duration(sec * float64(time.Second)), true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	d := when.Sub(now)
	if d < 0 {
		d = 0
	}
	return d, true
}
