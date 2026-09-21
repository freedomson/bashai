# jev

[![build status](https://img.shields.io/github/actions/workflow/status/kataras/jev/ci.yml?style=for-the-badge)](https://github.com/kataras/jev/actions) [![report card](https://img.shields.io/badge/report%20card-a%2B-ff3333.svg?style=for-the-badge)](https://goreportcard.com/report/github.com/kataras/jev) [![godocs](https://img.shields.io/badge/go-%20docs-488AC7.svg?style=for-the-badge)](https://pkg.go.dev/github.com/kataras/jev)

A Go client for the [TypeSafe](https://typesafe.ai) System One API and its model, Jev.

Jev does not write text. You send a state (a string, or JSON such as a ticket, a log, or the current state of a program) and one or more named questions. The call returns a typed answer and a probability for every question. Adding questions to the same call does not add a round trip.

This module is not affiliated with TypeSafe AI. The official clients are the [JavaScript SDK](https://github.com/typesafe-ai/typesafe-sdk-js) and the [Python SDK](https://github.com/typesafe-ai/typesafe-sdk-python). The HTTP contract is the [System One API](https://docs.typesafe.ai/api).

## Install

Go 1.27 or newer. The only dependency is `golang.org/x/time/rate`.

```sh
go get github.com/kataras/jev
```

Please star this open source project to attract more developers so that together we can improve it even more!

## Questions

| Question | You provide | You get back |
| --- | --- | --- |
| Noul | A yes/no question | Probability of yes, from 0 to 1 |
| Choice | Named options, up to 255 | The picked option, a probability for each option, and a confidence |
| Score | An ordered list of levels, up to 10 | A position on that list (it can fall between levels), a probability for each level, and a confidence |

Question names are yours. They come back as the answer keys. They are not part of the prompt.

`jev-latest` is the default model, and that alias moves. Set `WithModel("jev-1.13.0")` when a later run has to hit the same model.

## Usage

```go
client, err := jev.New() // reads TYPESAFE_API_KEY
resp, err := client.SystemOne(ctx, jev.Request{
    State: "I was charged twice. Please fix this ASAP.",
    Questions: jev.Questions{
        "billing": jev.Noul{Instructions: "Is this ticket about billing?"},
        "team": jev.Choice{
            Instructions: "Which team should handle this?",
            Criteria: map[string]any{
                "billing":   "Payments, invoices, refunds",
                "technical": nil, // null means the label has no description
            },
        },
        "urgency": jev.Score{
            Instructions: "How urgent is this ticket?",
            Criteria:     []string{"can wait", "this week", "today"},
        },
    },
})

billing, _ := resp.Noul("billing")
team, _ := resp.Choice("team")
urgency, _ := resp.Score("urgency")
```

`SystemOneAs` decodes the same JSON into a struct. It is a generic method, which is why this module requires Go 1.27. JSON names are case-sensitive.

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

A full program lives in [examples/quickstart](examples/quickstart/main.go).

## One question

`Noul`, `Choice`, and `Score` each send one question named `answer` through `SystemOne`. `Classify` is `Choice` with the question and the options as separate arguments. `Rate` is `Score` with the levels as a list. The `As` methods call `SystemOneAs`. In that JSON the question name is `answer`.

```go
yes, err := client.Noul(ctx, ticket, "Is this about billing?")

team, err := client.Classify(ctx, ticket, "Which team?", map[string]any{
    "billing":   "Payments, invoices, refunds",
    "technical": nil,
})

urgency, err := client.Rate(ctx, ticket, "How urgent?", []string{"can wait", "this week", "today"})
```

Several questions about the same state still belong in one `SystemOne` call.

List the models on the account with `ListModels`. The list includes the `jev-latest` alias.

```go
models, err := client.ListModels(ctx)
```

## Configuration

Explicit options win over environment variables. A blank environment value is ignored.

| Option | Environment | Default |
| --- | --- | --- |
| `WithAPIKey` | `TYPESAFE_API_KEY` | required |
| `WithBaseURL` | `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` |
| `WithModel` | `TYPESAFE_DEFAULT_MODEL` | `jev-latest` |
| `WithTimeout` | | 10s per attempt |
| `WithRetry` | | 2 retries, 500ms to 5s backoff |
| `WithRateLimit` | | 1,200 requests/min, 250,000 tokens/s |
| `WithRateLimiter` | | the limiter `WithRateLimit` builds |
| `WithLogger` | `TYPESAFE_LOG_LEVEL` | no logging |
| `WithHTTPClient` | | a client that does not follow redirects |
| `WithHeader` | | |

The same options can be passed to one call. They override the client for that call only. `WithRateLimit` is the exception: it belongs to `New`, because a limiter built for one call starts with a full budget and paces nothing. For one call, pass `WithRateLimiter` with a limiter you own, or `nil` to skip pacing.

`WithRetry` replaces the whole policy. Copy `DefaultRetryPolicy()` and edit the copy. A zero `RetryPolicy` retries nothing, because its status list is empty.

## Rate limits

The API limits each account to [1,200 requests per minute and 250,000 input tokens per second](https://docs.typesafe.ai/models#current-models). A request over either limit is a 429. Retrying after a 429 works, but it costs a round trip and a backoff wait each time. The client stays under the limit instead: every attempt, retries included, first waits on a token bucket built with [golang.org/x/time/rate](https://pkg.go.dev/golang.org/x/time/rate), the same way [kataras/httpclient](https://github.com/kataras/httpclient) paces its requests.

Requests are spread across the minute, with a burst of one second's worth (20 at the default). Tokens are estimated from the body size before the call and settled with `usage.input_tokens` after it, so the estimate corrects itself. When the server does answer 429 or 529 with `Retry-After`, every caller on that limiter pauses until then, not only the call that saw it. The pause is capped by `RetryPolicy.MaxRetryAfter`, 60s by default.

The limits are per account, so clients on the same key should share one limiter:

```go
shared := jev.NewLimiter(jev.DefaultRateLimit())
latest, err := jev.New(jev.WithRateLimiter(shared))
pinned, err := jev.New(jev.WithModel("jev-1.13.0"), jev.WithRateLimiter(shared))
// or join an existing client's budget:
third, err := jev.New(jev.WithRateLimiter(latest.Limiter()))
```

TypeSafe adjusts the limits without notice, and enterprise plans get higher ones. Set your own figures, or divide the account's budget between processes:

```go
client, err := jev.New(jev.WithRateLimit(jev.RateLimit{
    RequestsPerMinute: 300,
    TokensPerSecond:   60_000,
}))
```

A zero field disables that axis. `jev.WithRateLimit(jev.RateLimit{})` or `jev.WithRateLimiter(nil)` turns pacing off. A plain `*rate.Limiter` also satisfies `jev.Limiter`; it paces requests only.

The limiter waits are not retries. A wait that outlives your context returns that context's error, and no request is sent.

## Errors

```go
if errors.Is(err, context.Canceled) {
    // the caller cancelled; this is not retried
}
if errors.Is(err, jev.ErrUnauthorized) {
    // HTTP 401
}
if errors.Is(err, jev.ErrRateLimited) {
    // HTTP 429, after retries
}
if errors.Is(err, jev.ErrOverloaded) {
    // HTTP 529; this also matches jev.ErrServer
}
var api *jev.APIError
if errors.As(err, &api) {
    fmt.Println(api.Status, api.RequestID, api.Message)
}
```

| Sentinel | When |
| --- | --- |
| `ErrConfig` | Missing key, bad URL, bad option |
| `ErrRequest` | Rejected locally, before HTTP |
| `ErrResponse` | 2xx body that does not match the contract |
| `ErrTimeout` | The per-attempt timeout fired |
| `ErrConnection` | DNS, TLS, or a dropped connection |
| `ErrBadRequest` | HTTP 400 |
| `ErrUnauthorized` | HTTP 401 |
| `ErrForbidden` | HTTP 403 |
| `ErrNotFound` | HTTP 404 |
| `ErrUnprocessable` | HTTP 422 |
| `ErrRateLimited` | HTTP 429 |
| `ErrOverloaded` | HTTP 529 |
| `ErrServer` | HTTP 5xx, including 529 |

A deadline on the context you passed is returned as that context error. It does not match `ErrTimeout`. `ErrTimeout` means this client's own per-attempt limit.

## Retries

The default policy matches the official JavaScript SDK. It retries HTTP 408, 429, and 500 through 599, including 529. The first wait is 500ms, doubled up to 5s, with up to 25% of the wait subtracted at random. `Retry-After-Ms` is preferred over `Retry-After`. A server delay longer than 60s falls back to that backoff. Retries are the second line; the [rate limiter](#rate-limits) is the first.

There is no budget across attempts unless you set `RetryPolicy.MaxElapsed`. The context you pass always applies. Cancellation during a wait stops the call and is not retried.

POST is retried because a System One call only evaluates. Each attempt still spends input tokens.

## Logging

`TYPESAFE_LOG_LEVEL` may be `debug`, `info`, `warn`, `error`, or `off`. Info logs one line per attempt. Debug also logs headers and bodies. `Authorization`, `Cookie`, and `X-Api-Key` are replaced with `[redacted]`. Bodies are logged as sent, and they contain your state.

## Wire format

A few choices differ from other Go clients, on purpose:

- JSON is encoded with `encoding/json/v2`, and `<`, `>`, and `&` are left as themselves. `encoding/json` rewrites them to `\u003c`, `\u003e`, and `\u0026`, so the text the model reads is not the text you passed.
- Object keys are sorted. The same request produces the same bytes.
- `Request.Extra` cannot replace `state`, `model`, or `questions`.
- A `[]byte` state is sent as a JSON string. A plain byte slice would otherwise be base64.
- Score criteria must be a JSON array. A map is rejected. That matches the official SDK change in v0.6.0 (15 Sep 2026).
- Choice criteria must be a JSON object, with at most 255 options.

## Live tests

`TestLiveSystemOne` and `TestLiveModels` call the real API and spend tokens. `go test` skips them when `TYPESAFE_API_KEY` is unset or blank, and always under `go test -short`. Every other test runs against an in-process server and needs no key.

For GitHub Actions, add a repository secret named `TYPESAFE_API_KEY`. The workflow runs the unit tests without the key, then runs `-run '^TestLive'` only when the secret is present. Pull requests from forks do not receive secrets, so there the live step prints a note and passes. Do not commit a key.

## Agent skill

[skills/jev/SKILL.md](skills/jev/SKILL.md) is an Agent Skill for this module. The signatures in it match the Go API above. Claude Code, Cursor, Codex, and Plexon all read that format.

Install it for every agent this machine already has, including Plexon (it adopts `~/.agents/skills`):

```sh
npx skills add kataras/jev --skill jev -g -y
```

One project only: drop `-g`. The files land in `.agents/skills/jev` or `.claude/skills/jev`. Plexon reads both.

Pick agents by name when you do not want the full set:

```sh
npx skills add kataras/jev --skill jev -g -y -a claude-code -a cursor -a codex
```

Claude Code can install the same skill as a plugin. Inside a Claude Code session:

```text
/plugin marketplace add kataras/jev
/plugin install jev@kataras-jev
```

## License

[MIT](LICENSE).
