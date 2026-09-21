package jev

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.typesafe.ai"
	defaultModel   = "jev-latest"
	defaultTimeout = 10 * time.Second

	envAPIKey   = "TYPESAFE_API_KEY"
	envBaseURL  = "TYPESAFE_BASE_URL"
	envModel    = "TYPESAFE_DEFAULT_MODEL"
	envLogLevel = "TYPESAFE_LOG_LEVEL"
)

// Option configures a [Client]. Pass options to [New], or to a single call.
// A call option overrides the client for that call only.
// An empty string means "not set" and leaves the current value in place.
//
// The settings, their environment variables, and their defaults are the ones
// the official SDKs use; see [client config].
//
// [client config]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/TypeSafeClientConfig
type Option func(*config) error

type config struct {
	apiKey     string
	baseURL    string
	model      string
	timeout    time.Duration
	retry      RetryPolicy
	retrySet   bool
	headers    http.Header
	httpClient *http.Client
	logger     *slog.Logger
	loggerSet  bool
	limiter    Limiter
	limiterSet bool

	// perCall is set while call options are applied, so options that only
	// make sense on New can refuse.
	perCall bool
}

func (c config) clone() config {
	c.headers = c.headers.Clone()
	c.retry = c.retry.clone()
	return c
}

// WithAPIKey sets the bearer token sent as Authorization: Bearer <key>.
// The default is the TYPESAFE_API_KEY environment variable, the same variable
// the official SDKs read; see [API_KEY_ENV]. Blank and whitespace-only values
// are ignored.
//
// A missing or invalid key is a 401, returned as [ErrUnauthorized]; see [errors].
//
// [API_KEY_ENV]: https://docs.typesafe.ai/sdk/python/api/constants#typesafe_sdk.constants.API_KEY_ENV
// [errors]: https://docs.typesafe.ai/api#errors
func WithAPIKey(key string) Option {
	return func(cfg *config) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil
		}
		if strings.ContainsAny(key, " \r\n") {
			return fmt.Errorf("%w: API key contains whitespace", ErrConfig)
		}
		cfg.apiKey = key
		return nil
	}
}

// WithBaseURL sets the API root, for a proxy or a mock.
// It must be an http or https URL without credentials, a query, or a fragment.
// The default is TYPESAFE_BASE_URL, then https://api.typesafe.ai, the host of
// the [evaluation endpoint]. See [BASE_URL_ENV].
//
// [evaluation endpoint]: https://docs.typesafe.ai/api#evaluation-endpoint
// [BASE_URL_ENV]: https://docs.typesafe.ai/sdk/python/api/constants#typesafe_sdk.constants.BASE_URL_ENV
func WithBaseURL(baseURL string) Option {
	return func(cfg *config) error {
		baseURL = strings.TrimSpace(baseURL)
		if baseURL == "" {
			return nil
		}
		cfg.baseURL = baseURL
		return nil
	}
}

// WithModel sets the model used when [Request.Model] is empty.
// The default is TYPESAFE_DEFAULT_MODEL, then jev-latest; see [DEFAULT_MODEL_ENV].
//
// jev-latest is an alias and it moves with each release. Pin a version such as
// jev-1.13.0 when two runs must hit the same model, for example after tuning
// confidence thresholds. See [aliases] and [current models].
//
// [DEFAULT_MODEL_ENV]: https://docs.typesafe.ai/sdk/python/api/constants#typesafe_sdk.constants.DEFAULT_MODEL_ENV
// [aliases]: https://docs.typesafe.ai/models#aliases
// [current models]: https://docs.typesafe.ai/models#current-models
func WithModel(model string) Option {
	return func(cfg *config) error {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil
		}
		cfg.model = model
		return nil
	}
}

// WithTimeout sets the limit for one HTTP attempt, including reading the body.
// The default is 10 seconds, the same as the official SDKs; see [timeout].
// There is no budget across retries unless [RetryPolicy.MaxElapsed] is set.
// The context you pass to the call also applies.
//
// [timeout]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/TypeSafeClientConfig#timeout
func WithTimeout(timeout time.Duration) Option {
	return func(cfg *config) error {
		if timeout <= 0 {
			return fmt.Errorf("%w: timeout must be positive, got %s", ErrConfig, timeout)
		}
		cfg.timeout = timeout
		return nil
	}
}

// WithRetry replaces the retry policy.
// Copy [DefaultRetryPolicy] and edit it. The zero [RetryPolicy] retries nothing.
//
// The API asks clients to retry 429 and 529 with exponential backoff; see
// [handling rate limits]. The default policy does that, and [WithRateLimit]
// keeps the client under the limit in the first place.
//
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
func WithRetry(policy RetryPolicy) Option {
	return func(cfg *config) error {
		if err := policy.validate(); err != nil {
			return err
		}
		cfg.retry = policy.clone()
		cfg.retrySet = true
		return nil
	}
}

// WithHTTPClient uses client for requests. Per-attempt deadlines are set on
// the request context, so a Timeout on client is a second, shorter limit.
// Nil is ignored. The client is not closed.
//
// This is the place for a proxy, a custom TLS setup, or an in-process
// transport in tests. It plays the role of [fetch] in the JavaScript SDK.
//
// [fetch]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/TypeSafeClientConfig#fetch
func WithHTTPClient(client *http.Client) Option {
	return func(cfg *config) error {
		if client == nil {
			return nil
		}
		cfg.httpClient = client
		return nil
	}
}

// WithHeader sets a header sent on every request, like [defaultHeaders] in
// the JavaScript SDK. Authorization, Accept, Content-Type, User-Agent, and
// the X-TypeSafe-* headers are reserved.
//
// [defaultHeaders]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/TypeSafeClientConfig#defaultheaders
func WithHeader(key, value string) Option {
	return func(cfg *config) error {
		if err := checkHeader(key); err != nil {
			return err
		}
		if cfg.headers == nil {
			cfg.headers = make(http.Header)
		}
		cfg.headers.Set(key, value)
		return nil
	}
}

// WithLogger sets the logger. Info records one line per attempt.
// Debug adds headers and bodies. Authorization and cookie headers are redacted.
// Bodies are logged as sent, which includes your state.
// Nil is ignored. Without a logger, TYPESAFE_LOG_LEVEL selects one on stderr,
// and an unset or "off" level logs nothing. The levels and the redaction rule
// follow the official SDKs; see [logLevel] and [LOG_LEVEL_ENV].
//
// [logLevel]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/TypeSafeClientConfig#loglevel
// [LOG_LEVEL_ENV]: https://docs.typesafe.ai/sdk/python/api/constants#typesafe_sdk.constants.LOG_LEVEL_ENV
func WithLogger(logger *slog.Logger) Option {
	return func(cfg *config) error {
		if logger == nil {
			return nil
		}
		cfg.logger = logger
		cfg.loggerSet = true
		return nil
	}
}

func (cfg *config) resolve() error {
	if cfg.apiKey == "" {
		cfg.apiKey = strings.TrimSpace(os.Getenv(envAPIKey))
	}
	if cfg.apiKey == "" {
		return fmt.Errorf("%w: set WithAPIKey or %s", ErrConfig, envAPIKey)
	}
	if strings.ContainsAny(cfg.apiKey, " \r\n") {
		return fmt.Errorf("%w: API key contains whitespace", ErrConfig)
	}

	if cfg.baseURL == "" {
		cfg.baseURL = strings.TrimSpace(os.Getenv(envBaseURL))
	}
	if cfg.baseURL == "" {
		cfg.baseURL = defaultBaseURL
	}
	parsed, err := parseBaseURL(cfg.baseURL)
	if err != nil {
		return err
	}
	cfg.baseURL = parsed

	if cfg.model == "" {
		cfg.model = strings.TrimSpace(os.Getenv(envModel))
	}
	if cfg.model == "" {
		cfg.model = defaultModel
	}

	if cfg.timeout == 0 {
		cfg.timeout = defaultTimeout
	}
	if cfg.timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrConfig)
	}

	if !cfg.retrySet {
		cfg.retry = DefaultRetryPolicy()
	}
	if err := cfg.retry.validate(); err != nil {
		return err
	}

	if cfg.httpClient == nil {
		cfg.httpClient = &http.Client{
			CheckRedirect: refuseRedirect,
		}
	}
	if !cfg.loggerSet {
		logger, err := loggerFromEnv()
		if err != nil {
			return err
		}
		cfg.logger = logger
	}
	if !cfg.limiterSet {
		cfg.limiter = NewLimiter(DefaultRateLimit())
	}
	return nil
}

func parseBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%w: base URL must be an http or https URL, got %q", ErrConfig, raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: base URL must not include credentials", ErrConfig)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: base URL must not include a query or fragment", ErrConfig)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func loggerFromEnv() (*slog.Logger, error) {
	raw := strings.TrimSpace(os.Getenv(envLogLevel))
	if raw == "" || strings.EqualFold(raw, "off") {
		return nil, nil
	}
	var level slog.Level
	switch strings.ToLower(raw) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("%w: %s must be debug, info, warn, error, or off", ErrConfig, envLogLevel)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})), nil
}

func checkHeader(key string) error {
	canonical := http.CanonicalHeaderKey(key)
	switch canonical {
	case "Authorization", "Accept", "Content-Type", "User-Agent",
		"X-Typesafe-Sdk", "X-Typesafe-Runtime", "X-Typesafe-Retry-Count":
		return fmt.Errorf("%w: header %s is reserved", ErrConfig, canonical)
	default:
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: header name is empty", ErrConfig)
		}
		return nil
	}
}
