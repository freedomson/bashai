package jev

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShortcutsCallSystemOne(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		bodies = append(bodies, body)
		switch {
		case strings.Contains(body, `"type":"choice"`):
			_, _ = io.WriteString(w, `{
				"model":"jev-1.13.0",
				"answers":{"answer":{"type":"choice","choice":"billing","confidence":0.7,"probabilities":{"billing":0.7,"technical":0.3}}},
				"usage":{"input_tokens":5,"output_tokens":1}
			}`)
		case strings.Contains(body, `"type":"score"`):
			_, _ = io.WriteString(w, `{
				"model":"jev-1.13.0",
				"answers":{"answer":{"type":"score","score":1,"confidence":0.6,"legend":{"0":"low","1":"high"},"probabilities":{"0":0.2,"1":0.8}}},
				"usage":{"input_tokens":6,"output_tokens":1}
			}`)
		default:
			_, _ = io.WriteString(w, `{
				"model":"jev-1.13.0",
				"answers":{"answer":{"type":"noul","noul":0.8}},
				"usage":{"input_tokens":4,"output_tokens":1}
			}`)
		}
	}))
	defer srv.Close()

	client, err := New(WithAPIKey("k"), WithBaseURL(srv.URL), WithRetry(zeroRetry()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	noul, err := client.Noul(ctx, "a < b", "Is this about billing?")
	if err != nil {
		t.Fatal(err)
	}
	if noul.Noul != 0.8 {
		t.Fatalf("noul %v", noul.Noul)
	}
	if !strings.Contains(bodies[0], `"answer"`) || !strings.Contains(bodies[0], `"type":"noul"`) || strings.Contains(bodies[0], `\u003c`) {
		t.Fatalf("noul body %s", bodies[0])
	}

	choice, err := client.Classify(ctx, "ticket", "Which team?", map[string]any{
		"billing": "payments", "technical": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if choice.Choice != "billing" || choice.Confidence != 0.7 {
		t.Fatalf("%+v", choice)
	}

	scored, err := client.Rate(ctx, "ticket", "How urgent?", []string{"low", "high"})
	if err != nil {
		t.Fatal(err)
	}
	if scored.Score != 1 || scored.Legend["1"] != "high" {
		t.Fatalf("%+v", scored)
	}

	type decoded struct {
		Model string `json:"model"`
	}
	out, err := client.NoulAs[decoded](ctx, "ticket", jevNoul())
	if err != nil {
		t.Fatal(err)
	}
	if out.Model != "jev-1.13.0" {
		t.Fatalf("%+v", out)
	}
}

func TestShortcutRejectsAChoiceList(t *testing.T) {
	client, err := New(WithAPIKey("k"), WithRetry(zeroRetry()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Classify(context.Background(), "x", "which?", []string{"a", "b"})
	if !errors.Is(err, ErrRequest) {
		t.Fatalf("got %v", err)
	}
}

func jevNoul() Noul {
	return Noul{Instructions: "billing?", Criteria: &NoulCriteria{True: "money"}}
}
