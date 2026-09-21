package jev

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxErrorBody = 200

// Configuration and request sentinels.
var (
	// ErrConfig is wrapped by errors from [New] and from invalid options.
	ErrConfig = errors.New("jev: invalid configuration")
	// ErrRequest is wrapped by errors for calls rejected before any HTTP attempt.
	ErrRequest = errors.New("jev: invalid request")
	// ErrResponse is wrapped when a 2xx body does not match the System One contract.
	ErrResponse = errors.New("jev: invalid response")
	// ErrBodyTooLarge is wrapped when a response body exceeds 16 MiB.
	// It is not retried.
	ErrBodyTooLarge = errors.New("jev: response body exceeds 16 MiB")
)

// Status sentinels matched by [*APIError] through errors.Is.
// A 529 matches both [ErrOverloaded] and [ErrServer].
// The statuses the API documents, and what each one means, are under [errors].
// 429 and 529 ask for backoff, which the default [RetryPolicy] applies; see
// [handling rate limits].
//
// [errors]: https://docs.typesafe.ai/api#errors
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
var (
	ErrBadRequest    = errors.New("jev: bad request")       // HTTP 400
	ErrUnauthorized  = errors.New("jev: unauthorized")      // HTTP 401
	ErrForbidden     = errors.New("jev: forbidden")         // HTTP 403
	ErrNotFound      = errors.New("jev: not found")         // HTTP 404
	ErrUnprocessable = errors.New("jev: unprocessable")     // HTTP 422
	ErrRateLimited   = errors.New("jev: rate limited")      // HTTP 429
	ErrOverloaded    = errors.New("jev: overloaded")        // HTTP 529
	ErrServer        = errors.New("jev: server error")      // HTTP 5xx, including 529
	ErrStatus        = errors.New("jev: unexpected status") // any other non-2xx
)

// Transport sentinels matched by [*ConnectionError] through errors.Is.
var (
	// ErrConnection matches every ConnectionError.
	ErrConnection = errors.New("jev: connection error")
	// ErrTimeout matches a ConnectionError caused by the per-attempt timeout.
	// A deadline on the caller's context is returned as that context error
	// and does not match ErrTimeout.
	ErrTimeout = errors.New("jev: request timed out")
)

// APIError is an unsuccessful HTTP response, returned after retries.
// Status is the HTTP status; the API's table of statuses is under [errors].
// Message is pulled from the JSON body, including the field path for a 422.
// RequestID is the server's X-Typesafe-Request-Id, for support tickets.
//
// [errors]: https://docs.typesafe.ai/api#errors
type APIError struct {
	Status    int
	Method    string
	URL       string
	Header    http.Header
	Body      []byte
	Message   string
	RequestID string
	Attempts  int
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "jev: %s %s: %d", e.Method, e.URL, e.Status)
	if e.Message != "" {
		b.WriteByte(' ')
		b.WriteString(e.Message)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " (request_id=%s)", e.RequestID)
	}
	return b.String()
}

// Is reports whether target is the status sentinel for this response.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrBadRequest:
		return e.Status == http.StatusBadRequest
	case ErrUnauthorized:
		return e.Status == http.StatusUnauthorized
	case ErrForbidden:
		return e.Status == http.StatusForbidden
	case ErrNotFound:
		return e.Status == http.StatusNotFound
	case ErrUnprocessable:
		return e.Status == http.StatusUnprocessableEntity
	case ErrRateLimited:
		return e.Status == http.StatusTooManyRequests
	case ErrOverloaded:
		return e.Status == 529
	case ErrServer:
		return e.Status >= 500
	case ErrStatus:
		return e.Status < 500 && !knownStatus(e.Status)
	default:
		return false
	}
}

func knownStatus(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// RetryAfter returns the delay from Retry-After-Ms or Retry-After.
// A 429 or 529 may carry one; see [handling rate limits]. The client already
// waited it out when it retried, and a [Limiter] from [NewLimiter] pauses
// every caller for it, so read it here for logging or to plan the next call.
//
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
func (e *APIError) RetryAfter() (time.Duration, bool) {
	return parseRetryAfter(e.Header, time.Now())
}

// ConnectionError is a request that failed without a usable HTTP response,
// returned after retries. It is the Go form of the official SDKs'
// [connection errors]: APIConnectionError, and APITimeoutError when Timeout
// is set.
//
// [connection errors]: https://docs.typesafe.ai/sdk/python/api/exceptions#connection-errors
type ConnectionError struct {
	Method    string
	URL       string
	Timeout   time.Duration
	Attempts  int
	RequestID string
	Err       error
}

func (e *ConnectionError) Error() string {
	if e.Timeout > 0 {
		return fmt.Sprintf("jev: %s %s: timed out after %s", e.Method, e.URL, e.Timeout)
	}
	if e.Err != nil {
		return fmt.Sprintf("jev: %s %s: %s", e.Method, e.URL, e.Err.Error())
	}
	return fmt.Sprintf("jev: %s %s: connection error", e.Method, e.URL)
}

// Unwrap exposes the transport error. Attempt timeouts do not unwrap to
// context.DeadlineExceeded, so that sentinel stays reserved for the caller's
// own deadline.
func (e *ConnectionError) Unwrap() error { return e.Err }

// Is reports whether target is [ErrConnection] or, for a timed-out attempt, [ErrTimeout].
func (e *ConnectionError) Is(target error) bool {
	if target == ErrConnection {
		return true
	}
	return target == ErrTimeout && e.Timeout > 0
}

// ResponseError is a 2xx body that failed validation, or a body over the size
// limit. Field is the JSON path that failed, such as answers.team.choice.
// The contract it checks is the [response body] and its [answer types]; the
// official SDKs raise the same thing as [response validation] errors.
//
// [response body]: https://docs.typesafe.ai/api#response-body
// [answer types]: https://docs.typesafe.ai/api#answer-types
// [response validation]: https://docs.typesafe.ai/sdk/python/api/exceptions#response-validation
type ResponseError struct {
	Method    string
	URL       string
	RequestID string
	Field     string
	Body      []byte
	Err       error
}

func (e *ResponseError) Error() string {
	msg := "invalid response"
	if e.Field != "" {
		msg = "invalid response at " + e.Field
	}
	if detail := responseDetail(e.Err); detail != "" {
		msg += ": " + detail
	}
	if e.RequestID != "" {
		msg += " (request_id=" + e.RequestID + ")"
	}
	if e.Method == "" {
		return "jev: " + msg
	}
	return fmt.Sprintf("jev: %s %s: %s", e.Method, e.URL, msg)
}

func responseDetail(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	text = strings.TrimPrefix(text, ErrResponse.Error()+": ")
	text = strings.TrimPrefix(text, ErrBodyTooLarge.Error()+": ")
	if text == ErrResponse.Error() || text == ErrBodyTooLarge.Error() {
		return ""
	}
	return text
}

func (e *ResponseError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrResponse
}

func (e *ResponseError) Is(target error) bool {
	return target == ErrResponse || (target == ErrBodyTooLarge && errors.Is(e.Err, ErrBodyTooLarge))
}

func extractMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload any
	if err := unmarshal(body, &payload); err != nil {
		return truncate(strings.TrimSpace(string(body)))
	}
	switch v := payload.(type) {
	case string:
		return truncate(v)
	case map[string]any:
		if s, ok := stringField(v, "error"); ok {
			return truncate(s)
		}
		if m, ok := v["error"].(map[string]any); ok {
			if s, ok := stringField(m, "message"); ok {
				return truncate(s)
			}
		}
		if s, ok := stringField(v, "message"); ok {
			return truncate(s)
		}
		if s, ok := stringField(v, "detail"); ok {
			return truncate(s)
		}
		if m, ok := v["detail"].(map[string]any); ok {
			if s, ok := stringField(m, "message"); ok {
				return truncate(s)
			}
		}
		if list, ok := v["detail"].([]any); ok {
			if s := validationErrors(list); s != "" {
				return truncate(s)
			}
		}
	}
	return truncate(strings.TrimSpace(string(body)))
}

func stringField(m map[string]any, key string) (string, bool) {
	s, ok := m[key].(string)
	return s, ok && s != ""
}

func validationErrors(list []any) string {
	parts := make([]string, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := m["msg"].(string)
		if !ok || msg == "" {
			continue
		}
		loc, _ := m["loc"].([]any)
		var path []string
		for _, part := range loc {
			if s, ok := part.(string); ok && s != "body" {
				path = append(path, s)
			} else if n, ok := numberString(part); ok {
				path = append(path, n)
			}
		}
		if len(path) == 0 {
			parts = append(parts, msg)
			continue
		}
		parts = append(parts, strings.Join(path, ".")+": "+msg)
	}
	return strings.Join(parts, "; ")
}

func numberString(v any) (string, bool) {
	switch n := v.(type) {
	case float64:
		if math.Trunc(n) == n {
			return strconv.FormatInt(int64(n), 10), true
		}
		return strconv.FormatFloat(n, 'f', -1, 64), true
	default:
		return "", false
	}
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxErrorBody {
		return s
	}
	cut := 0
	n := 0
	for i := range s {
		if n == maxErrorBody {
			cut = i
			break
		}
		n++
	}
	if cut == 0 {
		return s
	}
	return s[:cut] + "..."
}
