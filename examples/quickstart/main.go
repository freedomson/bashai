// Command quickstart calls System One with one of each question type.
//
//	TYPESAFE_API_KEY=... go run .
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kataras/jev"
)

func main() {
	client, err := jev.New(jev.WithTimeout(20 * time.Second))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	billing, _ := resp.Noul("billing")
	team, _ := resp.Choice("team")
	urgency, _ := resp.Score("urgency")
	fmt.Printf("model=%s billing=%.3f team=%s urgency=%.2f\n", resp.Model, billing.Noul, team.Choice, urgency.Score)
}
