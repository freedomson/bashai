// Package jev is an HTTP client for the TypeSafe System One API.
//
// Jev is TypeSafe's System One model. You send it a state (text or JSON) and
// one or more named questions. It answers every question in that call and
// returns a probability with each answer. It does not generate text.
// The HTTP contract is the [API reference]; the ideas behind it are under
// [System One] and [primitives].
//
// Create a client with [New]. The API key comes from [WithAPIKey] or from
// the TYPESAFE_API_KEY environment variable.
//
//	client, err := jev.New()
//	resp, err := client.SystemOne(ctx, jev.Request{
//		State: "I was charged twice. Please fix this ASAP.",
//		Questions: jev.Questions{
//			"billing": jev.Noul{Instructions: "Is this ticket about billing?"},
//			"team": jev.Choice{
//				Instructions: "Which team should handle this?",
//				Criteria: map[string]any{
//					"billing":   "Payments, invoices, refunds",
//					"technical": nil,
//				},
//			},
//			"urgency": jev.Score{
//				Instructions: "How urgent is this ticket?",
//				Criteria:     []string{"can wait", "this week", "today"},
//			},
//		},
//	})
//	if billing, ok := resp.Noul("billing"); ok {
//		fmt.Println(billing.Noul)
//	}
//
// [Client.SystemOneAs] decodes that same JSON into a struct of your own.
// It is a generic method, so it needs Go 1.27.
//
// One question at a time can use [Client.Noul], [Client.Choice], [Client.Score],
// [Client.Classify], or [Client.Rate]. Each calls SystemOne with the question
// named "answer". The As forms call SystemOneAs.
//
// # Rate limits
//
// The API limits each account to a number of requests per minute and input
// tokens per second; the current figures are under [current models]. The
// client paces itself under those figures by default, with a [Limiter] built
// from [DefaultRateLimit], so a burst of calls queues instead of failing with
// 429. When the server does answer 429 or 529 with Retry-After, every caller
// on that limiter pauses until then. Set [WithRateLimit] to change the
// figures, or share one [NewLimiter] between clients on the same key with
// [WithRateLimiter]. See [handling rate limits].
//
// # Retries and errors
//
// The client retries 408, 429, and 5xx responses, including 529, with
// exponential backoff, as the API asks. Context cancellation is not retried.
// Failed responses are an [*APIError] and match a status sentinel such as
// [ErrRateLimited] through errors.Is; see [errors]. Credential headers are
// redacted from logs. Request and response bodies are not.
//
// [API reference]: https://docs.typesafe.ai/api
// [System One]: https://docs.typesafe.ai/concepts/system-one
// [primitives]: https://docs.typesafe.ai/primitives
// [current models]: https://docs.typesafe.ai/models#current-models
// [handling rate limits]: https://docs.typesafe.ai/api#handling-rate-limits
// [errors]: https://docs.typesafe.ai/api#errors
package jev
