package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"fennec/cua/internal/protocol"
)

// Question types.
const (
	// Noul is a yes/no question answered with the probability of yes.
	Noul = "noul"
	// Choice picks one key of the question's criteria.
	Choice = "choice"
)

// DecideArgs asks questions about a state. State is text, any JSON value, or
// a list of text and image_url parts; Questions maps keys to {type: noul |
// choice, instructions, criteria?}. Both are sent as given.
type DecideArgs struct {
	State     json.RawMessage `json:"state"`
	Questions json.RawMessage `json:"questions"`
	// Image says the state holds a screenshot: the verify model answers when
	// one is configured, else the decide model.
	Image bool `json:"image,omitempty"`
	// TimeoutMS caps the configured timeout when above 0.
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// Answer is one question's normalized answer.
type Answer struct {
	Type string `json:"-"` // Noul or Choice
	// Probability is the probability of yes, in [0, 1], for a noul question.
	Probability float64
	// Choice, Confidence and Probabilities answer a choice question.
	Choice        string
	Confidence    float64
	Probabilities map[string]float64
}

// MarshalJSON writes {probability} for a noul answer and {choice, confidence,
// probabilities} for a choice.
func (a Answer) MarshalJSON() ([]byte, error) {
	if a.Type == Noul {
		return encodeJSON(struct {
			Probability float64 `json:"probability"`
		}{a.Probability})
	}
	return encodeJSON(struct {
		Choice        string             `json:"choice"`
		Confidence    float64            `json:"confidence"`
		Probabilities map[string]float64 `json:"probabilities"`
	}{a.Choice, a.Confidence, a.Probabilities})
}

// Decided holds one answer per question and who answered.
type Decided struct {
	Answers map[string]Answer `json:"answers"`
	Served  protocol.Served   `json:"served"`
	Usage   Usage             `json:"usage"`
}

type question struct {
	Type     string                     `json:"type"`
	Criteria map[string]json.RawMessage `json:"criteria"`
}

func parseQuestions(raw json.RawMessage) (map[string]question, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return nil, invalid("decide: questions must be an object of questions")
	}
	var qs map[string]json.RawMessage
	if err := json.Unmarshal(t, &qs); err != nil {
		return nil, invalid("decide: questions are not valid JSON: %v", err)
	}
	if len(qs) == 0 {
		return nil, invalid("decide: no questions")
	}
	out := make(map[string]question, len(qs))
	for k, v := range qs {
		var q question
		if vt := bytes.TrimSpace(v); len(vt) == 0 || vt[0] != '{' || json.Unmarshal(vt, &q) != nil {
			return nil, invalid("decide: question %s must be an object with a type and instructions", k)
		}
		if q.Type != Noul && q.Type != Choice {
			return nil, invalid("decide: question %s has type %q, not noul or choice", k, q.Type)
		}
		out[k] = q
	}
	return out, nil
}

func (c *Client) decideModel(image bool) (role, model string) {
	if !image {
		return RoleDecide, c.cfg.Models.Decide
	}
	if v := c.cfg.Models.Verify; v != nil {
		return RoleVerify, *v
	}
	return RoleVerify, c.cfg.Models.Decide
}

// Decide asks the questions about the state, on the decisions endpoint or
// over chat as configured. With a decide_build pattern, an answer from a
// build that does not match fails.
func (c *Client) Decide(ctx context.Context, args DecideArgs) (*Decided, error) {
	qs, err := parseQuestions(args.Questions)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(args.State)) == 0 || !json.Valid(args.State) {
		return nil, invalid("decide: state is missing or not valid JSON")
	}
	role, model := c.decideModel(args.Image)
	timeout := c.timeout(args.TimeoutMS)
	var d *Decided
	if c.cfg.DecideAPI == DecideChat {
		d, err = c.decideOverChat(ctx, role, model, args, qs, timeout)
	} else {
		d, err = c.decideOnDecisions(ctx, role, model, args, qs, timeout)
	}
	if err != nil {
		return nil, err
	}
	if p := c.cfg.DecideBuild; p != nil && !BuildMatches(d.Served.Build, *p) {
		return nil, c.fail(protocol.ErrModelUnavailable,
			"%s answered from build %q, which does not match decide_build %q", model, d.Served.Build, *p)
	}
	return d, nil
}

type decisionsBody struct {
	Model     string          `json:"model"`
	State     json.RawMessage `json:"state"`
	Questions json.RawMessage `json:"questions"`
}

type decisionsReply struct {
	ID      *string                    `json:"id"`
	Model   *string                    `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   map[string]any             `json:"usage"`
}

func (c *Client) decideOnDecisions(ctx context.Context, role, model string, args DecideArgs, qs map[string]question, timeout time.Duration) (*Decided, error) {
	body := decisionsBody{Model: model, State: args.State, Questions: args.Questions}
	text, at, err := c.post(ctx, c.cfg.DecisionsURL, model, body, timeout)
	if err != nil {
		return nil, err
	}
	var r decisionsReply
	if err := c.decodeReply(at, text, &r); err != nil {
		return nil, err
	}
	u := usageOf(model, r.Usage)
	c.record(ctx, u)
	answers, err := normalize(model, r.Answers, qs)
	if err != nil {
		return nil, err
	}
	return &Decided{Answers: answers, Served: served(role, model, r.Model, r.ID), Usage: u}, nil
}

const decideChatInstructions = `You judge what a computer screen shows. Answer every question below and reply with only one JSON object whose keys are the question keys.
For a question of type "noul" (yes or no), answer {"noul": P}, where P from 0 to 1 is the probability that the answer is yes.
For a question of type "choice", answer {"choice": KEY, "probabilities": {KEY: P, ...}}, where KEY is one of the question's criteria keys and the probabilities cover every criteria key and sum to 1.`

func (c *Client) decideOverChat(ctx context.Context, role, model string, args DecideArgs, qs map[string]question, timeout time.Duration) (*Decided, error) {
	var asked bytes.Buffer
	if err := json.Compact(&asked, args.Questions); err != nil {
		return nil, invalid("decide: questions are not valid JSON: %v", err)
	}
	state, err := StateParts(args.State)
	if err != nil {
		return nil, err
	}
	parts := append([]Part{
		TextPart(decideChatInstructions + "\nQuestions: " + asked.String()),
		TextPart("State:"),
	}, state...)
	r, err := c.complete(ctx, role, model, parts, decideMaxTokens, timeout)
	if err != nil {
		return nil, err
	}
	given, err := replyObject(model, r.text)
	if err != nil {
		return nil, err
	}
	answers, err := normalize(model, given, qs)
	if err != nil {
		return nil, err
	}
	return &Decided{Answers: answers, Served: r.served, Usage: r.usage}, nil
}

// replyObject reads the JSON object of a chat reply, from its first '{' to its
// last '}'.
func replyObject(model, text string) (map[string]json.RawMessage, error) {
	s, e := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if s < 0 || e <= s {
		return nil, &Error{Code: protocol.ErrModelUnavailable, Message: fmt.Sprintf(
			"%s: the reply holds no JSON object: %s", model, snippet(text, replySnippet))}
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text[s:e+1]), &out); err != nil {
		return nil, &Error{Code: protocol.ErrModelUnavailable, Message: fmt.Sprintf(
			"%s: the reply's JSON does not parse: %v: %s", model, err, snippet(text, replySnippet))}
	}
	return out, nil
}

// normalize yields exactly one answer per question. A noul probability is
// clamped to [0, 1]. A choice outside the criteria falls back to the criterion
// with the highest probability; the confidence is the model's own, else the
// chosen key's probability.
func normalize(model string, given map[string]json.RawMessage, qs map[string]question) (map[string]Answer, error) {
	keys := make([]string, 0, len(qs))
	for k := range qs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	bad := func(format string, a ...any) error {
		return &Error{Code: protocol.ErrModelUnavailable, Message: model + ": " + fmt.Sprintf(format, a...)}
	}
	out := make(map[string]Answer, len(qs))
	for _, k := range keys {
		q := qs[k]
		var a map[string]json.RawMessage
		raw, ok := given[k]
		if t := bytes.TrimSpace(raw); !ok || len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &a) != nil {
			return nil, bad("no answer for %s", k)
		}
		if q.Type == Noul {
			p, ok := asFloat(a["noul"])
			if !ok {
				return nil, bad("the answer to %s has no noul probability", k)
			}
			out[k] = Answer{Type: Noul, Probability: min(max(p, 0), 1)}
			continue
		}
		var choice string
		if json.Unmarshal(a["choice"], &choice) != nil {
			return nil, bad("the answer to %s has no choice", k)
		}
		var rawProbs map[string]json.RawMessage
		if t := bytes.TrimSpace(a["probabilities"]); len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &rawProbs) != nil {
			return nil, bad("the answer to %s has no probabilities", k)
		}
		probs := make(map[string]float64, len(rawProbs))
		for ck, v := range rawProbs {
			if p, ok := asFloat(v); ok {
				probs[ck] = p
			}
		}
		confidence, own := asFloat(a["confidence"])
		if q.Criteria != nil {
			if _, listed := q.Criteria[choice]; !listed {
				best, found := likeliest(probs, q.Criteria)
				if !found {
					return nil, bad("%s chose %q, which is none of its criteria", k, choice)
				}
				choice, own = best, false
			}
		}
		if !own {
			confidence = probs[choice]
		}
		out[k] = Answer{Type: Choice, Choice: choice, Confidence: confidence, Probabilities: probs}
	}
	return out, nil
}

// likeliest is the criterion with the highest probability; ties go to the
// first key in sorted order.
func likeliest(probs map[string]float64, criteria map[string]json.RawMessage) (string, bool) {
	keys := make([]string, 0, len(probs))
	for k := range probs {
		if _, ok := criteria[k]; ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "", false
	}
	sort.Strings(keys)
	best := keys[0]
	for _, k := range keys[1:] {
		if probs[k] > probs[best] {
			best = k
		}
	}
	return best, true
}

func asFloat(raw json.RawMessage) (float64, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || (t[0] != '-' && (t[0] < '0' || t[0] > '9')) {
		return 0, false
	}
	var f float64
	if json.Unmarshal(t, &f) != nil {
		return 0, false
	}
	return f, true
}

// ---------------------------------------------------------------------------
// Script arguments

// ParseDecideArgs reads decide arguments from a script; malformed arguments
// are an invalid_argument error.
func ParseDecideArgs(raw json.RawMessage) (DecideArgs, error) {
	var a DecideArgs
	if err := strictDecode(raw, &a); err != nil {
		return a, invalid("decide: %v", err)
	}
	if _, err := parseQuestions(a.Questions); err != nil {
		return a, err
	}
	if len(bytes.TrimSpace(a.State)) == 0 {
		return a, invalid("decide: state is missing")
	}
	if a.TimeoutMS < 0 {
		return a, invalid("decide: timeout_ms must not be negative")
	}
	return a, nil
}

// ParseAskArgs reads ask arguments from a script.
func ParseAskArgs(raw json.RawMessage) (AskArgs, error) {
	var a AskArgs
	if err := strictDecode(raw, &a); err != nil {
		return a, invalid("ask: %v", err)
	}
	return a, a.validate()
}

// ParseGroundArgs reads ground arguments from a script.
func ParseGroundArgs(raw json.RawMessage) (GroundArgs, error) {
	var a GroundArgs
	if err := strictDecode(raw, &a); err != nil {
		return a, invalid("ground: %v", err)
	}
	return a, a.validate()
}

func strictDecode(raw json.RawMessage, v any) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '{' {
		return fmt.Errorf("arguments must be an object")
	}
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after the arguments")
	}
	return nil
}
