package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"fennec/cua/internal/protocol"
)

// seen is one request the fake endpoint received.
type seen struct {
	Path  string
	Query string
	Auth  string
	Body  string
}

func (s seen) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s.Body), &m); err != nil {
		t.Fatalf("request body is not JSON: %v: %s", err, s.Body)
	}
	return m
}

type fake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []seen
	base string // the /v1 root
}

// newFake serves handle and records every request.
func newFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *fake {
	t.Helper()
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, seen{Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization"), Body: string(b)})
		f.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.base = f.srv.URL + "/v1"
	return f
}

// replying answers every request with body as JSON.
func replying(t *testing.T, body any) *fake {
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	})
}

func (f *fake) requests() []seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seen(nil), f.got...)
}

// gateway is the default configuration pointed at the fake, with a file key.
func (f *fake) gateway() Config {
	c := Defaults()
	c.BaseURL = f.base
	c.DecisionsURL = f.base + "/decisions"
	c.APIKey = "gw-key"
	return c
}

func client(t *testing.T, cfg Config) *Client {
	t.Helper()
	c, err := New(cfg, WithGetenv(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func chatReply(content string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}}
}

var ctx = context.Background()

func TestRequestBodies(t *testing.T) {
	reply := chatReply(`{"point_2d": [500, 250]}`)
	reply["answers"] = map[string]any{"pick": map[string]any{"choice": "r1", "confidence": 0.9, "probabilities": map[string]any{"r1": 0.9, "none": 0.1}}}
	f := replying(t, reply)
	m := client(t, f.gateway())

	state := `["the list",{"type":"image_url","image_url":{"url":"data:image/png;base64,UE5H"}}]`
	questions := `{"pick":{"type":"choice","instructions":"Which?","criteria":{"r1":"a","none":"b"}}}`
	if _, err := m.Decide(ctx, DecideArgs{State: raw(state), Questions: raw(questions), Image: true, TimeoutMS: 5000}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ask(ctx, AskArgs{Prompt: "What is it?", Images: []string{"UE5H"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ground(ctx, GroundArgs{PNG: "UE5H", Target: "OK button", Area: Area{Width: 1280, Height: 800}}); err != nil {
		t.Fatal(err)
	}

	got := f.requests()
	if len(got) != 3 {
		t.Fatalf("requests = %d, want 3", len(got))
	}
	for _, s := range got {
		if s.Auth != "Bearer gw-key" {
			t.Errorf("%s: Authorization = %q", s.Path, s.Auth)
		}
	}
	want := []struct{ path, body string }{
		{"/v1/decisions", `{"model":"typesafe/jev-1.14","state":` + state + `,"questions":` + questions + `}`},
		{"/v1/chat/completions", `{"model":"anthropic/claude-sonnet-5","max_tokens":400,"messages":[{"role":"user","content":[{"type":"text","text":"What is it?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,UE5H"}}]}]}`},
	}
	for i, w := range want {
		if got[i].Path != w.path || got[i].Body != w.body {
			t.Errorf("request %d:\n got %s %s\nwant %s %s", i, got[i].Path, got[i].Body, w.path, w.body)
		}
	}
	// The decisions body holds exactly model, state and questions.
	if keys := sortedKeys(got[0].json(t)); !reflect.DeepEqual(keys, []string{"model", "questions", "state"}) {
		t.Errorf("decisions keys = %v", keys)
	}

	g := got[2].json(t)
	if got[2].Path != "/v1/chat/completions" || g["model"] != DefaultGroundModel || g["max_tokens"] != float64(100) {
		t.Errorf("ground request = %s %s", got[2].Path, got[2].Body)
	}
	content := g["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("ground content = %v", content)
	}
	img := content[0].(map[string]any)
	if img["type"] != "image_url" || img["image_url"].(map[string]any)["url"] != "data:image/png;base64,UE5H" {
		t.Errorf("ground first part = %v", img)
	}
	prompt := content[1].(map[string]any)["text"].(string)
	for _, want := range []string{"OK button", "point_2d", "1000"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("ground prompt lacks %q: %s", want, prompt)
		}
	}
	if msgs := g["messages"].([]any); len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("ground messages = %v", msgs)
	}
}

func sortedKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestStateParts(t *testing.T) {
	parts, err := StateParts(raw(`["the list", {"type": "image_url", "image_url": {"url": "data:image/png;base64,QUJD"}}, {"k": 1}, 7]`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Part{TextPart("the list"), PNGPart("QUJD"), TextPart(`{"k":1}`), TextPart("7")}
	if !reflect.DeepEqual(parts, want) {
		t.Errorf("parts = %#v", parts)
	}
	parts, err = StateParts(raw(`{"window_title": "x"}`))
	if err != nil || !reflect.DeepEqual(parts, []Part{TextPart(`{"window_title":"x"}`)}) {
		t.Errorf("object state = %#v, %v", parts, err)
	}
	parts, err = StateParts(raw(`"just text"`))
	if err != nil || !reflect.DeepEqual(parts, []Part{TextPart("just text")}) {
		t.Errorf("string state = %#v, %v", parts, err)
	}
	// An image part that is not a PNG data URL stays text.
	parts, _ = StateParts(raw(`[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`))
	if parts[0].PNG != "" {
		t.Errorf("a non-PNG URL became an image: %#v", parts)
	}

	b, _ := json.Marshal(PNGPart("QUJD"))
	if string(b) != `{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}` {
		t.Errorf("png part = %s", b)
	}
	b, _ = encodeJSON(TextPart("a<b"))
	if string(b) != `{"type":"text","text":"a<b"}` {
		t.Errorf("text part = %s", b)
	}
	if _, err := StateParts(nil); !isCode(err, protocol.ErrInvalid) {
		t.Errorf("missing state: %v", err)
	}
}

func isCode(err error, code protocol.ErrorCode) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

const twoQuestions = `{"on": {"type": "noul", "instructions": "Is it on?"}, "pick": {"type": "choice", "instructions": "Which?", "criteria": {"r1": "a", "none": "b"}}}`

func mustQuestions(t *testing.T, s string) map[string]question {
	t.Helper()
	qs, err := parseQuestions(raw(s))
	if err != nil {
		t.Fatal(err)
	}
	return qs
}

func TestDecideAnswers(t *testing.T) {
	qs := mustQuestions(t, twoQuestions)
	given, err := replyObject("m", "Sure.\n{\"on\": {\"noul\": 1.4}, \"pick\": {\"choice\": \"r1\", \"probabilities\": {\"r1\": 0.8, \"none\": 0.2}}}\nDone.")
	if err != nil {
		t.Fatal(err)
	}
	a, err := normalize("m", given, qs)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 2 {
		t.Fatalf("answers = %v", a)
	}
	j, _ := json.Marshal(a)
	if string(j) != `{"on":{"probability":1},"pick":{"choice":"r1","confidence":0.8,"probabilities":{"none":0.2,"r1":0.8}}}` {
		t.Errorf("answers = %s", j)
	}
	// Below 0 clamps to 0; the model's own confidence wins when the choice is listed.
	given, _ = replyObject("m", `{"on": {"noul": -0.3}, "pick": {"choice": "none", "confidence": 0.55, "probabilities": {"r1": 0.4, "none": 0.6}}, "extra": {"noul": 1}}`)
	a, err = normalize("m", given, qs)
	if err != nil {
		t.Fatal(err)
	}
	if a["on"].Probability != 0 || a["pick"].Choice != "none" || a["pick"].Confidence != 0.55 || len(a) != 2 {
		t.Errorf("answers = %+v", a)
	}
	// A choice question without criteria takes any choice.
	free := mustQuestions(t, `{"p": {"type": "choice", "instructions": "?"}}`)
	given, _ = replyObject("m", `{"p": {"choice": "zz", "probabilities": {"zz": 0.7}}}`)
	if a, err := normalize("m", given, free); err != nil || a["p"].Choice != "zz" || a["p"].Confidence != 0.7 {
		t.Errorf("free choice = %+v, %v", a, err)
	}
}

func TestDecideBadReplies(t *testing.T) {
	qs := mustQuestions(t, twoQuestions)
	cases := []struct{ reply, want string }{
		{`{"on": {"noul": 0.2}}`, "m: no answer for pick"},
		{`{"on": {"noul": 0.2}, "pick": {"choice": "r1"}}`, "m: the answer to pick has no probabilities"},
		{`{"on": {"type": "noul"}, "pick": {"choice": "r1", "probabilities": {"r1": 1}}}`, "m: the answer to on has no noul probability"},
		{`{"on": {"noul": "high"}, "pick": {"choice": "r1", "probabilities": {"r1": 1}}}`, "m: the answer to on has no noul probability"},
		{`{"on": {"noul": 0.2}, "pick": {"probabilities": {"r1": 1}}}`, "m: the answer to pick has no choice"},
		{`{"on": 0.2, "pick": {"choice": "r1", "probabilities": {"r1": 1}}}`, "m: no answer for on"},
	}
	for _, c := range cases {
		given, err := replyObject("m", c.reply)
		if err != nil {
			t.Fatal(err)
		}
		_, err = normalize("m", given, qs)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %s", c.reply, err, c.want)
		}
	}
	if _, err := replyObject("m", "no json here"); err == nil || !strings.HasPrefix(err.Error(), "m: the reply holds no JSON object") {
		t.Errorf("no object: %v", err)
	}
	if _, err := replyObject("m", `{"on": [}`); err == nil || !strings.HasPrefix(err.Error(), "m: ") {
		t.Errorf("bad object: %v", err)
	}

	// Over the wire: an empty chat reply, a reply without text, a wrong JSON type.
	for _, body := range []any{
		map[string]any{},
		chatReply(""),
		map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": nil}}}},
	} {
		f := replying(t, body)
		cfg := f.gateway()
		cfg.DecideAPI = DecideChat
		_, err := client(t, cfg).Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: raw(twoQuestions)})
		if err == nil || !strings.Contains(err.Error(), "no message text") || !strings.HasPrefix(err.Error(), DefaultDecideModel+" at ") {
			t.Errorf("%v: err = %v", body, err)
		}
	}
	f := replying(t, map[string]any{"answers": []any{1, 2}})
	_, err := client(t, f.gateway()).Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: raw(twoQuestions)})
	if err == nil || !strings.HasPrefix(err.Error(), DefaultDecideModel+" at ") || !isCode(err, protocol.ErrModelUnavailable) {
		t.Errorf("wrong type: %v", err)
	}
	f = newFake(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[1]`)) })
	_, err = client(t, f.gateway()).Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: raw(twoQuestions)})
	if err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("array reply: %v", err)
	}
}

func TestDecideFallback(t *testing.T) {
	qs := mustQuestions(t, twoQuestions)
	given, _ := replyObject("m", `{"on": {"noul": 0.2}, "pick": {"choice": "r2", "confidence": 0.99, "probabilities": {"r2": 0.9, "r1": 0.3, "none": 0.1}}}`)
	a, err := normalize("m", given, qs)
	if err != nil {
		t.Fatal(err)
	}
	if a["pick"].Choice != "r1" || a["pick"].Confidence != 0.3 {
		t.Errorf("fallback = %+v", a["pick"])
	}
	given, _ = replyObject("m", `{"on": {"noul": 0.2}, "pick": {"choice": "r2", "probabilities": {"r2": 1.0}}}`)
	if _, err := normalize("m", given, qs); err == nil || err.Error() != `m: pick chose "r2", which is none of its criteria` {
		t.Errorf("no listed option: %v", err)
	}
}

func TestDecideVerifyModel(t *testing.T) {
	f := replying(t, map[string]any{
		"model": "jev-1.14-20261014", "id": "gen-7",
		"answers": map[string]any{
			"on":   map[string]any{"type": "noul", "noul": 0.9},
			"pick": map[string]any{"type": "choice", "choice": "r2", "confidence": 0.7, "probabilities": map[string]any{"r2": 0.7, "none": 0.3}},
		},
		"usage": map[string]any{"input_tokens": 284, "output_tokens": 21, "cost": 0.0000119},
	})
	cfg := f.gateway()
	verify := "jev-verify"
	cfg.Models.Verify = &verify
	m := client(t, cfg)
	q := `{"on": {"type": "noul", "instructions": "On?"}, "pick": {"type": "choice", "instructions": "Which?", "criteria": {"r2": "x", "none": "y"}}}`

	out, err := m.Decide(ctx, DecideArgs{State: raw(`"a window"`), Questions: raw(q), Image: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answers["on"].Probability != 0.9 || out.Answers["pick"].Choice != "r2" || out.Answers["pick"].Confidence != 0.7 {
		t.Errorf("answers = %+v", out.Answers)
	}
	if out.Served != (protocol.Served{Role: RoleVerify, Model: "jev-verify", Build: "jev-1.14-20261014", ID: "gen-7"}) {
		t.Errorf("served = %+v", out.Served)
	}
	if out.Usage != (Usage{Model: "jev-verify", InputTokens: 284, OutputTokens: 21}) {
		t.Errorf("usage = %+v", out.Usage)
	}
	// Without an image the decide model answers.
	if _, err := m.Decide(ctx, DecideArgs{State: raw(`"a window"`), Questions: raw(q)}); err != nil {
		t.Fatal(err)
	}
	got := f.requests()
	if got[0].Path != "/v1/decisions" || got[0].json(t)["model"] != "jev-verify" || got[1].json(t)["model"] != DefaultDecideModel {
		t.Errorf("models asked = %s, %s", got[0].Body, got[1].Body)
	}
	// With no verify model an image judgment goes to the decide model.
	m2 := client(t, f.gateway())
	if out, err := m2.Decide(ctx, DecideArgs{State: raw(`"w"`), Questions: raw(q), Image: true}); err != nil || out.Served.Model != DefaultDecideModel {
		t.Errorf("fallback to decide: %+v, %v", out, err)
	}
	if u := m.Usage(); u["jev-verify"].Calls != 1 || u[DefaultDecideModel].Calls != 1 || u["jev-verify"].InputTokens != 284 {
		t.Errorf("tally = %+v", u)
	}
}

func TestDecideOverChat(t *testing.T) {
	f := replying(t, map[string]any{
		"id": "chatcmpl-1", "model": "local-vl",
		"choices": []any{map[string]any{"message": map[string]any{"content": "Here:\n{\"on\": {\"noul\": 0.2}}"}}},
		"usage":   map[string]any{"prompt_tokens": 50, "completion_tokens": 9, "prompt_tokens_details": map[string]any{"cached_tokens": 32}},
	})
	cfg := f.gateway()
	cfg.APIKey = ""
	cfg.DecideAPI = DecideChat
	cfg.DecisionsURL = ""
	cfg.Models.Decide = "local-vl"
	m := client(t, cfg)
	out, err := m.Decide(ctx, DecideArgs{
		State:     raw(`["the window", {"type": "image_url", "image_url": {"url": "data:image/png;base64,UE5H"}}]`),
		Questions: raw(`{"on": {"type": "noul", "instructions": "On?"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answers["on"].Probability != 0.2 || out.Served.Build != "local-vl" || out.Served.ID != "chatcmpl-1" {
		t.Errorf("out = %+v", out)
	}
	if out.Usage != (Usage{Model: "local-vl", InputTokens: 50, OutputTokens: 9, CachedTokens: 32}) {
		t.Errorf("usage = %+v", out.Usage)
	}
	got := f.requests()
	if len(got) != 1 || got[0].Path != "/v1/chat/completions" || got[0].Auth != "" {
		t.Fatalf("requests = %+v", got)
	}
	body := got[0].json(t)
	if body["max_tokens"] != float64(800) {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 4 {
		t.Fatalf("content = %v", content)
	}
	if !strings.Contains(content[0].(map[string]any)["text"].(string), `"on"`) {
		t.Errorf("first part does not ask the questions: %v", content[0])
	}
	if content[2].(map[string]any)["text"] != "the window" ||
		content[3].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,UE5H" {
		t.Errorf("state parts = %v", content[2:])
	}
}

func TestBuildPattern(t *testing.T) {
	p := "typesafe/jev-1.14-########"
	for _, ok := range []string{"typesafe/jev-1.14-20261014", "typesafe/jev-1.14-20270101"} {
		if !BuildMatches(ok, p) {
			t.Errorf("%s should match", ok)
		}
	}
	for _, other := range []string{
		"typesafe/jev-1.14", "typesafe/jev-1.13-20260917", "typesafe/jev-preview",
		"typesafe/jev-1.14-2026", "typesafe/jev-1.141-20261014", "typesafe/jev-1.14-2026101",
		"typesafe/jev-1.14-202610140", "typesafe/jev-1.14-2026101x", "~typesafe/jev-latest", "",
		"typesafe/jev-1.14-2026101\u0663",
	} {
		if BuildMatches(other, p) {
			t.Errorf("%q should not match", other)
		}
	}
	// An empty pattern is refused at load; no pattern takes any build.
	empty := ""
	cfg := Defaults()
	cfg.DecideBuild = &empty
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "decide_build is empty") {
		t.Errorf("empty pattern: %v", err)
	}
	if _, err := ParseConfigJSON([]byte(`{"decide_build": ""}`)); err == nil {
		t.Error("an empty decide_build loaded")
	}
}

func TestDecideBuildPin(t *testing.T) {
	q := raw(`{"on": {"type": "noul", "instructions": "On?"}}`)
	answer := map[string]any{"on": map[string]any{"noul": 0.5}}
	pattern := "typesafe/jev-1.14-########"
	for _, build := range []any{"typesafe/jev-1.14-20261014", "typesafe/jev-1.14", nil} {
		f := replying(t, map[string]any{"model": build, "answers": answer})
		cfg := f.gateway()
		cfg.DecideBuild = &pattern
		_, err := client(t, cfg).Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: q, Image: true})
		if build == "typesafe/jev-1.14-20261014" {
			if err != nil {
				t.Errorf("matching build refused: %v", err)
			}
			continue
		}
		name, _ := build.(string)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) || !strings.Contains(err.Error(), pattern) {
			t.Errorf("build %v: err = %v", build, err)
		}
		// Without a pattern any build answers.
		if _, err := client(t, f.gateway()).Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: q}); err != nil {
			t.Errorf("unpinned build %v: %v", build, err)
		}
	}
	// The pin applies over chat too.
	f := replying(t, map[string]any{"model": "other-7", "choices": []any{map[string]any{"message": map[string]any{"content": `{"on": {"noul": 1}}`}}}})
	cfg := f.gateway()
	cfg.DecideAPI = DecideChat
	cfg.DecideBuild = &pattern
	if _, err := client(t, cfg).Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: q}); err == nil || !strings.Contains(err.Error(), `"other-7"`) {
		t.Errorf("chat pin: %v", err)
	}
}

func TestGroundPoint(t *testing.T) {
	screen := Area{Width: 1280, Height: 800}
	crop := Area{Left: 320, Top: 200, Width: 640, Height: 400}
	cases := []struct {
		text       string
		area       Area
		wantX, wan int
	}{
		{`{"point_2d": [500, 250]}`, screen, 640, 200},
		{`[250.5, 750]`, crop, 480, 500},
		{`point_2d 7: (300, 900)`, screen, 384, 720},
		{`[1000, 1000]`, screen, 1279, 799},
		{`[0, 0]`, screen, 0, 0},
		{`[1000, 0]`, crop, 959, 200},
		{`The button is at x=12 , 40 roughly`, screen, 15, 32},
		{`[500, 500]`, Area{Left: -1440, Top: -100, Width: 1440, Height: 900}, -720, 350},
	}
	for _, c := range cases {
		x, y, err := GroundPoint(c.text, "t", c.area)
		if err != nil || x != c.wantX || y != c.wan {
			t.Errorf("%s on %+v = (%d, %d), %v; want (%d, %d)", c.text, c.area, x, y, err, c.wantX, c.wan)
		}
	}
	if _, _, err := GroundPoint("no point", "OK", screen); err == nil || err.Error() != "ground: the reply gives no point for OK: no point" {
		t.Errorf("no point: %v", err)
	}
	if _, _, err := GroundPoint("[1001, 5]", "OK", screen); err == nil || err.Error() != "ground: the model placed OK at (1001, 5), outside the 0 to 1000 scale" {
		t.Errorf("beyond 1000: %v", err)
	}
	if _, _, err := GroundPoint("[-3, 5]", "OK", screen); err == nil || !strings.Contains(err.Error(), "OK") {
		t.Errorf("below 0: %v", err)
	}
}

func TestGroundRetina(t *testing.T) {
	// The PNG is a 2x capture (2560x1600) of a 1280x800 area; only the area counts.
	cases := []struct {
		said       string
		wantX, wan int
	}{
		{`{"point_2d": [500, 500]}`, 640, 400},
		{`{"point_2d": [1000, 1000]}`, 1279, 799},
		{`{"point_2d": [0, 0]}`, 0, 0},
	}
	for _, c := range cases {
		f := replying(t, chatReply(c.said))
		m := client(t, f.gateway())
		out, err := m.Ground(ctx, GroundArgs{PNG: "UE5H", Target: "OK button", Area: Area{Width: 1280, Height: 800}})
		if err != nil || out.X != c.wantX || out.Y != c.wan {
			t.Errorf("%s: %+v, %v", c.said, out, err)
		}
	}
	f := replying(t, chatReply(`{"point_2d": [1200, 10]}`))
	if _, err := client(t, f.gateway()).Ground(ctx, GroundArgs{PNG: "UE5H", Target: "OK button", Area: Area{Width: 1280, Height: 800}}); err == nil || !strings.Contains(err.Error(), "OK button") {
		t.Errorf("out of range: %v", err)
	}
	// Malformed arguments from a script are invalid arguments, with no request.
	for _, a := range []string{`{"png_b64": "UE5H", "target": "x"}`, `{"png_b64": "", "target": "x", "area": {"width": 5, "height": 5}}`, `{"png_b64": "UE5H", "target": "x", "area": {"width": 5, "height": 5}, "extra": 1}`, `[]`} {
		if _, err := ParseGroundArgs(raw(a)); !isCode(err, protocol.ErrInvalid) {
			t.Errorf("%s: %v", a, err)
		}
	}
	if _, err := ParseGroundArgs(raw(`{"png_b64": "UE5H", "target": "x", "area": {"left": 3, "width": 5, "height": 5}}`)); err != nil {
		t.Errorf("good ground args: %v", err)
	}
}

func TestErrorNamesEndpoint(t *testing.T) {
	// An error status names the URL that answered after the redirect, with up to 300 characters of body.
	long := strings.Repeat("x", 400)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			http.Redirect(w, r, "/moved?key=sk-1#frag", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("oops" + long))
	})
	cfg := f.gateway()
	_, err := client(t, cfg).Ask(ctx, AskArgs{Prompt: "x"})
	want := DefaultAskModel + " at " + f.srv.URL + "/moved answered 500 Internal Server Error: oops" + long[:296]
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant %s", err, want)
	}
	got := f.requests()
	if len(got) != 2 || got[1].Path != "/moved" || got[1].Query != "key=sk-1" {
		t.Errorf("requests = %+v", got)
	}

	// A failed send names the URL requested, redacted.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	cfg = Defaults()
	cfg.BaseURL = strings.Replace(deadURL, "http://", "http://user:pw@", 1) + "/v1?key=sk-secret#f"
	cfg.APIKey = "sk-secret-key"
	_, err = client(t, cfg).Ask(ctx, AskArgs{Prompt: "x"})
	if err == nil || !strings.HasPrefix(err.Error(), DefaultAskModel+" request to "+deadURL+"/v1") || !isCode(err, protocol.ErrModelUnavailable) {
		t.Errorf("failed send: %v", err)
	}
	for _, leak := range []string{"sk-secret", "user", "pw@", "#f"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("failed send leaks %q: %v", leak, err)
		}
	}
	// The protocol error keeps the code.
	var me *Error
	if !errors.As(err, &me) || me.Protocol().Code != protocol.ErrModelUnavailable {
		t.Errorf("protocol error = %v", err)
	}

	// Malformed script arguments are refused as invalid arguments.
	for _, a := range []string{`{"prompt": ""}`, `{"prompt": 3}`, `{"prompt": "x", "images": [""]}`} {
		if _, err := ParseAskArgs(raw(a)); !isCode(err, protocol.ErrInvalid) {
			t.Errorf("ask %s: %v", a, err)
		}
	}
	for _, a := range []string{`{"state": "x"}`, `{"state": "x", "questions": {}}`, `{"state": "x", "questions": {"q": {"type": "maybe"}}}`, `{"questions": {"q": {"type": "noul"}}}`} {
		if _, err := ParseDecideArgs(raw(a)); !isCode(err, protocol.ErrInvalid) {
			t.Errorf("decide %s: %v", a, err)
		}
	}
	if _, err := ParseDecideArgs(raw(`{"state": "x", "questions": {"q": {"type": "noul", "instructions": "?"}}, "image": true, "timeout_ms": 5000}`)); err != nil {
		t.Errorf("good decide args: %v", err)
	}
}

func TestErrorNamesRedactedURL(t *testing.T) {
	if got := RedactURL("https://user:secret@gw.example/v1/chat/completions?key=sk-1#k"); got != "https://gw.example/v1/chat/completions" {
		t.Errorf("redacted = %s", got)
	}
	// A key echoed in an error body is scrubbed too.
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad key " + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
	})
	cfg := f.gateway()
	cfg.BaseURL = f.base + "?api_key=sk-q"
	cfg.APIKey = "sk-very-secret"
	_, err := client(t, cfg).Ask(ctx, AskArgs{Prompt: "x"})
	if err == nil || !strings.HasPrefix(err.Error(), DefaultAskModel+" at "+f.srv.URL+"/v1") {
		t.Fatalf("err = %v", err)
	}
	for _, leak := range []string{"sk-very-secret", "sk-q"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error leaks %q: %v", leak, err)
		}
	}

	// A timeout names the requested URL.
	block := make(chan struct{})
	slow := newFake(t, func(http.ResponseWriter, *http.Request) { <-block })
	defer close(block)
	cfg = slow.gateway()
	cfg.TimeoutMS = 50
	_, err = client(t, cfg).Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: raw(`{"q": {"type": "noul"}}`)})
	if err == nil || !strings.HasPrefix(err.Error(), DefaultDecideModel+" request to "+slow.base+"/decisions: no answer within") {
		t.Errorf("timeout: %v", err)
	}
}

func TestPaymentRequired(t *testing.T) {
	for _, body := range []string{`{"error":{"message":"Insufficient credits"}}`, ""} {
		f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(body))
		})
		m := client(t, f.gateway())
		calls := []struct {
			model, path string
			call        func() error
		}{
			{DefaultDecideModel, "/v1/decisions", func() error {
				_, err := m.Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: raw(`{"q": {"type": "noul"}}`)})
				return err
			}},
			{DefaultGroundModel, "/v1/chat/completions", func() error {
				_, err := m.Ground(ctx, GroundArgs{PNG: "UE5H", Target: "OK", Area: Area{Width: 10, Height: 10}})
				return err
			}},
			{DefaultAskModel, "/v1/chat/completions", func() error {
				_, err := m.Ask(ctx, AskArgs{Prompt: "x"})
				return err
			}},
		}
		for _, c := range calls {
			err := c.call()
			want := CreditsUsedUp + " " + c.model + " at " + f.srv.URL + c.path + " answered 402 Payment Required"
			if body != "" {
				want += ": " + body
			}
			want += "; add credit or upgrade the plan, then try again"
			if err == nil || err.Error() != want {
				t.Errorf("err = %v\nwant %s", err, want)
			}
			if !isCode(err, protocol.ErrCreditsExhausted) {
				t.Errorf("code of %v", err)
			}
		}
		// One request per call: no retry and no fallback.
		if n := len(f.requests()); n != 3 {
			t.Errorf("requests = %d, want 3", n)
		}
	}
}

func Test402Message(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte("  This request requires more credits  "))
	})
	_, err := client(t, f.gateway()).Ask(ctx, AskArgs{Prompt: "x"})
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	for _, want := range []string{DefaultAskModel, f.srv.URL + "/v1/chat/completions", "This request requires more credits;", "add credit"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
	if !strings.HasPrefix(msg, "model credits are used up:") {
		t.Errorf("message = %s", msg)
	}
	var me *Error
	if !errors.As(err, &me) {
		t.Fatal("not a model error")
	}
	pe := me.Protocol()
	if pe.Code != protocol.ErrCreditsExhausted || pe.Message != msg || !strings.Contains(pe.Hint, "credit") {
		t.Errorf("protocol error = %+v", pe)
	}
	if len(f.requests()) != 1 {
		t.Errorf("requests = %d", len(f.requests()))
	}
}

func TestSingleEndpointKeySource(t *testing.T) {
	answers := map[string]any{"q": map[string]any{"noul": 1}}
	reply := chatReply(`{"point_2d": [1, 1]}`)
	reply["answers"] = answers
	f := replying(t, reply)
	env := map[string]string{"MY_KEY": "env-key", DefaultKeyEnv: "or-key"}
	getenv := func(k string) string { return env[k] }

	call := func(cfg Config) []seen {
		t.Helper()
		before := len(f.requests())
		m, err := New(cfg, WithGetenv(getenv))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Decide(ctx, DecideArgs{State: raw(`"x"`), Questions: raw(`{"q": {"type": "noul"}}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Ask(ctx, AskArgs{Prompt: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Ground(ctx, GroundArgs{PNG: "UE5H", Target: "t", Area: Area{Width: 4, Height: 4}}); err != nil {
			t.Fatal(err)
		}
		return f.requests()[before:]
	}
	base := f.gateway()

	// The file's key wins over the variable.
	for _, s := range call(base) {
		if s.Auth != "Bearer gw-key" {
			t.Errorf("file key: %s Authorization = %q", s.Path, s.Auth)
		}
	}
	// Else the configured variable; the default variable is OPENROUTER_API_KEY.
	fromEnv := base
	fromEnv.APIKey = ""
	fromEnv.APIKeyEnv = "MY_KEY"
	for _, s := range call(fromEnv) {
		if s.Auth != "Bearer env-key" {
			t.Errorf("env key: Authorization = %q", s.Auth)
		}
	}
	fromEnv.APIKeyEnv = DefaultKeyEnv
	if s := call(fromEnv)[0]; s.Auth != "Bearer or-key" {
		t.Errorf("default env key: Authorization = %q", s.Auth)
	}
	// Else none, and no Authorization header.
	none := base
	none.APIKey = ""
	none.APIKeyEnv = "UNSET_VAR"
	for _, s := range call(none) {
		if s.Auth != "" {
			t.Errorf("no key: Authorization = %q", s.Auth)
		}
	}
	// Every call went to the one configured endpoint.
	for _, s := range f.requests() {
		if s.Path != "/v1/chat/completions" && s.Path != "/v1/decisions" {
			t.Errorf("unexpected path %s", s.Path)
		}
	}
	// The key never prints.
	cfg := base
	cfg.APIKey = "sk-hidden"
	for _, out := range []string{fmt.Sprint(cfg), fmt.Sprintf("%v %+v %#v %s %q", cfg, cfg, cfg, cfg.APIKey, cfg.APIKey)} {
		if strings.Contains(out, "sk-hidden") {
			t.Errorf("key printed: %s", out)
		}
	}
	if b, _ := json.Marshal(cfg); strings.Contains(string(b), "sk-hidden") {
		t.Errorf("key in JSON: %s", b)
	}
	if cfg.APIKey.Reveal() != "sk-hidden" {
		t.Error("Reveal lost the key")
	}
}

func TestUsageTally(t *testing.T) {
	f := replying(t, map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "a cat"}}}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 2},
	})
	m := client(t, f.gateway())
	var run Tally
	rctx := WithTally(ctx, &run)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := m.Ask(rctx, AskArgs{Prompt: "what?", Images: []string{"UE5H"}})
			if err != nil || out.Text != "a cat" {
				t.Errorf("ask = %+v, %v", out, err)
			}
		}()
	}
	wg.Wait()
	if _, err := m.Ask(ctx, AskArgs{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	want := protocol.ModelUsage{Calls: 4, InputTokens: 40, OutputTokens: 8}
	if got := run.Snapshot()[DefaultAskModel]; got != want {
		t.Errorf("run usage = %+v", got)
	}
	if got := m.Usage()[DefaultAskModel]; got.Calls != 5 {
		t.Errorf("total usage = %+v", got)
	}
}
