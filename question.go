package jev

import (
	"fmt"
	"reflect"
	"strings"

	"encoding/json/jsontext"
)

// Questions maps the name you choose to a question. It is the questions
// member of the [request body].
// Answers come back under the same names. Names are not part of the prompt.
// On the wire, names are sorted so two equal requests encode to the same bytes.
//
// [request body]: https://docs.typesafe.ai/api#request-body
type Questions map[string]Question

// Question is a [Noul], [Choice], [Score], or [Raw]. The three kinds and how
// to pick one are described under [question types] and [choose a question type].
//
// [question types]: https://docs.typesafe.ai/api#question-types
// [choose a question type]: https://docs.typesafe.ai/primitives#choose-a-question-type
type Question interface {
	isQuestion()
}

// Noul asks a yes-or-no question.
// The answer is the probability that the answer is yes, from 0 to 1.
// The wire shape is under [noul], and [writing a noul question] has advice on
// phrasing.
//
// [noul]: https://docs.typesafe.ai/api#noul
// [writing a noul question]: https://docs.typesafe.ai/primitives/noul#writing-a-noul-question
type Noul struct {
	// Instructions is the question: a string, or any value that encodes as a
	// JSON object or array. Nil omits the field. See [structured instructions].
	//
	// [structured instructions]: https://docs.typesafe.ai/primitives/advanced#structured-instructions
	Instructions any
	// Criteria optionally describes what a yes and a no mean.
	Criteria *NoulCriteria
}

// NoulCriteria describes the two outcomes of a [Noul]: what a value near 1
// means, and what a value near 0 means. Nil fields are omitted.
// See [noul] and [structured noul criteria].
//
// [noul]: https://docs.typesafe.ai/api#noul
// [structured noul criteria]: https://docs.typesafe.ai/primitives/advanced#structured-noul-criteria
type NoulCriteria struct {
	True  any `json:"true,omitzero"`
	False any `json:"false,omitzero"`
}

// Choice picks one option from a set you define.
// Criteria is a JSON object: a map[string]any, a map[string]string, or a struct
// with json tags. A nil description leaves that option undescribed and is sent
// as null. At least one option is required, and more than 255 is rejected.
// The wire shape is under [choice]; [structured choice options] shows how a
// description can be an object rather than a string.
//
// [choice]: https://docs.typesafe.ai/api#choice
// [structured choice options]: https://docs.typesafe.ai/primitives/advanced#structured-choice-options
type Choice struct {
	Instructions any
	Criteria     any
}

// Score rates the state against an ordered rubric.
// Criteria is a JSON array of level descriptions, lowest first, such as a
// []string or []any. The API schema requires at least one level. The public
// docs cap a score at 10 levels, and this client enforces that cap.
// A map is rejected: since SDK 0.6.0, score criteria are a list, not an object.
// The wire shape is under [score]; [writing good levels] has advice on the rubric.
//
// [score]: https://docs.typesafe.ai/api#score
// [writing good levels]: https://docs.typesafe.ai/primitives/score#writing-good-levels
type Score struct {
	Instructions any
	Criteria     any
}

// Raw is a question object sent as-is, for a kind this version does not model.
// It must include a non-empty string field named type.
// Known kinds are validated the same way as [Noul], [Choice], and [Score].
// The current kinds are listed under [question types].
//
// [question types]: https://docs.typesafe.ai/api#question-types
type Raw map[string]any

func (Noul) isQuestion()   {}
func (Choice) isQuestion() {}
func (Score) isQuestion()  {}
func (Raw) isQuestion()    {}

const (
	maxChoiceOptions = 255
	maxScoreLevels   = 10
)

type questionSpec struct {
	kind    string
	options []string
	levels  int
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions,omitzero"`
	Criteria     any    `json:"criteria,omitzero"`
}

// Request is the input to [Client.SystemOne]: the [request body] of one POST
// to /v1/systemone.
//
// [request body]: https://docs.typesafe.ai/api#request-body
type Request struct {
	// State is the content every question reads. A string, a map, a slice, or
	// a struct with json tags. A []byte is sent as text, not as base64.
	// Nil is sent as JSON null. What to put in it, and how to point a
	// question at a nested field, is under [state].
	//
	// [state]: https://docs.typesafe.ai/concepts/state
	State any
	// Questions is the non-empty set of named questions.
	Questions Questions
	// Model overrides the client default when non-empty. See [models].
	//
	// [models]: https://docs.typesafe.ai/models#current-models
	Model string
	// Extra adds top-level JSON fields this version does not model.
	// Keys state, model, and questions are reserved and rejected.
	Extra map[string]any
}

func (r Request) encode(defaultModel string) ([]byte, map[string]questionSpec, error) {
	if len(r.Questions) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one question is required", ErrRequest)
	}
	model := strings.TrimSpace(r.Model)
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		return nil, nil, fmt.Errorf("%w: model is required", ErrRequest)
	}

	questions := make(map[string]rawJSON, len(r.Questions))
	specs := make(map[string]questionSpec, len(r.Questions))
	for name, question := range r.Questions {
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("%w: question name is empty", ErrRequest)
		}
		if isNilQuestion(question) {
			return nil, nil, fmt.Errorf("%w: question %q is nil", ErrRequest, name)
		}
		encoded, spec, err := encodeQuestion(name, question)
		if err != nil {
			return nil, nil, err
		}
		questions[name] = encoded
		specs[name] = spec
	}

	body := make(map[string]any, len(r.Extra)+3)
	for key, value := range r.Extra {
		switch key {
		case "state", "model", "questions":
			return nil, nil, fmt.Errorf("%w: extra field %q replaces %s; put that on Request instead", ErrRequest, key, key)
		case "":
			return nil, nil, fmt.Errorf("%w: extra field name is empty", ErrRequest)
		}
		body[key] = value
	}
	state, err := normalizeState(r.State)
	if err != nil {
		return nil, nil, err
	}
	body["state"] = state
	body["model"] = model
	body["questions"] = questions

	encoded, err := marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode request: %v", ErrRequest, err)
	}
	return encoded, specs, nil
}

// rawJSON inlines its bytes. A plain []byte would be base64-encoded.
type rawJSON []byte

func (v rawJSON) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteValue(jsontext.Value(v))
}

func encodeQuestion(name string, question Question) (rawJSON, questionSpec, error) {
	wire, err := questionWire(name, question)
	if err != nil {
		return nil, questionSpec{}, err
	}
	encoded, err := marshal(wire)
	if err != nil {
		return nil, questionSpec{}, fmt.Errorf("%w: question %q: %v", ErrRequest, name, err)
	}
	spec, err := inspectQuestion(name, encoded)
	if err != nil {
		return nil, questionSpec{}, err
	}
	return rawJSON(encoded), spec, nil
}

func questionWire(name string, question Question) (any, error) {
	switch q := question.(type) {
	case Noul:
		var criteria any
		if q.Criteria != nil {
			criteria = q.Criteria
		}
		return wireQuestion{Type: "noul", Instructions: q.Instructions, Criteria: criteria}, nil
	case Choice:
		if isNilValue(q.Criteria) {
			return nil, fmt.Errorf("%w: choice %q requires criteria", ErrRequest, name)
		}
		return wireQuestion{Type: "choice", Instructions: q.Instructions, Criteria: q.Criteria}, nil
	case Score:
		if isNilValue(q.Criteria) {
			return nil, fmt.Errorf("%w: score %q requires criteria", ErrRequest, name)
		}
		return wireQuestion{Type: "score", Instructions: q.Instructions, Criteria: q.Criteria}, nil
	case Raw:
		return map[string]any(q), nil
	default:
		return nil, fmt.Errorf("%w: question %q has an unknown Go type %T", ErrRequest, name, question)
	}
}

type questionProbe struct {
	Type         string         `json:"type"`
	Instructions jsontext.Value `json:"instructions"`
	Criteria     jsontext.Value `json:"criteria"`
}

func inspectQuestion(name string, encoded []byte) (questionSpec, error) {
	var probe questionProbe
	if err := unmarshal(encoded, &probe); err != nil {
		return questionSpec{}, fmt.Errorf("%w: question %q: %v", ErrRequest, name, err)
	}
	kind := strings.TrimSpace(probe.Type)
	if kind == "" {
		return questionSpec{}, fmt.Errorf("%w: question %q requires a non-empty type", ErrRequest, name)
	}
	spec := questionSpec{kind: kind}
	switch kind {
	case "noul":
		return spec, nil
	case "choice":
		options, err := choiceOptions(name, probe.Criteria)
		if err != nil {
			return questionSpec{}, err
		}
		spec.options = options
		return spec, nil
	case "score":
		n, err := scoreLevels(name, probe.Criteria)
		if err != nil {
			return questionSpec{}, err
		}
		spec.levels = n
		return spec, nil
	default:
		return spec, nil
	}
}

func choiceOptions(name string, raw jsontext.Value) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: choice %q requires criteria", ErrRequest, name)
	}
	var obj map[string]jsontext.Value
	if err := unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("%w: choice %q criteria must be a JSON object, not a list", ErrRequest, name)
	}
	if len(obj) == 0 {
		return nil, fmt.Errorf("%w: choice %q needs at least one option", ErrRequest, name)
	}
	if len(obj) > maxChoiceOptions {
		return nil, fmt.Errorf("%w: choice %q has %d options; the maximum is %d", ErrRequest, name, len(obj), maxChoiceOptions)
	}
	options := make([]string, 0, len(obj))
	for key := range obj {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%w: choice %q has an empty option name", ErrRequest, name)
		}
		options = append(options, key)
	}
	return options, nil
}

func scoreLevels(name string, raw jsontext.Value) (int, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("%w: score %q requires criteria", ErrRequest, name)
	}
	var levels []jsontext.Value
	if err := unmarshal(raw, &levels); err != nil {
		return 0, fmt.Errorf("%w: score %q criteria must be a list of descriptions, not a map", ErrRequest, name)
	}
	if len(levels) < 1 {
		return 0, fmt.Errorf("%w: score %q needs at least one level", ErrRequest, name)
	}
	if len(levels) > maxScoreLevels {
		return 0, fmt.Errorf("%w: score %q has %d levels; the maximum is %d", ErrRequest, name, len(levels), maxScoreLevels)
	}
	return len(levels), nil
}

func normalizeState(state any) (any, error) {
	switch v := state.(type) {
	case []byte:
		return string(v), nil
	case jsontext.Value:
		if err := checkRawJSON("state", v); err != nil {
			return nil, err
		}
		return rawJSON(v), nil
	case rawJSON:
		if err := checkRawJSON("state", jsontext.Value(v)); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return state, nil
	}
}

func checkRawJSON(field string, v jsontext.Value) error {
	if len(v) == 0 {
		return fmt.Errorf("%w: %s raw JSON is empty", ErrRequest, field)
	}
	dec := jsontext.NewDecoder(strings.NewReader(string(v)))
	if _, err := dec.ReadValue(); err != nil {
		return fmt.Errorf("%w: %s raw JSON: %v", ErrRequest, field, err)
	}
	return nil
}

func isNilQuestion(q Question) bool {
	return isNilValue(q)
}

func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}
