package jev

import (
	"context"
	"fmt"
)

// AnswerKey is the question name used by the single-question methods
// ([Client.Noul], [Client.Choice], [Client.Score], [Client.Classify], [Client.Rate]).
// In a struct passed to the As methods, read it as answers.answer.
// The name is a key of the questions map in the [request body]; the API does
// not see it as part of the prompt.
//
// [request body]: https://docs.typesafe.ai/api#request-body
const AnswerKey = "answer"

// Noul asks one yes/no question and returns the probability of yes.
// question is a [Noul] value, or the instructions alone: a string, or any value
// that encodes as a JSON object or array. See [noul] and [reading a noul].
//
// The call is one [Client.SystemOne] request. For several questions about the
// same state, use SystemOne; see [ask more than one question per call].
//
// [noul]: https://docs.typesafe.ai/api#noul
// [reading a noul]: https://docs.typesafe.ai/primitives/noul#reading-a-noul
// [ask more than one question per call]: https://docs.typesafe.ai/primitives/noul#good-practice-ask-more-than-one-question-per-call
func (c *Client) Noul(ctx context.Context, state any, question any, opts ...Option) (NoulAnswer, error) {
	q, err := asNoul(question)
	if err != nil {
		return NoulAnswer{}, err
	}
	resp, err := c.SystemOne(ctx, oneQuestion(state, q), opts...)
	if err != nil {
		return NoulAnswer{}, err
	}
	answer, ok := resp.Noul(AnswerKey)
	if !ok {
		return NoulAnswer{}, fmt.Errorf("%w: noul answer %q is missing", ErrResponse, AnswerKey)
	}
	return answer, nil
}

// NoulAs asks one yes/no question and decodes the response JSON into T.
// It calls [Client.SystemOneAs]. The question name in that JSON is [AnswerKey].
// The shape to mirror is under [noul answer].
//
// [noul answer]: https://docs.typesafe.ai/api#noul-answer
func (c *Client) NoulAs[T any](ctx context.Context, state any, question any, opts ...Option) (T, error) {
	var zero T
	q, err := asNoul(question)
	if err != nil {
		return zero, err
	}
	return c.SystemOneAs[T](ctx, oneQuestion(state, q), opts...)
}

// Choice asks one choice question. See [choice] and [choice answer].
// The call is one [Client.SystemOne] request; several questions about one
// state belong in one SystemOne call, see [ask more than one question per call].
//
// [choice]: https://docs.typesafe.ai/api#choice
// [choice answer]: https://docs.typesafe.ai/api#choice-answer
// [ask more than one question per call]: https://docs.typesafe.ai/primitives/choice#good-practice-ask-more-than-one-question-per-call
func (c *Client) Choice(ctx context.Context, state any, q Choice, opts ...Option) (ChoiceAnswer, error) {
	resp, err := c.SystemOne(ctx, oneQuestion(state, q), opts...)
	if err != nil {
		return ChoiceAnswer{}, err
	}
	answer, ok := resp.Choice(AnswerKey)
	if !ok {
		return ChoiceAnswer{}, fmt.Errorf("%w: choice answer %q is missing", ErrResponse, AnswerKey)
	}
	return answer, nil
}

// ChoiceAs asks one choice question and decodes the response JSON into T.
// It calls [Client.SystemOneAs]. The question name in that JSON is [AnswerKey].
// The shape to mirror is under [choice answer].
//
// [choice answer]: https://docs.typesafe.ai/api#choice-answer
func (c *Client) ChoiceAs[T any](ctx context.Context, state any, q Choice, opts ...Option) (T, error) {
	return c.SystemOneAs[T](ctx, oneQuestion(state, q), opts...)
}

// Score asks one score question. See [score], [levels], and [score answer].
// The call is one [Client.SystemOne] request.
//
// [score]: https://docs.typesafe.ai/api#score
// [levels]: https://docs.typesafe.ai/primitives/score#levels
// [score answer]: https://docs.typesafe.ai/api#score-answer
func (c *Client) Score(ctx context.Context, state any, q Score, opts ...Option) (ScoreAnswer, error) {
	resp, err := c.SystemOne(ctx, oneQuestion(state, q), opts...)
	if err != nil {
		return ScoreAnswer{}, err
	}
	answer, ok := resp.Score(AnswerKey)
	if !ok {
		return ScoreAnswer{}, fmt.Errorf("%w: score answer %q is missing", ErrResponse, AnswerKey)
	}
	return answer, nil
}

// ScoreAs asks one score question and decodes the response JSON into T.
// It calls [Client.SystemOneAs]. The question name in that JSON is [AnswerKey].
// The shape to mirror is under [score answer].
//
// [score answer]: https://docs.typesafe.ai/api#score-answer
func (c *Client) ScoreAs[T any](ctx context.Context, state any, q Score, opts ...Option) (T, error) {
	return c.SystemOneAs[T](ctx, oneQuestion(state, q), opts...)
}

// Classify picks one option. instructions is the question. criteria maps each
// option to a description, or nil when the option needs none; see [choice].
// It calls [Client.Choice].
//
// [choice]: https://docs.typesafe.ai/api#choice
func (c *Client) Classify(ctx context.Context, state any, instructions any, criteria any, opts ...Option) (ChoiceAnswer, error) {
	return c.Choice(ctx, state, Choice{Instructions: instructions, Criteria: criteria}, opts...)
}

// ClassifyAs picks one option and decodes the response JSON into T.
// It calls [Client.ChoiceAs]. The question name in that JSON is [AnswerKey].
// The shape to mirror is under [choice answer].
//
// [choice answer]: https://docs.typesafe.ai/api#choice-answer
func (c *Client) ClassifyAs[T any](ctx context.Context, state any, instructions any, criteria any, opts ...Option) (T, error) {
	return c.ChoiceAs[T](ctx, state, Choice{Instructions: instructions, Criteria: criteria}, opts...)
}

// Rate places the state on an ordered rubric. levels is a JSON array, usually
// a []string, lowest level first; see [score] and [writing good levels].
// It calls [Client.Score].
//
// [score]: https://docs.typesafe.ai/api#score
// [writing good levels]: https://docs.typesafe.ai/primitives/score#writing-good-levels
func (c *Client) Rate(ctx context.Context, state any, instructions any, levels any, opts ...Option) (ScoreAnswer, error) {
	return c.Score(ctx, state, Score{Instructions: instructions, Criteria: levels}, opts...)
}

// RateAs places the state on an ordered rubric and decodes the response JSON into T.
// It calls [Client.ScoreAs]. The question name in that JSON is [AnswerKey].
// The shape to mirror is under [score answer].
//
// [score answer]: https://docs.typesafe.ai/api#score-answer
func (c *Client) RateAs[T any](ctx context.Context, state any, instructions any, levels any, opts ...Option) (T, error) {
	return c.ScoreAs[T](ctx, state, Score{Instructions: instructions, Criteria: levels}, opts...)
}

func oneQuestion(state any, q Question) Request {
	return Request{
		State:     state,
		Questions: Questions{AnswerKey: q},
	}
}

func asNoul(question any) (Noul, error) {
	switch q := question.(type) {
	case Noul:
		return q, nil
	case *Noul:
		if q == nil {
			return Noul{}, fmt.Errorf("%w: nil noul", ErrRequest)
		}
		return *q, nil
	default:
		return Noul{Instructions: question}, nil
	}
}
