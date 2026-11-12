package models

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fennec/cua/internal/protocol"
)

// Roles as reported in Served.
const (
	RoleDecide = "decide"
	RoleVerify = "verify"
	RoleGround = "ground"
	RoleAsk    = "ask"
)

const (
	askMaxTokens    = 400
	groundMaxTokens = 100
	decideMaxTokens = 800

	// CreditsUsedUp starts the message of a 402 from the endpoint.
	CreditsUsedUp = "model credits are used up:"

	statusSnippet = 300
	replySnippet  = 120
	maxReplyBytes = 16 << 20
)

// Error is a model call failure. Its message names the model and the endpoint
// (without credentials, query or fragment) and never the key.
type Error struct {
	Code    protocol.ErrorCode
	Message string
}

func (e *Error) Error() string { return e.Message }

// Protocol converts the failure into the tool error shape.
func (e *Error) Protocol() *protocol.Error {
	pe := &protocol.Error{Code: e.Code, Message: e.Message}
	if e.Code == protocol.ErrCreditsExhausted {
		pe.Hint = "add credit to the model account or upgrade its plan, then try again"
	}
	return pe
}

func invalid(format string, a ...any) error {
	return &Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf(format, a...)}
}

// Client calls the configured models. It is safe for concurrent use.
type Client struct {
	cfg     Config
	key     Secret
	http    *http.Client
	getenv  func(string) string
	chatURL string
	total   Tally
}

// Option adjusts a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client; the default is a fresh http.Client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithGetenv sets how the key variable is read; the default is os.Getenv.
func WithGetenv(f func(string) string) Option { return func(c *Client) { c.getenv = f } }

// New validates cfg and resolves its key once.
func New(cfg Config, opts ...Option) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, http: &http.Client{}}
	for _, o := range opts {
		o(c)
	}
	c.key = cfg.Key(c.getenv)
	c.chatURL = strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	return c, nil
}

// HasKey reports whether a key is configured, without revealing it.
func (c *Client) HasKey() bool { return c.key != "" }

// Usage is every model call's usage since the client started, per model.
func (c *Client) Usage() protocol.Usage { return c.total.Snapshot() }

func (c *Client) timeout(capMS int) time.Duration {
	ms := c.cfg.TimeoutMS
	if capMS > 0 && capMS < ms {
		ms = capMS
	}
	return time.Duration(ms) * time.Millisecond
}

func (c *Client) record(ctx context.Context, u Usage) {
	c.total.Add(u)
	if t, ok := ctx.Value(tallyKey{}).(*Tally); ok && t != nil {
		t.Add(u)
	}
}

// fail builds a model error with the key scrubbed from it.
func (c *Client) fail(code protocol.ErrorCode, format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	if k := c.key.Reveal(); len(k) >= 4 {
		msg = strings.ReplaceAll(msg, k, redacted)
	}
	return &Error{Code: code, Message: msg}
}

// post sends one JSON POST and returns the reply body and "<model> at <url>",
// the URL being the one that answered after redirects.
func (c *Client) post(ctx context.Context, target, model string, body any, timeout time.Duration) ([]byte, string, error) {
	data, err := encodeJSON(body)
	if err != nil {
		return nil, "", invalid("%s: cannot encode the request: %v", model, err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return nil, "", c.fail(protocol.ErrModelUnavailable, "%s request to %s: not a valid request URL", model, RedactURL(target))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key.Reveal())
	}
	res, err := c.http.Do(req)
	if err != nil {
		// A *url.Error prints the URL it last tried, query included; name the
		// requested one, redacted, and keep only the cause.
		cause := err
		var ue *url.Error
		if errors.As(err, &ue) {
			cause = ue.Err
		}
		why := cause.Error()
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			why = fmt.Sprintf("no answer within %s", timeout)
		case errors.Is(err, context.Canceled):
			why = "canceled"
		}
		return nil, "", c.fail(protocol.ErrModelUnavailable, "%s request to %s: %s", model, RedactURL(target), why)
	}
	defer res.Body.Close()
	answered := target
	if res.Request != nil && res.Request.URL != nil {
		answered = res.Request.URL.String()
	}
	at := model + " at " + RedactURL(answered)
	text, err := io.ReadAll(io.LimitReader(res.Body, maxReplyBytes))
	if err != nil {
		return nil, at, c.fail(protocol.ErrModelUnavailable, "%s: reading the reply: %v", at, err)
	}
	if res.StatusCode == http.StatusPaymentRequired {
		why := snippet(strings.TrimSpace(string(text)), statusSnippet)
		if why != "" {
			why = ": " + why
		}
		return nil, at, c.fail(protocol.ErrCreditsExhausted,
			"%s %s answered %s%s; add credit or upgrade the plan, then try again", CreditsUsedUp, at, res.Status, why)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, at, c.fail(protocol.ErrModelUnavailable, "%s answered %s: %s", at, res.Status, snippet(string(text), statusSnippet))
	}
	return text, at, nil
}

// decodeReply decodes a reply body that must be one JSON object.
func (c *Client) decodeReply(at string, text []byte, v any) error {
	if t := bytes.TrimSpace(text); len(t) == 0 || t[0] != '{' {
		return c.fail(protocol.ErrModelUnavailable, "%s: the reply is not a JSON object: %s", at, snippet(string(text), replySnippet))
	}
	if err := json.Unmarshal(text, v); err != nil {
		return c.fail(protocol.ErrModelUnavailable, "%s: unexpected reply: %v: %s", at, err, snippet(string(text), replySnippet))
	}
	return nil
}

// RedactURL drops the user, password, query and fragment of raw, so a key in
// a URL never reaches a message.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable URL)"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

func snippet(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// ---------------------------------------------------------------------------
// Usage

// Usage is the token usage of one model call.
type Usage struct {
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CachedTokens int    `json:"cached_tokens"`
}

// usageOf reads a reply's usage block in the chat field names
// (prompt_tokens, completion_tokens, prompt_tokens_details.cached_tokens) or
// the decisions ones (input_tokens, output_tokens, input_tokens_details).
func usageOf(model string, u map[string]any) Usage {
	num := func(m map[string]any, keys ...string) int {
		for _, k := range keys {
			if f, ok := m[k].(float64); ok && f >= 0 {
				return int(f)
			}
		}
		return 0
	}
	cached := 0
	for _, k := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if d, ok := u[k].(map[string]any); ok {
			if n := num(d, "cached_tokens"); n > 0 {
				cached = n
				break
			}
		}
	}
	return Usage{
		Model:        model,
		InputTokens:  num(u, "prompt_tokens", "input_tokens"),
		OutputTokens: num(u, "completion_tokens", "output_tokens"),
		CachedTokens: cached,
	}
}

// Tally sums model usage per model. The zero value is ready; it is safe for
// concurrent use.
type Tally struct {
	mu sync.Mutex
	m  protocol.Usage
}

// Add counts one call.
func (t *Tally) Add(u Usage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = protocol.Usage{}
	}
	mu := t.m[u.Model]
	mu.Calls++
	mu.InputTokens += u.InputTokens
	mu.OutputTokens += u.OutputTokens
	mu.CachedTokens += u.CachedTokens
	t.m[u.Model] = mu
}

// Snapshot copies the sums.
func (t *Tally) Snapshot() protocol.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(protocol.Usage, len(t.m))
	for k, v := range t.m {
		out[k] = v
	}
	return out
}

type tallyKey struct{}

// WithTally returns a context whose model calls are also counted in t, such
// as the usage of one script run or plan.
func WithTally(ctx context.Context, t *Tally) context.Context {
	return context.WithValue(ctx, tallyKey{}, t)
}
