---
name: jev
description: >
  Use when writing Go that calls the TypeSafe System One API through
  github.com/kataras/jev. Trigger on Jev, TypeSafe, System One, Noul, jev.New,
  SystemOne, or a fast classify, route, or score step beside an LLM.
---

# Jev Go client

Call `github.com/kataras/jev`. Do not hand-roll HTTP for this API, and do not use `github.com/kataras/httpclient` for it. This module already speaks the System One contract, including retries and the JSON wire format.

Jev returns a probability for each question you name. It does not write text. Keep generation in an LLM. Use Jev for a closed decision: yes or no, one label from a set, or a position on a rubric you define.

## When to use

- Classify, route, score, or check a piece of text or JSON.
- Several questions share one state. Put them in one `SystemOne` call.
- A later step should branch on a probability or a confidence value.

## When not to use

- The output is prose, code, or an open-ended answer.
- The decision is ordinary Go (`if status == 404`).
- You need a chat history. Send the current state as text or as a JSON value.

## Setup

Go 1.27 or newer.

```sh
go get github.com/kataras/jev
```

The API key comes from the environment. `jev.New` reads `TYPESAFE_API_KEY`. Never write the key into source, tests, or logs.

```go
client, err := jev.New()
```

`jev.New` is the constructor. There is no `NewClient`.

Optional client settings, in this package:

| Option | Environment | Default |
| --- | --- | --- |
| `WithAPIKey` | `TYPESAFE_API_KEY` | required |
| `WithBaseURL` | `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` |
| `WithModel` | `TYPESAFE_DEFAULT_MODEL` | `jev-latest` |
| `WithTimeout` | | 10s per attempt |
| `WithRetry` | | `DefaultRetryPolicy()` |
| `WithRateLimit` | | `DefaultRateLimit()`: 1,200 req/min, 250,000 tokens/s |
| `WithRateLimiter` | | the limiter `WithRateLimit` builds |
| `WithLogger` | `TYPESAFE_LOG_LEVEL` | no logging |
| `WithHTTPClient` | | no redirects |
| `WithHeader` | | |

A blank string does not clear a setting. The same options may be passed to one call and apply only to that call, except `WithRateLimit`, which is refused per call with `ErrConfig`. Per call, pass `WithRateLimiter(nil)` to skip pacing or `WithRateLimiter(l)` with a limiter you own.

`jev-latest` is an alias and it moves. Pass `WithModel("jev-1.13.0")` or set `Request.Model` when two runs must hit the same model. `ListModels` returns the names on the account.

## One call

```go
resp, err := client.SystemOne(ctx, jev.Request{
    State: "I was charged twice. Please fix this ASAP.",
    Questions: jev.Questions{
        "billing": jev.Noul{Instructions: "Is this ticket about billing?"},
        "team": jev.Choice{
            Instructions: "Which team should handle this?",
            Criteria: map[string]any{
                "billing":   "Payments, invoices, refunds",
                "technical": nil,
            },
        },
        "urgency": jev.Score{
            Instructions: "How urgent is this ticket?",
            Criteria:     []string{"can wait", "this week", "today"},
        },
    },
})
if err != nil {
    return err
}

billing, ok := resp.Noul("billing")
team, ok := resp.Choice("team")
urgency, ok := resp.Score("urgency")
```

Rules for that shape:

- `State` is a string, a map, a slice, or a struct with json tags. A `[]byte` is sent as text. Nil is JSON null.
- `Questions` is a `map[string]Question`. Empty is `ErrRequest`. Names come back as answer keys. They are not part of the prompt.
- `Noul` is yes or no. The answer field is `Noul` (`float64`, 0 to 1), not a bool.
- `Choice.Criteria` is a JSON object: `map[string]any`, `map[string]string`, or a struct with json tags. A nil description is sent as null. At least one option, at most 255. A list is rejected.
- `Score.Criteria` is an ordered list, lowest level first: `[]string` or `[]any`. At least one level, at most 10. A map is rejected.
- `Instructions` may be a string, or a value that encodes as a JSON object or array. Nil omits the field.
- `Noul.Criteria` is an optional `*NoulCriteria` with `True` and `False`. Nil omits it.
- `Request.Model` overrides the client model for that call.
- `Request.Extra` adds unknown top-level JSON fields. Keys `state`, `model`, and `questions` are rejected.
- `Raw` (`map[string]any`) is for a question kind this version does not model. It needs a non-empty string `type`.

Read every answer with the typed getter. `ok == false` means that name is missing or is a different kind.

```go
if team, ok := resp.Choice("team"); ok && team.Confidence >= 0.8 {
    route(team.Choice)
}
```

`ChoiceAnswer` has `Choice`, `Confidence`, and `Probabilities`. `ScoreAnswer` has `Score` (it may sit between levels), `Confidence`, `Legend`, and `Probabilities`. Legend and probability keys are `"0"`, `"1"`, and so on. `Usage.InputTokens` and `Usage.OutputTokens` are on the response. `RequestID` is the server's `X-Typesafe-Request-Id`.

`SystemOneAs[T]` decodes the response JSON into your struct. JSON names are case-sensitive. Unknown members are ignored.

```go
type ticket struct {
    Model string `json:"model"`
    Answers struct {
        Billing struct {
            Noul float64 `json:"noul"`
        } `json:"billing"`
    } `json:"answers"`
}

out, err := client.SystemOneAs[ticket](ctx, req)
```

`resp.As[ticket]()` does the same decode after you already hold a `*Response`.

## One question

`Noul`, `Choice`, and `Score` each send one question named `answer` (`jev.AnswerKey`) through `SystemOne`. `Classify` is `Choice` with the question and the options as separate arguments. `Rate` is `Score` with the levels as a list. The `As` methods (`NoulAs`, `ChoiceAs`, `ScoreAs`, `ClassifyAs`, `RateAs`) call `SystemOneAs`. In that JSON the question name is `answer`.

```go
yes, err := client.Noul(ctx, ticket, "Is this about billing?")

team, err := client.Classify(ctx, ticket, "Which team?", map[string]any{
    "billing":   "Payments, invoices, refunds",
    "technical": nil,
})

urgency, err := client.Rate(ctx, ticket, "How urgent?", []string{"can wait", "this week", "today"})
```

`Noul` also accepts a `jev.Noul` or `*jev.Noul` when the question needs true and false criteria. `Choice` and `Score` take the structs. Several questions about the same state still belong in one `SystemOne` call.

## Errors

Use `errors.Is` and `errors.As`. Do not match on error strings.

| Sentinel | Meaning |
| --- | --- |
| `ErrConfig` | Bad option, or no API key |
| `ErrRequest` | Rejected before HTTP, including a nil context |
| `ErrResponse` | 2xx body failed the contract. `*ResponseError` has `Field` |
| `ErrBodyTooLarge` | Body over 16 MiB. Not retried |
| `ErrTimeout` | This client's per-attempt limit. Not the caller's context deadline |
| `ErrConnection` | DNS, TLS, or a dropped connection |
| `ErrBadRequest` | 400 |
| `ErrUnauthorized` | 401 |
| `ErrForbidden` | 403 |
| `ErrNotFound` | 404 |
| `ErrUnprocessable` | 422 |
| `ErrRateLimited` | 429, after retries. `*APIError` has `RetryAfter` |
| `ErrOverloaded` | 529 |
| `ErrServer` | every 5xx, including 529 |
| `context.Canceled` | Caller cancelled. Not retried |

A 529 matches both `ErrOverloaded` and `ErrServer`. Check `ErrOverloaded` first when the overload case matters.

## Retries

The default policy retries 408, 429, and 500–599, twice, after the first attempt. Backoff starts at 500ms, doubles up to 5s, and subtracts up to 25% of the wait. `Retry-After-Ms` wins over `Retry-After`. A server delay longer than 60s falls back to backoff. It is not capped and then waited out.

There is no budget across attempts unless `RetryPolicy.MaxElapsed` is set. Copy `DefaultRetryPolicy()` and edit the copy. A zero `RetryPolicy` retries nothing.

```go
policy := jev.DefaultRetryPolicy()
policy.MaxRetries = 0
client, err := jev.New(jev.WithRetry(policy))
```

POST is retried. Each attempt spends input tokens.

## Rate limits

The account limit is 1,200 requests per minute and 250,000 input tokens per second. The client paces itself under that by default with a `golang.org/x/time/rate` token bucket: every attempt, retries included, waits on it first. Tokens are estimated from the body and corrected with `usage.input_tokens`. A 429 or 529 with `Retry-After` pauses every caller on the limiter until then.

Clients on the same key share the account budget, so give them one limiter:

```go
shared := jev.NewLimiter(jev.DefaultRateLimit())
a, err := jev.New(jev.WithRateLimiter(shared))
b, err := jev.New(jev.WithModel("jev-1.13.0"), jev.WithRateLimiter(shared))
// or: jev.New(jev.WithRateLimiter(a.Limiter()))
```

Own figures, for a plan with other limits or a budget split across processes:

```go
client, err := jev.New(jev.WithRateLimit(jev.RateLimit{RequestsPerMinute: 300, TokensPerSecond: 60_000}))
```

A zero field disables that axis; `WithRateLimit(jev.RateLimit{})` or `WithRateLimiter(nil)` turns pacing off. Do not write a second rate limiter around this client. A limiter wait that outlives the context returns the context error and sends nothing.

## Tests

Unit tests should use `httptest` and `WithBaseURL` plus `WithAPIKey("test-key")`. Do not call the live API from those tests. Pass `WithRateLimiter(nil)` or a small `WithRateLimit` when a test sends many requests, or the default 20-per-second burst slows it down. For exact timing, use `testing/synctest` with an in-process `http.RoundTripper` through `WithHTTPClient`; `httptest.NewServer` does not work inside a synctest bubble.

Live tests skip when `TYPESAFE_API_KEY` is empty and under `go test -short`. They must not print the key.

Assert on ranges and on membership in the option set you sent. Do not assert a specific probability. `jev-latest` moves.

## Do not invent

These are not in the module. Do not write them, and do not tell the user they exist.

- `NewClient`, `Ask`, `Evaluate`, `Chat`, `Complete`
- A bool answer for Noul
- Score criteria as a map
- Choice criteria as a list
- `encoding/json` (v1) for the request body
- A hard-coded API key
- One HTTP call per question when the questions share a state
- A `time.Sleep` or a hand-written limiter around calls; use `WithRateLimit` or `WithRateLimiter`

Full signatures are in [api.md](api.md). A runnable program is `bashai` in the module repo.
