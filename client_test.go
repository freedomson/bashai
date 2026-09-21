package jev

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEncodeLeavesMarkupAlone(t *testing.T) {
	body, _, err := (Request{
		State: "a < b & c > d",
		Questions: Questions{
			"billing": Noul{Instructions: "is a < b?"},
			"team": Choice{
				Instructions: "which?",
				Criteria:     map[string]any{"billing": nil, "technical": "bugs & outages"},
			},
		},
	}).encode("jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, `\u003c`) || strings.Contains(text, `\u003e`) || strings.Contains(text, `\u0026`) {
		t.Fatalf("JSON escaped markup: %s", text)
	}
	if !strings.Contains(text, `"billing":null`) {
		t.Fatalf("nil choice description was not JSON null: %s", text)
	}
	if strings.Contains(text, `"criteria"`) && strings.Contains(text, `"type":"noul"`) {
		// The noul question must not grow a criteria field. The choice question has one.
		noul := text[strings.Index(text, `"billing"`):strings.Index(text, `"team"`)]
		if strings.Contains(noul, `"criteria"`) {
			t.Fatalf("noul encoded criteria: %s", noul)
		}
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	req := Request{
		State: map[string]any{"b": 1, "a": "x < y"},
		Questions: Questions{
			"z": Noul{Instructions: "z?"},
			"a": Score{Instructions: "how?", Criteria: []string{"low", "high"}},
		},
		Extra: map[string]any{"trace": "1"},
	}
	first, _, err := req.encode("jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := req.encode("jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("encoding changed\n%s\n%s", first, second)
	}
}

func TestEncodeBytesStateIsText(t *testing.T) {
	body, _, err := (Request{
		State:     []byte("a < b"),
		Questions: Questions{"q": Noul{Instructions: "yes?"}},
	}).encode("jev-latest")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"state":"a < b"`) {
		t.Fatalf("state = %s", body)
	}
}

func TestEncodeRejects(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{name: "no questions", req: Request{State: "x"}, want: "at least one question"},
		{name: "score map", req: Request{Questions: Questions{"q": Score{Instructions: "how?", Criteria: map[string]string{"0": "low"}}}}},
		{name: "choice list", req: Request{Questions: Questions{"q": Choice{Criteria: []string{"a", "b"}}}}},
		{name: "choice empty", req: Request{Questions: Questions{"q": Choice{Criteria: map[string]any{}}}}},
		{name: "score empty", req: Request{Questions: Questions{"q": Score{Criteria: []string{}}}}},
		{name: "score too long", req: Request{Questions: Questions{"q": Score{Criteria: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}}}}},
		{name: "extra state", req: Request{Questions: Questions{"q": Noul{Instructions: "x"}}, Extra: map[string]any{"state": "nope"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tc.req.encode("jev-latest")
			if !errors.Is(err, ErrRequest) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestNewRequiresKey(t *testing.T) {
	t.Setenv(envAPIKey, "")
	_, err := New()
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("got %v", err)
	}
}

func TestReservedHeader(t *testing.T) {
	_, err := New(WithAPIKey("test-key"), WithHeader("Authorization", "nope"))
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("got %v", err)
	}
}

func TestSystemOneRoundTrip(t *testing.T) {
	var seen atomic.Int32
	var gotBody []byte
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != pathSystemOne || r.Method != http.MethodPost {
			t.Errorf("path %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("User-Agent") != "jev/"+Version {
			t.Errorf("user agent %s", r.Header.Get("User-Agent"))
		}
		if !strings.HasPrefix(r.Header.Get("X-Typesafe-Sdk"), "jev/") {
			t.Errorf("sdk header %s", r.Header.Get("X-Typesafe-Sdk"))
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("X-Typesafe-Request-Id", "req_1")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model":"jev-1.13.0",
			"answers":{
				"billing":{"type":"noul","noul":0.95},
				"team":{"type":"choice","choice":"billing","confidence":0.8,"probabilities":{"billing":0.9,"technical":0.1}},
				"urgency":{"type":"score","score":1.05,"confidence":0.7,"legend":{"0":"can wait","1":"this week","2":"today"},"probabilities":{"0":0.1,"1":0.8,"2":0.1}}
			},
			"usage":{"input_tokens":12,"output_tokens":3}
		}`)
	}))
	defer srv.Close()

	client, err := New(
		WithAPIKey("test-key"),
		WithBaseURL(srv.URL),
		WithRetry(zeroRetry()),
		WithHeader("X-Request-Tag", "ticket"),
	)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SystemOne(context.Background(), Request{
		State: "a < b & c",
		Questions: Questions{
			"billing": Noul{Instructions: "billing?"},
			"team": Choice{Instructions: "team?", Criteria: map[string]any{
				"billing": "payments", "technical": nil,
			}},
			"urgency": Score{Instructions: "how urgent?", Criteria: []string{"can wait", "this week", "today"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth %q", gotAuth)
	}
	if strings.Contains(string(gotBody), `\u003c`) {
		t.Fatalf("request escaped markup: %s", gotBody)
	}
	if resp.Model != "jev-1.13.0" || resp.RequestID != "req_1" || resp.Attempts != 1 {
		t.Fatalf("%+v", resp)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage %+v", resp.Usage)
	}
	billing, ok := resp.Noul("billing")
	if !ok || billing.Noul != 0.95 {
		t.Fatalf("noul %+v %v", billing, ok)
	}
	team, ok := resp.Choice("team")
	if !ok || team.Choice != "billing" || team.Probabilities["technical"] != 0.1 {
		t.Fatalf("choice %+v", team)
	}
	urgency, ok := resp.Score("urgency")
	if !ok || urgency.Score != 1.05 || urgency.Legend["2"] != "today" {
		t.Fatalf("score %+v", urgency)
	}
	if seen.Load() != 1 {
		t.Fatalf("attempts %d", seen.Load())
	}

	type ticket struct {
		Model string `json:"model"`
		Usage struct {
			Input int `json:"input_tokens"`
		} `json:"usage"`
	}
	decoded, err := resp.As[ticket]()
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Model != "jev-1.13.0" || decoded.Usage.Input != 12 {
		t.Fatalf("%+v", decoded)
	}
	again, err := client.SystemOneAs[ticket](context.Background(), Request{
		State:     "a < b & c",
		Questions: Questions{"billing": Noul{Instructions: "billing?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Model != "jev-1.13.0" {
		t.Fatalf("%+v", again)
	}
}

func TestRetryAfterThenSuccess(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := seen.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After-Ms", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":"slow down"}`)
			return
		}
		if got := r.Header.Get("X-Typesafe-Retry-Count"); got != "1" {
			t.Errorf("retry count %q", got)
		}
		_, _ = io.WriteString(w, `{
			"model":"jev-1.13.0",
			"answers":{"billing":{"type":"noul","noul":1}},
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer srv.Close()

	policy := DefaultRetryPolicy()
	policy.randFloat = func() float64 { return 0 }
	client, err := New(WithAPIKey("k"), WithBaseURL(srv.URL), WithRetry(policy), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SystemOne(context.Background(), Request{
		State:     "x",
		Questions: Questions{"billing": Noul{Instructions: "billing?"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempts != 2 {
		t.Fatalf("attempts %d", resp.Attempts)
	}
	if seen.Load() != 2 {
		t.Fatalf("seen %d", seen.Load())
	}
}

func TestUnauthorizedIsNotRetried(t *testing.T) {
	var seen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer srv.Close()
	client, err := New(WithAPIKey("k"), WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SystemOne(context.Background(), Request{
		State:     "x",
		Questions: Questions{"q": Noul{Instructions: "q?"}},
	})
	var api *APIError
	if !errors.As(err, &api) || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("got %v", err)
	}
	if api.Message != "bad key" || api.Attempts != 1 || seen.Load() != 1 {
		t.Fatalf("api %+v seen %d", api, seen.Load())
	}
}

func TestCancelIsNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"later"}`)
	}))
	defer srv.Close()
	client, err := New(WithAPIKey("k"), WithBaseURL(srv.URL), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = client.SystemOne(ctx, Request{
		State:     "x",
		Questions: Questions{"q": Noul{Instructions: "q?"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestBadAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"model":"jev-1.13.0",
			"answers":{"billing":{"type":"choice","choice":"nope","confidence":0.2,"probabilities":{"a":1}}},
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer srv.Close()
	client, err := New(WithAPIKey("k"), WithBaseURL(srv.URL), WithRetry(zeroRetry()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.SystemOne(context.Background(), Request{
		State:     "x",
		Questions: Questions{"billing": Noul{Instructions: "billing?"}},
	})
	if !errors.Is(err, ErrResponse) {
		t.Fatalf("got %v", err)
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != pathModels {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"jev-latest","description":"alias","release_date":"2026-09-15"}]}`)
	}))
	defer srv.Close()
	client, err := New(WithAPIKey("k"), WithBaseURL(srv.URL), WithRetry(zeroRetry()))
	if err != nil {
		t.Fatal(err)
	}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "jev-latest" || models[0].ReleaseDate != "2026-09-15" {
		t.Fatalf("%+v", models)
	}
}

func TestLogsRedactAuthorization(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"model":"jev-1.13.0",
			"answers":{"q":{"type":"noul","noul":0}},
			"usage":{"input_tokens":1,"output_tokens":0}
		}`)
	}))
	defer srv.Close()
	client, err := New(WithAPIKey("super-secret"), WithBaseURL(srv.URL), WithRetry(zeroRetry()), WithLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SystemOne(context.Background(), Request{
		State: "x", Questions: Questions{"q": Noul{Instructions: "q?"}},
	}); err != nil {
		t.Fatal(err)
	}
	logs := buf.String()
	if strings.Contains(logs, "super-secret") {
		t.Fatalf("log leaked the key:\n%s", logs)
	}
	if !strings.Contains(logs, "[redacted]") {
		t.Fatalf("log did not redact:\n%s", logs)
	}
}

func TestRetryDelayHonorsServerAndBackoff(t *testing.T) {
	p := DefaultRetryPolicy()
	p.randFloat = func() float64 { return 1 } // subtract the full jitter
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	h := make(http.Header)
	h.Set("Retry-After-Ms", "25")
	if d := p.delay(0, h, now); d != 25*time.Millisecond {
		t.Fatalf("retry-after %s", d)
	}
	h.Set("Retry-After-Ms", "120000")
	d := p.delay(0, h, now)
	want := time.Duration(float64(p.InitialBackoff) * 0.75)
	if d != want {
		t.Fatalf("fallback delay %s, want %s", d, want)
	}
	h = make(http.Header)
	h.Set("Retry-After", now.Add(2*time.Second).Format(http.TimeFormat))
	if d := p.delay(0, h, now); d != 2*time.Second {
		t.Fatalf("http-date %s", d)
	}
}

func TestExtractValidationMessage(t *testing.T) {
	msg := extractMessage([]byte(`{"detail":[{"loc":["body","questions","tone","criteria"],"msg":"too short"}]}`))
	if msg != "questions.tone.criteria: too short" {
		t.Fatalf("%q", msg)
	}
}

func zeroRetry() RetryPolicy {
	p := DefaultRetryPolicy()
	p.MaxRetries = 0
	return p
}
