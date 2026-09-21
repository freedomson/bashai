package jev_test

import (
	"context"
	"fmt"

	"github.com/kataras/jev"
)

func Example() {
	client, err := jev.New()
	if err != nil {
		fmt.Println(err)
		return
	}
	resp, err := client.SystemOne(context.Background(), jev.Request{
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
		fmt.Println(err)
		return
	}
	if billing, ok := resp.Noul("billing"); ok {
		fmt.Println(billing.Noul > 0.5)
	}
}

// The API limits are per account. Two clients on the same key share one
// limiter, so together they stay under the limit.
func ExampleWithRateLimiter() {
	shared := jev.NewLimiter(jev.DefaultRateLimit())

	latest, err := jev.New(jev.WithRateLimiter(shared))
	if err != nil {
		fmt.Println(err)
		return
	}
	pinned, err := jev.New(jev.WithModel("jev-1.13.0"), jev.WithRateLimiter(latest.Limiter()))
	if err != nil {
		fmt.Println(err)
		return
	}
	_, _ = latest, pinned
}

// A plan with other limits, or one process out of several on the same key.
func ExampleWithRateLimit() {
	client, err := jev.New(jev.WithRateLimit(jev.RateLimit{
		RequestsPerMinute: 300,
		TokensPerSecond:   60_000,
	}))
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = client
}
