package jev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const maxBody = 16 << 20

const (
	pathSystemOne = "/v1/systemone"
	pathModels    = "/v1/models"
)

// Client calls the TypeSafe System One API. It is safe for concurrent use.
// Create it with [New]. The HTTP contract it speaks is the [API reference].
//
// [API reference]: https://docs.typesafe.ai/api
type Client struct {
	cfg config
	seq atomic.Uint64
}

// New returns a client. Options override environment variables, which override
// the defaults. The API key is required; get one from the [quick start].
//
// The precedence, the environment variable names, and the defaults match the
// official SDKs. See [client config] for the JavaScript SDK's version of the
// same table.
//
// [quick start]: https://docs.typesafe.ai/introduction/quickstart
// [client config]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/TypeSafeClientConfig
func New(opts ...Option) (*Client, error) {
	var cfg config
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&cfg); err != nil {
			return nil, err
		}
	}
	if err := cfg.resolve(); err != nil {
		return nil, err
	}
	return &Client{cfg: cfg}, nil
}

// BaseURL returns the API root without a trailing slash.
// The production root is https://api.typesafe.ai; see the [evaluation endpoint].
//
// [evaluation endpoint]: https://docs.typesafe.ai/api#evaluation-endpoint
func (c *Client) BaseURL() string { return c.cfg.baseURL }

// Model returns the model used when a request does not name one.
// The names and aliases the API accepts are listed under [models].
//
// [models]: https://docs.typesafe.ai/models#current-models
func (c *Client) Model() string { return c.cfg.model }

// Limiter returns the client-side rate limiter, or nil when pacing is off.
// Pass it to [WithRateLimiter] on another client so both share one budget,
// which is what the per-account limits under [current models] ask for.
//
// [current models]: https://docs.typesafe.ai/models#current-models
func (c *Client) Limiter() Limiter { return c.cfg.limiter }

// CloseIdleConnections closes idle connections on the underlying HTTP client.
// It does not cancel calls that are in flight.
func (c *Client) CloseIdleConnections() {
	c.cfg.httpClient.CloseIdleConnections()
}

// SystemOne evaluates req.State against req.Questions with one POST to the
// [evaluation endpoint]. One answer comes back for each question, under the
// same name; see [response body].
//
// Several questions about one state belong in one call. The model reads the
// state once and answers them in parallel, so adding a question does not add
// a round trip; see [ask multiple questions together].
//
// [evaluation endpoint]: https://docs.typesafe.ai/api#evaluation-endpoint
// [response body]: https://docs.typesafe.ai/api#response-body
// [ask multiple questions together]: https://docs.typesafe.ai/primitives#ask-multiple-questions-together
func (c *Client) SystemOne(ctx context.Context, req Request, opts ...Option) (*Response, error) {
	_, resp, err := c.systemOne(ctx, req, opts...)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// SystemOneAs evaluates the request and decodes the JSON response into T.
// Object names are case-sensitive. Members that T does not declare are ignored.
// The JSON shape to mirror in T is documented under [response body] and
// [answer types].
//
// [response body]: https://docs.typesafe.ai/api#response-body
// [answer types]: https://docs.typesafe.ai/api#answer-types
func (c *Client) SystemOneAs[T any](ctx context.Context, req Request, opts ...Option) (T, error) {
	var out T
	raw, _, err := c.systemOne(ctx, req, opts...)
	if err != nil {
		return out, err
	}
	if err := unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	return out, nil
}

func (c *Client) systemOne(ctx context.Context, req Request, opts ...Option) ([]byte, *Response, error) {
	cfg, err := c.forCall(opts)
	if err != nil {
		return nil, nil, err
	}
	body, specs, err := req.encode(cfg.model)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.do(ctx, cfg, http.MethodPost, pathSystemOne, body)
	if err != nil {
		return nil, nil, err
	}
	resp, err := decodeResponse(res.body, specs)
	if err != nil {
		return nil, nil, stampResponse(err, http.MethodPost, res.url, res.requestID, res.body)
	}
	settleTokens(cfg.limiter, len(body), res.estimatedTokens, resp.Usage.InputTokens)
	resp.RequestID = res.requestID
	resp.Attempts = res.attempts
	return res.body, resp, nil
}

// ModelInfo is one model the account can call, as returned by [listing models].
// ReleaseDate is whatever string the API returns. The docs describe a calendar
// date; the service currently returns a full timestamp.
//
// [listing models]: https://docs.typesafe.ai/models#listing-models
type ModelInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

// ListModels returns the models available to the account with GET /v1/models.
// The list holds the aliases, such as jev-latest. Versioned IDs such as
// jev-1.13.0 are accepted by the model field whether or not they appear here.
// See [listing models] and [aliases].
//
// [listing models]: https://docs.typesafe.ai/models#listing-models
// [aliases]: https://docs.typesafe.ai/models#aliases
func (c *Client) ListModels(ctx context.Context, opts ...Option) ([]ModelInfo, error) {
	cfg, err := c.forCall(opts)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, cfg, http.MethodGet, pathModels, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Models []ModelInfo `json:"models"`
	}
	if err := unmarshal(res.body, &payload); err != nil || payload.Models == nil {
		return nil, stampResponse(
			fmt.Errorf("%w: expected an object with a models array", ErrResponse),
			http.MethodGet, res.url, res.requestID, res.body,
		)
	}
	for i, model := range payload.Models {
		if strings.TrimSpace(model.Name) == "" {
			return nil, stampResponse(
				fieldError(fmt.Sprintf("models.%d.name", i), fmt.Errorf("%w: name is missing", ErrResponse)),
				http.MethodGet, res.url, res.requestID, res.body,
			)
		}
	}
	return payload.Models, nil
}

func (c *Client) forCall(opts []Option) (config, error) {
	cfg := c.cfg.clone()
	cfg.perCall = true
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&cfg); err != nil {
			return config{}, err
		}
	}
	if strings.TrimSpace(cfg.apiKey) == "" || strings.ContainsAny(cfg.apiKey, " \r\n") {
		return config{}, fmt.Errorf("%w: API key is missing or contains whitespace", ErrConfig)
	}
	parsed, err := parseBaseURL(cfg.baseURL)
	if err != nil {
		return config{}, err
	}
	cfg.baseURL = parsed
	if strings.TrimSpace(cfg.model) == "" {
		return config{}, fmt.Errorf("%w: model is required", ErrConfig)
	}
	if cfg.timeout <= 0 {
		return config{}, fmt.Errorf("%w: timeout must be positive", ErrConfig)
	}
	if err := cfg.retry.validate(); err != nil {
		return config{}, err
	}
	if cfg.httpClient == nil {
		return config{}, fmt.Errorf("%w: HTTP client is nil", ErrConfig)
	}
	return cfg, nil
}

type result struct {
	status    int
	header    http.Header
	body      []byte
	url       string
	requestID string
	attempts  int
	// estimatedTokens is what the token limiter reserved before sending,
	// so the caller can settle the difference once usage is known.
	estimatedTokens int
}

func (c *Client) do(ctx context.Context, cfg config, method, path string, body []byte) (result, error) {
	if ctx == nil {
		return result{}, fmt.Errorf("%w: nil context", ErrRequest)
	}
	url := cfg.baseURL + path
	started := time.Now()
	estimated := estimateTokens(cfg.limiter, len(body))
	var last error
	var lastHeader http.Header
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return result{}, err
		}
		if attempt > 0 {
			wait := cfg.retry.delay(attempt-1, lastHeader, time.Now())
			if cfg.retry.MaxElapsed > 0 && time.Since(started)+wait > cfg.retry.MaxElapsed {
				return result{}, withAttempts(last, attempt)
			}
			if err := sleep(ctx, wait); err != nil {
				return result{}, err
			}
		}
		// Every attempt waits, retries included: each one is a request the
		// account is charged for.
		waitStarted := time.Now()
		if err := waitLimiter(ctx, cfg.limiter, estimated); err != nil {
			return result{}, err
		}
		if waited := time.Since(waitStarted); waited > 0 {
			c.logWait(ctx, cfg, method, path, waited, estimated)
		}

		n := c.seq.Add(1)
		res, err := c.attempt(ctx, cfg, method, url, body, attempt, n)
		if err != nil {
			if ctx.Err() != nil {
				return result{}, ctx.Err()
			}
			last = err
			lastHeader = nil
		} else if res.status >= 200 && res.status < 300 {
			res.attempts = attempt + 1
			res.estimatedTokens = estimated
			return res, nil
		} else {
			last = &APIError{
				Status:    res.status,
				Method:    method,
				URL:       url,
				Header:    res.header,
				Body:      res.body,
				Message:   extractMessage(res.body),
				RequestID: res.requestID,
			}
			lastHeader = res.header
			pauseLimiter(cfg, res.header)
		}
		if attempt >= cfg.retry.MaxRetries || !cfg.retry.retries(last) {
			return result{}, withAttempts(last, attempt+1)
		}
		c.logRetry(ctx, cfg, n, method, path, attempt, cfg.retry.MaxRetries, last)
	}
}

// estimateTokens asks the client's own limiter for a token estimate.
// Any other Limiter paces requests only, so the estimate is zero.
func estimateTokens(l Limiter, bodyLen int) int {
	if own, ok := l.(*limiter); ok && own != nil {
		return own.estimateTokens(bodyLen)
	}
	return 0
}

// pauseLimiter holds every caller on the client's own limiter until the
// Retry-After in a failed response, within the retry policy's MaxRetryAfter.
// The same cap as retries applies: a longer server delay is not waited out.
func pauseLimiter(cfg config, header http.Header) {
	own, ok := cfg.limiter.(*limiter)
	if !ok || own == nil {
		return
	}
	d, ok := parseRetryAfter(header, time.Now())
	if !ok || d <= 0 || d > cfg.retry.MaxRetryAfter {
		return
	}
	own.pause(d)
}

// settleTokens tells the client's own limiter what the API charged.
func settleTokens(l Limiter, bodyLen, estimated, actual int) {
	if own, ok := l.(*limiter); ok && own != nil {
		own.observe(bodyLen, estimated, actual)
	}
}

func (c *Client) attempt(ctx context.Context, cfg config, method, url string, body []byte, attempt int, id uint64) (result, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(attemptCtx, method, url, reader)
	if err != nil {
		return result{}, fmt.Errorf("%w: %v", ErrRequest, err)
	}
	for key, values := range cfg.headers {
		req.Header[key] = append([]string(nil), values...)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "jev/"+Version)
	req.Header.Set("X-TypeSafe-SDK", "jev/"+Version)
	req.Header.Set("X-TypeSafe-Runtime", fmt.Sprintf("go/%s %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if attempt > 0 {
		req.Header.Set("X-TypeSafe-Retry-Count", fmt.Sprintf("%d", attempt))
	}

	c.logRequest(ctx, cfg, id, method, url, attempt, req.Header, body)
	started := time.Now()
	resp, err := cfg.httpClient.Do(req)
	if err != nil {
		c.logFailure(ctx, cfg, id, method, url, time.Since(started), err)
		return result{}, transportError(err, ctx, cfg.timeout, method, url)
	}
	defer resp.Body.Close()

	payload, err := readBody(resp.Body)
	if err != nil {
		if errors.Is(err, ErrBodyTooLarge) {
			c.logFailure(ctx, cfg, id, method, url, time.Since(started), err)
			return result{}, &ResponseError{
				Method:    method,
				URL:       url,
				RequestID: resp.Header.Get("X-Typesafe-Request-Id"),
				Body:      payload,
				Err:       ErrBodyTooLarge,
			}
		}
		if ctx.Err() != nil {
			return result{}, ctx.Err()
		}
		c.logFailure(ctx, cfg, id, method, url, time.Since(started), err)
		return result{}, transportError(err, ctx, cfg.timeout, method, url)
	}
	requestID := resp.Header.Get("X-Typesafe-Request-Id")
	c.logResponse(ctx, cfg, id, method, url, resp.StatusCode, time.Since(started), requestID, payload)
	return result{
		status:    resp.StatusCode,
		header:    resp.Header.Clone(),
		body:      payload,
		url:       url,
		requestID: requestID,
	}, nil
}

func readBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBody {
		return b[:maxBody], ErrBodyTooLarge
	}
	return b, nil
}

func transportError(err error, parent context.Context, timeout time.Duration, method, url string) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	timedOut := errors.Is(err, context.DeadlineExceeded)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		timedOut = true
	}
	ce := &ConnectionError{Method: method, URL: url, Err: err}
	if timedOut {
		ce.Timeout = timeout
		ce.Err = fmt.Errorf("attempt exceeded %s", timeout)
	}
	return ce
}

func withAttempts(err error, n int) error {
	switch e := err.(type) {
	case *APIError:
		e.Attempts = n
	case *ConnectionError:
		e.Attempts = n
	}
	return err
}

func stampResponse(err error, method, url, requestID string, body []byte) error {
	var re *ResponseError
	if errors.As(err, &re) {
		if re.Method == "" {
			re.Method = method
		}
		if re.URL == "" {
			re.URL = url
		}
		if re.RequestID == "" {
			re.RequestID = requestID
		}
		if re.Body == nil {
			re.Body = body
		}
		return err
	}
	return &ResponseError{Method: method, URL: url, RequestID: requestID, Body: body, Err: err}
}

func (c *Client) logRequest(ctx context.Context, cfg config, id uint64, method, url string, attempt int, header http.Header, body []byte) {
	if cfg.logger == nil || !cfg.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	cfg.logger.Log(ctx, slog.LevelDebug, "typesafe request",
		slog.Uint64("id", id),
		slog.String("method", method),
		slog.String("url", url),
		slog.Int("attempt", attempt),
		slog.String("headers", redactHeaders(header)),
		slog.String("body", string(body)),
	)
}

func (c *Client) logResponse(ctx context.Context, cfg config, id uint64, method, url string, status int, elapsed time.Duration, requestID string, body []byte) {
	if cfg.logger == nil {
		return
	}
	cfg.logger.Log(ctx, slog.LevelInfo, "typesafe response",
		slog.Uint64("id", id),
		slog.String("method", method),
		slog.String("url", url),
		slog.Int("status", status),
		slog.Int64("ms", elapsed.Milliseconds()),
		slog.String("request_id", requestID),
	)
	if cfg.logger.Enabled(ctx, slog.LevelDebug) {
		cfg.logger.Log(ctx, slog.LevelDebug, "typesafe response body", slog.Uint64("id", id), slog.String("body", string(body)))
	}
}

func (c *Client) logWait(ctx context.Context, cfg config, method, path string, waited time.Duration, estimatedTokens int) {
	if cfg.logger == nil || !cfg.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	cfg.logger.Log(ctx, slog.LevelDebug, "typesafe rate limit wait",
		slog.String("method", method),
		slog.String("path", path),
		slog.Int64("ms", waited.Milliseconds()),
		slog.Int("estimated_tokens", estimatedTokens),
	)
}

func (c *Client) logFailure(ctx context.Context, cfg config, id uint64, method, url string, elapsed time.Duration, err error) {
	if cfg.logger == nil {
		return
	}
	cfg.logger.Log(ctx, slog.LevelInfo, "typesafe request failed",
		slog.Uint64("id", id),
		slog.String("method", method),
		slog.String("url", url),
		slog.Int64("ms", elapsed.Milliseconds()),
		slog.String("err", err.Error()),
	)
}

func (c *Client) logRetry(ctx context.Context, cfg config, id uint64, method, path string, attempt, max int, err error) {
	if cfg.logger == nil {
		return
	}
	cfg.logger.Log(ctx, slog.LevelInfo, "typesafe retry",
		slog.Uint64("id", id),
		slog.String("method", method),
		slog.String("path", path),
		slog.Int("retry", attempt+1),
		slog.Int("max", max),
		slog.String("err", err.Error()),
	)
}

func redactHeaders(h http.Header) string {
	if h == nil {
		return ""
	}
	clone := h.Clone()
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key"} {
		if clone.Get(key) != "" {
			clone.Set(key, "[redacted]")
		}
	}
	// Sorted, so two log lines for the same request compare equal.
	var b strings.Builder
	for _, key := range slices.Sorted(maps.Keys(clone)) {
		for _, value := range clone[key] {
			if b.Len() > 0 {
				b.WriteString("; ")
			}
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(value)
		}
	}
	return b.String()
}
