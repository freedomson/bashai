package jev

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/time/rate"
)

// Timing assertions run inside synctest bubbles, where the clock is virtual
// and a wait of one second costs nothing. That needs an in-process transport:
// httptest.NewServer would spawn goroutines outside the bubble. inProcess
// builds one from a handler. Pick rates that are exact in float64, such as
// 2/s (500ms) or 4/s (250ms); 3/s would be 333333333ns.

const okBody = `{
	"model":"jev-1.13.0",
	"answers":{"q":{"type":"noul","noul":0.5}},
	"usage":{"input_tokens":10,"output_tokens":1}
}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func inProcess(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
}

func okHandler(calls *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, okBody)
	})
}

// flakyHandler fails with each status in turn, then succeeds.
func flakyHandler(calls *atomic.Int32, statuses ...int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(calls.Add(1))
		if n <= len(statuses) {
			w.WriteHeader(statuses[n-1])
			_, _ = io.WriteString(w, `{"error":"later"}`)
			return
		}
		_, _ = io.WriteString(w, okBody)
	})
}

func fastRetry(maxRetries int) RetryPolicy {
	p := DefaultRetryPolicy()
	p.MaxRetries = maxRetries
	p.InitialBackoff = time.Millisecond
	p.MaxBackoff = time.Millisecond
	p.Jitter = 0
	return p
}

func oneNoul() Request {
	return Request{State: "x", Questions: Questions{"q": Noul{Instructions: "q?"}}}
}

func mustNew(t *testing.T, opts ...Option) *Client {
	t.Helper()
	client, err := New(append([]Option{WithAPIKey("k"), WithBaseURL("http://jev.test")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestDefaultLimiterMatchesTheDocumentedLimit(t *testing.T) {
	client := mustNew(t)
	own, ok := client.Limiter().(*limiter)
	if !ok {
		t.Fatalf("default limiter is %T", client.Limiter())
	}
	if got := float64(own.requests.Limit()); got != DefaultRequestsPerMinute/60 {
		t.Fatalf("requests per second %v", got)
	}
	if own.requests.Burst() != DefaultRequestsPerMinute/60 {
		t.Fatalf("request burst %d", own.requests.Burst())
	}
	if got := float64(own.tokens.Limit()); got != DefaultTokensPerSecond {
		t.Fatalf("tokens per second %v", got)
	}
	if own.tokens.Burst() != DefaultTokensPerSecond {
		t.Fatalf("token burst %d", own.tokens.Burst())
	}
}

func TestZeroRateLimitDisablesPacing(t *testing.T) {
	var typedNil *rate.Limiter
	var typedOwnNil *limiter
	for name, client := range map[string]*Client{
		"zero limit":       mustNew(t, WithRateLimit(RateLimit{})),
		"nil limiter":      mustNew(t, WithRateLimiter(nil)),
		"typed nil":        mustNew(t, WithRateLimiter(typedNil)),
		"typed own nil":    mustNew(t, WithRateLimiter(typedOwnNil)),
		"clears the built": mustNew(t, WithRateLimit(DefaultRateLimit()), WithRateLimiter(nil)),
	} {
		if client.Limiter() != nil {
			t.Fatalf("%s: limiter is %T, want nil", name, client.Limiter())
		}
	}
	if l := NewLimiter(RateLimit{}); l != nil {
		t.Fatalf("NewLimiter(zero) = %T, want untyped nil", l)
	}
}

func TestRateLimitRejectsNegative(t *testing.T) {
	_, err := New(WithAPIKey("k"), WithRateLimit(RateLimit{RequestsPerMinute: -1}))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("got %v", err)
	}
}

func TestRateLimitIsNotACallOption(t *testing.T) {
	var calls atomic.Int32
	client := mustNew(t, WithHTTPClient(inProcess(okHandler(&calls))))
	_, err := client.SystemOne(context.Background(), oneNoul(), WithRateLimit(DefaultRateLimit()))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("the request was sent")
	}
	// A limiter you own is fine per call, and so is nil to skip pacing.
	if _, err := client.SystemOne(context.Background(), oneNoul(), WithRateLimiter(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestLimiterPacesRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		// Two per second with a burst of two: calls three and four wait 500ms each.
		client := mustNew(t,
			WithHTTPClient(inProcess(okHandler(&calls))),
			WithRateLimit(RateLimit{RequestsPerMinute: 120}),
		)
		start := time.Now()
		for range 4 {
			if _, err := client.SystemOne(context.Background(), oneNoul()); err != nil {
				t.Fatal(err)
			}
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("four calls at 2/s must take 1s, took %v", elapsed)
		}
		if calls.Load() != 4 {
			t.Fatalf("calls %d", calls.Load())
		}
	})
}

func TestSharedLimiterIsOneBudgetAcrossClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		shared := NewLimiter(RateLimit{RequestsPerMinute: 120})
		transport := WithHTTPClient(inProcess(okHandler(&calls)))
		latest := mustNew(t, transport, WithModel("jev-latest"), WithRateLimiter(shared))
		// The second client joins through Client.Limiter, which is how a
		// client that did not build the limiter still shares the budget.
		pinned := mustNew(t, transport, WithModel("jev-1.13.0"), WithRateLimiter(latest.Limiter()))

		start := time.Now()
		var wg sync.WaitGroup
		for _, c := range []*Client{latest, pinned} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 3 {
					if _, err := c.SystemOne(context.Background(), oneNoul()); err != nil {
						t.Error(err)
						return
					}
				}
			}()
		}
		wg.Wait()
		// Six requests on a shared 2/s budget with a burst of two: four wait 500ms each.
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Fatalf("expected 2s, took %v", elapsed)
		}
	})
}

func TestSeparateLimitersAreSeparateBudgets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		transport := WithHTTPClient(inProcess(okHandler(&calls)))
		a := mustNew(t, transport, WithRateLimit(RateLimit{RequestsPerMinute: 120}))
		b := mustNew(t, transport, WithRateLimit(RateLimit{RequestsPerMinute: 120}))
		start := time.Now()
		for _, c := range []*Client{a, b, a, b} {
			if _, err := c.SystemOne(context.Background(), oneNoul()); err != nil {
				t.Fatal(err)
			}
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("each client has its own burst of two, took %v", elapsed)
		}
	})
}

func TestLimiterWaitsOnEveryRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		// One per second with a burst of one: attempts two and three each wait
		// a second, which swallows the millisecond backoff.
		client := mustNew(t,
			WithHTTPClient(inProcess(flakyHandler(&calls, http.StatusServiceUnavailable, http.StatusServiceUnavailable))),
			WithRateLimit(RateLimit{RequestsPerMinute: 60}),
			WithRetry(fastRetry(3)),
		)
		start := time.Now()
		resp, err := client.SystemOne(context.Background(), oneNoul())
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Fatalf("retries must wait on the limiter too, took %v", elapsed)
		}
		if resp.Attempts != 3 || calls.Load() != 3 {
			t.Fatalf("attempts %d calls %d", resp.Attempts, calls.Load())
		}
	})
}

func TestRetryAfterPausesEveryCaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"error":"slow down"}`)
				return
			}
			_, _ = io.WriteString(w, okBody)
		})
		shared := NewLimiter(DefaultRateLimit())
		first := mustNew(t, WithHTTPClient(inProcess(handler)), WithRateLimiter(shared), WithRetry(zeroRetry()))
		second := mustNew(t, WithHTTPClient(inProcess(handler)), WithRateLimiter(shared))

		start := time.Now()
		_, err := first.SystemOne(context.Background(), oneNoul())
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("got %v", err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("a 429 with no retries must return at once, took %v", elapsed)
		}

		// A different client on the same limiter, and a different goroutine,
		// waits out the server's Retry-After before sending.
		if _, err := second.SystemOne(context.Background(), oneNoul()); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 2*time.Second {
			t.Fatalf("the second caller must wait for Retry-After, took %v", elapsed)
		}
		// Once the pause has passed it is gone.
		start = time.Now()
		if _, err := second.SystemOne(context.Background(), oneNoul()); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("an expired pause must not wait, took %v", elapsed)
		}
	})
}

func TestRetryAfterBeyondTheCapDoesNotPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = io.WriteString(w, okBody)
		})
		client := mustNew(t, WithHTTPClient(inProcess(handler)), WithRetry(zeroRetry()))
		if _, err := client.SystemOne(context.Background(), oneNoul()); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("got %v", err)
		}
		start := time.Now()
		if _, err := client.SystemOne(context.Background(), oneNoul()); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != 0 {
			t.Fatalf("a Retry-After over MaxRetryAfter must not pause the client, took %v", elapsed)
		}
	})
}

func TestPauseIsInterruptedByTheContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		own := NewLimiter(DefaultRateLimit()).(*limiter)
		own.pause(time.Minute)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		err := own.Wait(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("took %v", elapsed)
		}
	})
}

func TestTokenLimiterWaitsForTheEstimate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var bodyLen int
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			b, _ := io.ReadAll(r.Body)
			bodyLen = len(b)
			// Report exactly what the 4 bytes-per-token guess predicts, so the
			// ratio does not move and the arithmetic stays exact.
			_, _ = io.WriteString(w, strings.Replace(okBody, `"input_tokens":10`, `"input_tokens":`+strconv.Itoa(len(b)/4), 1))
		})
		client := mustNew(t,
			WithHTTPClient(inProcess(handler)),
			WithRateLimit(RateLimit{TokensPerSecond: 1000}),
		)
		// A state of 1,600 bytes plus the envelope is about 1,700 bytes, so each
		// call reserves about 425 tokens: two fit in the burst, the third waits.
		req := Request{State: strings.Repeat("ab", 800), Questions: Questions{"q": Noul{Instructions: "q?"}}}
		body, _, err := req.encode(client.Model())
		if err != nil {
			t.Fatal(err)
		}
		estimate := int(math.Ceil(float64(len(body)) / initialBytesPerToken))
		if estimate <= 1000/3 || estimate > 1000/2 {
			t.Fatalf("test needs two calls in the burst, estimate %d", estimate)
		}

		start := time.Now()
		for range 3 {
			if _, err := client.SystemOne(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		}
		// Third call needs `estimate` tokens with 1000-2*estimate left.
		want := time.Duration(float64(3*estimate-1000) / 1000 * float64(time.Second))
		if elapsed := time.Since(start); elapsed != want {
			t.Fatalf("token pacing: took %v, want %v (body %d bytes, estimate %d)", elapsed, want, bodyLen, estimate)
		}
	})
}

func TestTokenLimiterCapsARequestAtTheBurst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			b, _ := io.ReadAll(r.Body)
			_, _ = io.WriteString(w, strings.Replace(okBody, `"input_tokens":10`, `"input_tokens":`+strconv.Itoa(len(b)/4), 1))
		})
		client := mustNew(t,
			WithHTTPClient(inProcess(handler)),
			WithRateLimit(RateLimit{TokensPerSecond: 1000}),
		)
		// About 2,000 tokens estimated: over the burst, so each call takes the
		// whole bucket and the second waits a full second.
		req := Request{State: strings.Repeat("ab", 4000), Questions: Questions{"q": Noul{Instructions: "q?"}}}
		start := time.Now()
		for range 2 {
			if _, err := client.SystemOne(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("took %v", elapsed)
		}
	})
}

func TestTokenLimiterSettlesAndLearns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		own := NewLimiter(RateLimit{TokensPerSecond: 1000}).(*limiter)
		ctx := context.Background()

		if got := own.estimateTokens(400); got != 100 {
			t.Fatalf("estimate %d", got)
		}
		if err := own.waitTokens(ctx, 100); err != nil {
			t.Fatal(err)
		}
		// The API charged 600 for a body guessed at 100: the other 500 leave the
		// bucket now.
		own.observe(400, 100, 600)
		if got := own.tokens.Tokens(); got != 400 {
			t.Fatalf("tokens left %v, want 400", got)
		}
		// 400 bytes for 600 tokens is 0.67 bytes per token, clamped to 1, and
		// blended 70/30 into the previous 4.
		want := 4*0.7 + 1*0.3
		if got := math.Float64frombits(own.bytesPerToken.Load()); math.Abs(got-want) > 1e-9 {
			t.Fatalf("bytes per token %v, want %v", got, want)
		}
		// Nothing to settle when the estimate was high; the ratio still moves.
		own.observe(4000, 500, 250)
		if got := own.tokens.Tokens(); got != 400 {
			t.Fatalf("an overestimate must not take tokens, left %v", got)
		}
		if got := math.Float64frombits(own.bytesPerToken.Load()); got <= want {
			t.Fatalf("ratio must rise after a cheaper call, got %v", got)
		}
	})
}

type erringLimiter struct{ err error }

func (l erringLimiter) Wait(ctx context.Context) error {
	if l.err != nil {
		return l.err
	}
	return ctx.Err()
}

func TestCustomLimiterErrors(t *testing.T) {
	var calls atomic.Int32
	transport := WithHTTPClient(inProcess(okHandler(&calls)))

	client := mustNew(t, transport, WithRateLimiter(erringLimiter{err: errors.New("budget exhausted")}))
	_, err := client.SystemOne(context.Background(), oneNoul())
	if !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client = mustNew(t, transport, WithRateLimiter(erringLimiter{}))
	if _, err := client.SystemOne(ctx, oneNoul()); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests were sent", calls.Load())
	}
}

func TestPlainRateLimiterPacesRequestsOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		// golang.org/x/time/rate straight from the caller: 2/s, burst 1.
		client := mustNew(t,
			WithHTTPClient(inProcess(okHandler(&calls))),
			WithRateLimiter(rate.NewLimiter(2, 1)),
		)
		start := time.Now()
		for range 3 {
			if _, err := client.SystemOne(context.Background(), oneNoul()); err != nil {
				t.Fatal(err)
			}
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("took %v", elapsed)
		}
	})
}
