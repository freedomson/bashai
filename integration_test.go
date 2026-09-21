package jev

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// The live tests spend real tokens on a real account. They run only when
// TYPESAFE_API_KEY holds a key, which CI takes from a repository secret, and
// never under -short. A fork's pull request has no secrets, so there they skip.

// liveClient returns a client for the real API, or skips the test.
func liveClient(t *testing.T) (*Client, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("live tests are skipped under -short")
	}
	if strings.TrimSpace(os.Getenv(envAPIKey)) == "" {
		t.Skipf("%s is not set; the live tests need a real key", envAPIKey)
	}
	client, err := New(WithTimeout(20 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	t.Cleanup(client.CloseIdleConnections)
	return client, ctx
}

func TestLiveSystemOne(t *testing.T) {
	client, ctx := liveClient(t)

	resp, err := client.SystemOne(ctx, Request{
		State: "Hi, I've been trying to connect my Stripe account for 3 days and the integration keeps failing. I'm losing sales. Please help ASAP.",
		Questions: Questions{
			"department": Choice{
				Instructions: "Which team should handle this",
				Criteria: map[string]any{
					"billing":   "Payment or subscription issues",
					"technical": "Bugs or integration problems",
					"sales":     "Pricing or account questions",
				},
			},
			"frustration": Score{
				Instructions: "How frustrated the customer appears",
				Criteria:     []string{"Calm, just stating facts", "Frustrated but civil", "Very angry, strong language"},
			},
			"is_urgent": Noul{Instructions: "The message conveys urgency or time-sensitivity"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model == "" || resp.Usage.InputTokens <= 0 {
		t.Fatalf("model %q usage %+v", resp.Model, resp.Usage)
	}
	department, ok := resp.Choice("department")
	if !ok || (department.Choice != "billing" && department.Choice != "technical" && department.Choice != "sales") {
		t.Fatalf("department %+v", department)
	}
	if department.Confidence < 0 || department.Confidence > 1 {
		t.Fatalf("confidence %v", department.Confidence)
	}
	frustration, ok := resp.Score("frustration")
	if !ok || frustration.Score < 0 || frustration.Score > 2 {
		t.Fatalf("frustration %+v", frustration)
	}
	urgent, ok := resp.Noul("is_urgent")
	if !ok || urgent.Noul < 0 || urgent.Noul > 1 {
		t.Fatalf("urgent %+v", urgent)
	}
	t.Logf("model=%s department=%s confidence=%.3f frustration=%.3f urgent=%.3f tokens=%d",
		resp.Model, department.Choice, department.Confidence, frustration.Score, urgent.Noul, resp.Usage.InputTokens)
}

func TestLiveModels(t *testing.T) {
	client, ctx := liveClient(t)
	models, err := client.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("no models")
	}
	for _, model := range models {
		if model.Name == "" {
			t.Fatalf("empty name in %+v", models)
		}
		t.Logf("model %s released %s", model.Name, model.ReleaseDate)
	}
}
