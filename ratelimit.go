package jev

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// The documented limits for jev-1.13.0. TypeSafe adjusts them without notice,
// and enterprise plans get higher ones, so they are a starting point and not a
// contract. See [current models].
//
// [current models]: https://docs.typesafe.ai/models#current-models
const (
	// DefaultRequestsPerMinute is the documented request limit per account.
	DefaultRequestsPerMinute = 1200
	// DefaultTokensPerSecond is the documented input-token limit per account.
	DefaultTokensPerSecond = 250_000
)

// initialBytesPerToken is the first guess at how many request-body bytes make
// one input token. English prose is close to 4; JSON with short keys is lower.
// The limiter replaces it with the ratio the API reports after the first call.
const initialBytesPerToken = 4.0

// A Limiter blocks until the next request may be sent, or until ctx ends.
//
// [*rate.Limiter] from golang.org/x/time/rate satisfies this as written, and
// [NewLimiter] returns one that also paces input tokens. An implementation
// must be safe for concurrent use: the client waits on it from every goroutine
// that sends, and a Limiter shared between clients is waited on by all of them.
//
// The API counts requests and tokens per account, so two clients on the same
// key share one budget. Hand them the same Limiter with [WithRateLimiter].
// See [handling rate limits].
//
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
type Limiter interface {
	Wait(ctx context.Context) error
}

// RateLimit is the client-side pacing for one account.
// Start from [DefaultRateLimit] and change the fields you care about.
// A zero field disables that axis; the zero RateLimit paces nothing.
//
// The numbers to match are on the [models page]. They are per account, so if
// several processes share a key, divide the budget between them.
//
// [models page]: https://docs.typesafe.ai/models#current-models
type RateLimit struct {
	// RequestsPerMinute caps how many requests this limiter lets through.
	// Requests are spread across the minute: the burst is one second's worth,
	// so a quiet client can send about RequestsPerMinute/60 at once.
	RequestsPerMinute int
	// TokensPerSecond caps input tokens. The body size is not known in tokens
	// before the call, so the limiter estimates from the body length, then
	// corrects itself with usage.input_tokens from each response.
	// The burst is one second's worth; a request larger than that waits for
	// a full second's budget.
	TokensPerSecond int
}

// DefaultRateLimit is the documented limit for jev-1.13.0: 1,200 requests per
// minute and 250,000 input tokens per second. See [current models].
//
// [current models]: https://docs.typesafe.ai/models#current-models
func DefaultRateLimit() RateLimit {
	return RateLimit{
		RequestsPerMinute: DefaultRequestsPerMinute,
		TokensPerSecond:   DefaultTokensPerSecond,
	}
}

func (l RateLimit) validate() error {
	if l.RequestsPerMinute < 0 || l.TokensPerSecond < 0 {
		return fmt.Errorf("%w: rate limits must not be negative", ErrConfig)
	}
	return nil
}

// IsZero reports whether the limit paces nothing.
func (l RateLimit) IsZero() bool {
	return l.RequestsPerMinute <= 0 && l.TokensPerSecond <= 0
}

// NewLimiter returns a Limiter for limit. Pass it to [WithRateLimiter] on
// every client that shares the account, so they share one budget:
//
//	shared := jev.NewLimiter(jev.DefaultRateLimit())
//	fast, _ := jev.New(jev.WithModel("jev-latest"), jev.WithRateLimiter(shared))
//	pinned, _ := jev.New(jev.WithModel("jev-1.13.0"), jev.WithRateLimiter(shared))
//
// Besides pacing, the limiter honors Retry-After. When a response carries one,
// every goroutine waiting on this limiter pauses until that time. A 429 tells
// the whole account to slow down, not only the call that saw it. See
// [handling rate limits].
//
// A zero limit returns nil, which disables pacing. The result is the interface
// rather than a concrete type on purpose: a nil pointer inside a non-nil
// interface would pass a nil check and panic on the first Wait.
//
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
func NewLimiter(limit RateLimit) Limiter {
	if limit.IsZero() {
		return nil
	}
	l := &limiter{}
	if limit.RequestsPerMinute > 0 {
		perSecond := float64(limit.RequestsPerMinute) / 60
		l.requests = rate.NewLimiter(rate.Limit(perSecond), max(1, int(math.Ceil(perSecond))))
	}
	if limit.TokensPerSecond > 0 {
		l.tokens = rate.NewLimiter(rate.Limit(limit.TokensPerSecond), limit.TokensPerSecond)
	}
	l.bytesPerToken.Store(math.Float64bits(initialBytesPerToken))
	return l
}

// WithRateLimit paces requests and input tokens to limit, on top of retries.
// The default is [DefaultRateLimit], the documented account limit, so a client
// stays under it instead of finding it through 429 responses.
//
// Pass the zero [RateLimit] to disable pacing. Pass it to [New] only: a limiter
// built for one call would start with a full budget and pace nothing. For one
// call, use [WithRateLimiter] with a limiter you own, or nil to skip pacing.
//
// The limit is per account. Several clients on the same key should share one
// limiter through [NewLimiter] and [WithRateLimiter]. See [current models].
//
// [current models]: https://docs.typesafe.ai/models#current-models
func WithRateLimit(limit RateLimit) Option {
	return func(cfg *config) error {
		if cfg.perCall {
			return fmt.Errorf("%w: WithRateLimit applies to New; for one call pass WithRateLimiter", ErrConfig)
		}
		if err := limit.validate(); err != nil {
			return err
		}
		cfg.limiter = NewLimiter(limit)
		cfg.limiterSet = true
		return nil
	}
}

// WithRateLimiter uses a Limiter you own, in place of the one [WithRateLimit]
// builds. Clients given the same Limiter share one budget, which is what an
// account-wide quota asks for. Nil disables pacing.
//
// A plain [*rate.Limiter] paces requests only. The Limiter from [NewLimiter]
// also paces tokens and honors Retry-After. See [handling rate limits].
//
// As a call option it replaces the client's limiter for that call.
//
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
func WithRateLimiter(l Limiter) Option {
	return func(cfg *config) error {
		cfg.limiter = limiterOrNil(l)
		cfg.limiterSet = true
		return nil
	}
}

// limiterOrNil unwraps the typed nils this package can meet. A nil pointer
// inside a non-nil interface passes an "l != nil" check and panics on Wait.
func limiterOrNil(l Limiter) Limiter {
	switch v := l.(type) {
	case nil:
		return nil
	case *rate.Limiter:
		if v == nil {
			return nil
		}
	case *limiter:
		if v == nil {
			return nil
		}
	}
	return l
}

// limiter is the Limiter behind NewLimiter: a request bucket, a token bucket,
// a self-correcting bytes-per-token ratio, and a pause set by Retry-After.
type limiter struct {
	requests *rate.Limiter
	tokens   *rate.Limiter

	// bytesPerToken holds a float64 as bits. It starts at initialBytesPerToken
	// and moves toward the ratio the API reports.
	bytesPerToken atomic.Uint64
	// pausedUntil is a unix nanosecond timestamp. Zero means not paused.
	pausedUntil atomic.Int64
}

// Wait blocks for a server pause, then for one request token.
func (l *limiter) Wait(ctx context.Context) error {
	if err := l.waitPause(ctx); err != nil {
		return err
	}
	if l.requests == nil {
		return nil
	}
	return waitN(ctx, l.requests, 1)
}

// waitTokens blocks until n estimated input tokens are available.
// A request larger than the burst waits for the full burst.
func (l *limiter) waitTokens(ctx context.Context, n int) error {
	if l.tokens == nil || n <= 0 {
		return nil
	}
	return waitN(ctx, l.tokens, min(n, l.tokens.Burst()))
}

// estimateTokens guesses the input tokens of a body from its length.
func (l *limiter) estimateTokens(bodyLen int) int {
	if l.tokens == nil || bodyLen <= 0 {
		return 0
	}
	ratio := math.Float64frombits(l.bytesPerToken.Load())
	return max(1, int(math.Ceil(float64(bodyLen)/ratio)))
}

// observe records what the API charged. Tokens beyond the estimate are taken
// from the bucket now, so later calls wait for them, and the ratio moves
// toward what was seen.
func (l *limiter) observe(bodyLen, estimated, actual int) {
	if l.tokens == nil || actual <= 0 {
		return
	}
	if extra := actual - estimated; extra > 0 {
		// A reservation over the burst is not OK and takes nothing; the
		// estimate was capped at the burst already, so the bucket owes at most
		// one burst.
		l.tokens.ReserveN(time.Now(), min(extra, l.tokens.Burst()))
	}
	if bodyLen <= 0 {
		return
	}
	observed := float64(bodyLen) / float64(actual)
	observed = math.Max(1, math.Min(observed, 16))
	for {
		oldBits := l.bytesPerToken.Load()
		old := math.Float64frombits(oldBits)
		next := old*0.7 + observed*0.3
		if l.bytesPerToken.CompareAndSwap(oldBits, math.Float64bits(next)) {
			return
		}
	}
}

// pause holds every caller until d from now. A shorter pause never replaces a
// longer one already in place.
func (l *limiter) pause(d time.Duration) {
	if d <= 0 {
		return
	}
	until := time.Now().Add(d).UnixNano()
	for {
		current := l.pausedUntil.Load()
		if current >= until || l.pausedUntil.CompareAndSwap(current, until) {
			return
		}
	}
}

func (l *limiter) waitPause(ctx context.Context) error {
	until := l.pausedUntil.Load()
	if until == 0 {
		return nil
	}
	remaining := time.Until(time.Unix(0, until))
	if remaining <= 0 {
		l.pausedUntil.CompareAndSwap(until, 0)
		return nil
	}
	return sleep(ctx, remaining)
}

// waitN reserves n tokens and sleeps for the delay. Unlike rate.Limiter.WaitN
// it does not fail early when the delay outruns the context deadline; the
// deadline fires and the caller gets that context error, like everywhere else
// in this client.
func waitN(ctx context.Context, l *rate.Limiter, n int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := l.ReserveN(time.Now(), n)
	if !r.OK() {
		return fmt.Errorf("%w: rate limiter: %d exceeds the burst of %d", ErrRequest, n, l.Burst())
	}
	delay := r.Delay()
	if delay == 0 {
		return nil
	}
	if err := sleep(ctx, delay); err != nil {
		r.Cancel()
		return err
	}
	return nil
}

// sleep waits for d or until ctx ends, and returns the context error in that case.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitLimiter is the pre-attempt gate: the request limiter, then the token
// limiter when the limiter has one. A custom Limiter's error is returned as
// it came when it is the context's own; anything else is an ErrRequest.
func waitLimiter(ctx context.Context, l Limiter, estimatedTokens int) error {
	if l == nil {
		return nil
	}
	if err := l.Wait(ctx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if own, ok := l.(*limiter); ok && own != nil {
			return err
		}
		return fmt.Errorf("%w: rate limiter: %v", ErrRequest, err)
	}
	if own, ok := l.(*limiter); ok {
		return own.waitTokens(ctx, estimatedTokens)
	}
	return nil
}
