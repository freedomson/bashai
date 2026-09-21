package jev

import (
	"os"
	"strings"
	"testing"
)

func TestSkillNamesTheRealAPI(t *testing.T) {
	body, err := os.ReadFile("skills/jev/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, must := range []string{
		"jev.New",
		"SystemOne",
		"SystemOneAs",
		"ListModels",
		"jev.Noul",
		"jev.Choice",
		"jev.Score",
		"client.Noul",
		"client.Classify",
		"client.Rate",
		"AnswerKey",
		"WithAPIKey",
		"TYPESAFE_API_KEY",
		"DefaultRetryPolicy",
		"ErrUnauthorized",
		"ErrOverloaded",
		"WithRateLimit",
		"WithRateLimiter",
		"NewLimiter",
		"DefaultRateLimit",
	} {
		if !strings.Contains(text, must) {
			t.Errorf("SKILL.md does not mention %s", must)
		}
	}
	for _, banned := range []string{
		"jev.NewClient",
		"client.Ask",
		".Evaluate(",
		"httpclient.New",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("SKILL.md mentions %s, which this module does not provide", banned)
		}
	}

	api, err := os.ReadFile("skills/jev/api.md")
	if err != nil {
		t.Fatal(err)
	}
	apiText := string(api)
	for _, must := range []string{
		"func New(opts ...Option)",
		"func (c *Client) SystemOne(",
		"func (c *Client) SystemOneAs[T any]",
		"func (c *Client) ListModels(",
		"type Noul struct",
		"type Choice struct",
		"type Score struct",
		"func (c *Client) Classify(",
		"const AnswerKey",
		"type Questions map[string]Question",
		"func WithRateLimit(limit RateLimit) Option",
		"func WithRateLimiter(l Limiter) Option",
		"func NewLimiter(limit RateLimit) Limiter",
		"type RateLimit struct",
		"type Limiter interface",
		"func (c *Client) Limiter() Limiter",
	} {
		if !strings.Contains(apiText, must) {
			t.Errorf("api.md does not mention %s", must)
		}
	}
}
