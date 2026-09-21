package jev

import (
	"bytes"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"encoding/json/jsontext"
)

// Response is a successful System One result: the [response body] of one call.
// Model is the versioned ID that answered, even when the request named an
// alias. Attempts counts HTTP attempts including retries.
//
// [response body]: https://docs.typesafe.ai/api#response-body
type Response struct {
	Model     string
	Answers   map[string]Answer
	Usage     Usage
	RequestID string
	Attempts  int

	raw []byte
}

// Usage counts tokens for one call. Input tokens are billed and count toward
// the token rate limit; output tokens are free. See [usage] and [current models].
//
// [usage]: https://docs.typesafe.ai/sdk/javascript/api/interfaces/Usage
// [current models]: https://docs.typesafe.ai/models#current-models
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Answer is the result of one question: [NoulAnswer], [ChoiceAnswer],
// [ScoreAnswer], or [UnknownAnswer]. Every answer carries a type matching
// its question; see [answer types].
//
// [answer types]: https://docs.typesafe.ai/api#answer-types
type Answer interface {
	Type() string
}

// NoulAnswer is the probability that a yes/no question is yes, from 0 (no)
// to 1 (yes). See [noul answer] and [reading a noul].
//
// [noul answer]: https://docs.typesafe.ai/api#noul-answer
// [reading a noul]: https://docs.typesafe.ai/primitives/noul#reading-a-noul
type NoulAnswer struct {
	Noul float64
}

func (NoulAnswer) Type() string { return "noul" }

// ChoiceAnswer is the selected option, a probability for every option, and
// a confidence derived from that distribution. See [choice answer], and
// [confidence] for how confidence differs from the winning probability.
//
// [choice answer]: https://docs.typesafe.ai/api#choice-answer
// [confidence]: https://docs.typesafe.ai/confidence
type ChoiceAnswer struct {
	Choice        string
	Confidence    float64
	Probabilities map[string]float64
}

func (ChoiceAnswer) Type() string { return "choice" }

// ScoreAnswer is a probability-weighted position along the rubric.
// Score can fall between levels. Legend maps the level index, as a string, back
// to the description you sent. Probabilities uses those same string keys.
// See [score answer] and [reading a score].
//
// [score answer]: https://docs.typesafe.ai/api#score-answer
// [reading a score]: https://docs.typesafe.ai/primitives/score#reading-a-score
type ScoreAnswer struct {
	Score         float64
	Confidence    float64
	Legend        map[string]any
	Probabilities map[string]float64
}

func (ScoreAnswer) Type() string { return "score" }

// UnknownAnswer is a question kind this version does not model.
// Raw is the JSON object for that answer. The kinds this version knows are
// listed under [answer types].
//
// [answer types]: https://docs.typesafe.ai/api#answer-types
type UnknownAnswer struct {
	Kind string
	Raw  []byte
}

func (a UnknownAnswer) Type() string { return a.Kind }

// Noul returns the yes/no answer named name. See [noul answer].
//
// [noul answer]: https://docs.typesafe.ai/api#noul-answer
func (r *Response) Noul(name string) (NoulAnswer, bool) {
	if r == nil {
		return NoulAnswer{}, false
	}
	a, ok := r.Answers[name].(NoulAnswer)
	return a, ok
}

// Choice returns the choice answer named name. See [choice answer].
//
// [choice answer]: https://docs.typesafe.ai/api#choice-answer
func (r *Response) Choice(name string) (ChoiceAnswer, bool) {
	if r == nil {
		return ChoiceAnswer{}, false
	}
	a, ok := r.Answers[name].(ChoiceAnswer)
	return a, ok
}

// Score returns the score answer named name. See [score answer].
//
// [score answer]: https://docs.typesafe.ai/api#score-answer
func (r *Response) Score(name string) (ScoreAnswer, bool) {
	if r == nil {
		return ScoreAnswer{}, false
	}
	a, ok := r.Answers[name].(ScoreAnswer)
	return a, ok
}

// Raw returns a copy of the response JSON, in the shape of the [response body].
//
// [response body]: https://docs.typesafe.ai/api#response-body
func (r *Response) Raw() []byte {
	if r == nil {
		return nil
	}
	return bytes.Clone(r.raw)
}

// As decodes the response JSON into T.
// JSON object names are case-sensitive. Unknown object members are ignored.
// The names to mirror in T are under [response body] and [answer types].
//
// [response body]: https://docs.typesafe.ai/api#response-body
// [answer types]: https://docs.typesafe.ai/api#answer-types
func (r *Response) As[T any]() (T, error) {
	var out T
	if r == nil || len(r.raw) == 0 {
		return out, fmt.Errorf("%w: empty response", ErrResponse)
	}
	if err := unmarshal(r.raw, &out); err != nil {
		return out, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	return out, nil
}

type usageProbe struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
}

type responseProbe struct {
	Model   *string                   `json:"model"`
	Answers map[string]jsontext.Value `json:"answers"`
	Usage   *usageProbe               `json:"usage"`
}

func decodeResponse(body []byte, specs map[string]questionSpec) (*Response, error) {
	var probe responseProbe
	if err := unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResponse, err)
	}
	if probe.Model == nil || strings.TrimSpace(*probe.Model) == "" {
		return nil, fieldError("model", fmt.Errorf("%w: model is missing", ErrResponse))
	}
	if probe.Answers == nil {
		return nil, fieldError("answers", fmt.Errorf("%w: answers is missing", ErrResponse))
	}
	usage, err := decodeUsage(probe.Usage)
	if err != nil {
		return nil, err
	}
	answers := make(map[string]Answer, len(probe.Answers))
	for name, spec := range specs {
		raw, ok := probe.Answers[name]
		if !ok {
			return nil, fieldError("answers."+name, fmt.Errorf("%w: answer is missing", ErrResponse))
		}
		answer, err := decodeAnswer(name, raw, spec)
		if err != nil {
			return nil, err
		}
		answers[name] = answer
	}
	for name, raw := range probe.Answers {
		if _, ok := answers[name]; ok {
			continue
		}
		answer, err := decodeAnswer(name, raw, questionSpec{})
		if err != nil {
			return nil, err
		}
		answers[name] = answer
	}
	return &Response{
		Model:   *probe.Model,
		Answers: answers,
		Usage:   usage,
		raw:     bytes.Clone(body),
	}, nil
}

func decodeUsage(u *usageProbe) (Usage, error) {
	if u == nil || u.InputTokens == nil || u.OutputTokens == nil {
		return Usage{}, fieldError("usage", fmt.Errorf("%w: usage is missing input_tokens or output_tokens", ErrResponse))
	}
	if *u.InputTokens < 0 || *u.OutputTokens < 0 {
		return Usage{}, fieldError("usage", fmt.Errorf("%w: token counts must not be negative", ErrResponse))
	}
	return Usage{InputTokens: *u.InputTokens, OutputTokens: *u.OutputTokens}, nil
}

func decodeAnswer(name string, raw jsontext.Value, spec questionSpec) (Answer, error) {
	var kindProbe struct {
		Type *string `json:"type"`
	}
	if err := unmarshal(raw, &kindProbe); err != nil {
		return nil, fieldError("answers."+name, err)
	}
	if kindProbe.Type == nil || *kindProbe.Type == "" {
		return nil, fieldError("answers."+name+".type", fmt.Errorf("%w: type is missing", ErrResponse))
	}
	kind := *kindProbe.Type
	if spec.kind != "" && kind != spec.kind {
		return nil, fieldError("answers."+name+".type", fmt.Errorf("%w: got %q, question was %q", ErrResponse, kind, spec.kind))
	}
	switch kind {
	case "noul":
		return decodeNoul(name, raw)
	case "choice":
		return decodeChoice(name, raw, spec.options)
	case "score":
		return decodeScore(name, raw, spec.levels)
	default:
		if spec.kind != "" {
			return nil, fieldError("answers."+name+".type", fmt.Errorf("%w: unsupported type %q", ErrResponse, kind))
		}
		return UnknownAnswer{Kind: kind, Raw: bytes.Clone(raw)}, nil
	}
}

func decodeNoul(name string, raw jsontext.Value) (NoulAnswer, error) {
	var probe struct {
		Noul *float64 `json:"noul"`
	}
	if err := unmarshal(raw, &probe); err != nil {
		return NoulAnswer{}, fieldError("answers."+name, err)
	}
	if probe.Noul == nil || !unitInterval(*probe.Noul) {
		return NoulAnswer{}, fieldError("answers."+name+".noul", fmt.Errorf("%w: noul must be a number from 0 to 1", ErrResponse))
	}
	return NoulAnswer{Noul: *probe.Noul}, nil
}

func decodeChoice(name string, raw jsontext.Value, options []string) (ChoiceAnswer, error) {
	var probe struct {
		Choice        *string            `json:"choice"`
		Confidence    *float64           `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := unmarshal(raw, &probe); err != nil {
		return ChoiceAnswer{}, fieldError("answers."+name, err)
	}
	path := "answers." + name
	if probe.Choice == nil || *probe.Choice == "" {
		return ChoiceAnswer{}, fieldError(path+".choice", fmt.Errorf("%w: choice is missing", ErrResponse))
	}
	if probe.Confidence == nil || !unitInterval(*probe.Confidence) {
		return ChoiceAnswer{}, fieldError(path+".confidence", fmt.Errorf("%w: confidence must be a number from 0 to 1", ErrResponse))
	}
	if probe.Probabilities == nil {
		return ChoiceAnswer{}, fieldError(path+".probabilities", fmt.Errorf("%w: probabilities is missing", ErrResponse))
	}
	for key, p := range probe.Probabilities {
		if !unitInterval(p) {
			return ChoiceAnswer{}, fieldError(path+".probabilities."+key, fmt.Errorf("%w: probability must be from 0 to 1", ErrResponse))
		}
	}
	if len(options) > 0 {
		if !slices.Contains(options, *probe.Choice) {
			return ChoiceAnswer{}, fieldError(path+".choice", fmt.Errorf("%w: %q is not one of the options", ErrResponse, *probe.Choice))
		}
		for _, option := range options {
			if _, ok := probe.Probabilities[option]; !ok {
				return ChoiceAnswer{}, fieldError(path+".probabilities."+option, fmt.Errorf("%w: probability is missing", ErrResponse))
			}
		}
	}
	return ChoiceAnswer{
		Choice:        *probe.Choice,
		Confidence:    *probe.Confidence,
		Probabilities: probe.Probabilities,
	}, nil
}

func decodeScore(name string, raw jsontext.Value, levels int) (ScoreAnswer, error) {
	var probe struct {
		Score         *float64           `json:"score"`
		Confidence    *float64           `json:"confidence"`
		Legend        map[string]any     `json:"legend"`
		Probabilities map[string]float64 `json:"probabilities"`
	}
	if err := unmarshal(raw, &probe); err != nil {
		return ScoreAnswer{}, fieldError("answers."+name, err)
	}
	path := "answers." + name
	if probe.Score == nil || math.IsNaN(*probe.Score) || math.IsInf(*probe.Score, 0) {
		return ScoreAnswer{}, fieldError(path+".score", fmt.Errorf("%w: score must be a finite number", ErrResponse))
	}
	if probe.Confidence == nil || !unitInterval(*probe.Confidence) {
		return ScoreAnswer{}, fieldError(path+".confidence", fmt.Errorf("%w: confidence must be a number from 0 to 1", ErrResponse))
	}
	if probe.Legend == nil {
		return ScoreAnswer{}, fieldError(path+".legend", fmt.Errorf("%w: legend is missing", ErrResponse))
	}
	if probe.Probabilities == nil {
		return ScoreAnswer{}, fieldError(path+".probabilities", fmt.Errorf("%w: probabilities is missing", ErrResponse))
	}
	for key, p := range probe.Probabilities {
		if !unitInterval(p) {
			return ScoreAnswer{}, fieldError(path+".probabilities."+key, fmt.Errorf("%w: probability must be from 0 to 1", ErrResponse))
		}
	}
	if levels > 0 {
		for i := range levels {
			key := strconv.Itoa(i)
			if _, ok := probe.Legend[key]; !ok {
				return ScoreAnswer{}, fieldError(path+".legend."+key, fmt.Errorf("%w: legend entry is missing", ErrResponse))
			}
			if _, ok := probe.Probabilities[key]; !ok {
				return ScoreAnswer{}, fieldError(path+".probabilities."+key, fmt.Errorf("%w: probability is missing", ErrResponse))
			}
		}
	}
	return ScoreAnswer{
		Score:         *probe.Score,
		Confidence:    *probe.Confidence,
		Legend:        probe.Legend,
		Probabilities: probe.Probabilities,
	}, nil
}

func unitInterval(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1
}

func fieldError(field string, err error) error {
	return &ResponseError{Field: field, Err: err}
}
