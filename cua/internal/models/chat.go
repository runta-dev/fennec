package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"fennec/cua/internal/protocol"
)

const pngDataURL = "data:image/png;base64,"

// Part is one content part of the single user message of a chat request:
// text, or a PNG given as base64.
type Part struct {
	Text string
	PNG  string // base64; non-empty makes this an image part
}

// TextPart is a text part.
func TextPart(s string) Part { return Part{Text: s} }

// PNGPart is an image part holding a base64 PNG.
func PNGPart(b64 string) Part { return Part{PNG: b64} }

type wireText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type wireImage struct {
	Type     string `json:"type"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

// MarshalJSON writes {type: text, text} or {type: image_url, image_url: {url:
// data:image/png;base64,...}}.
func (p Part) MarshalJSON() ([]byte, error) {
	if p.PNG != "" {
		w := wireImage{Type: "image_url"}
		w.ImageURL.URL = pngDataURL + p.PNG
		return encodeJSON(w)
	}
	return encodeJSON(wireText{Type: "text", Text: p.Text})
}

// StateParts turns a judgment state into content parts. A list gives one part
// per item, anything else one part: a string is text, an image_url part with
// a PNG data URL is an image, and any other value is text holding its compact
// JSON.
func StateParts(state json.RawMessage) ([]Part, error) {
	t := bytes.TrimSpace(state)
	if len(t) == 0 {
		return nil, invalid("state is missing")
	}
	var items []json.RawMessage
	if t[0] == '[' {
		if err := json.Unmarshal(t, &items); err != nil {
			return nil, invalid("state is not valid JSON: %v", err)
		}
	} else {
		if !json.Valid(t) {
			return nil, invalid("state is not valid JSON")
		}
		items = []json.RawMessage{t}
	}
	parts := make([]Part, 0, len(items))
	for _, it := range items {
		parts = append(parts, statePart(it))
	}
	return parts, nil
}

func statePart(raw json.RawMessage) Part {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return TextPart(s)
	}
	var img struct {
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &img) == nil {
		if b64, ok := strings.CutPrefix(img.ImageURL.URL, pngDataURL); ok && b64 != "" {
			return PNGPart(b64)
		}
	}
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return TextPart(string(raw))
	}
	return TextPart(b.String())
}

type chatBody struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	Messages  []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content []Part `json:"content"`
}

type completion struct {
	ID      *string `json:"id"`
	Model   *string `json:"model"`
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage map[string]any `json:"usage"`
}

type reply struct {
	text   string
	served protocol.Served
	usage  Usage
}

// complete sends parts as one user message and answers the reply's text; a
// reply without text is an error.
func (c *Client) complete(ctx context.Context, role, model string, parts []Part, maxTokens int, timeout time.Duration) (reply, error) {
	body := chatBody{Model: model, MaxTokens: maxTokens, Messages: []chatMessage{{Role: "user", Content: parts}}}
	text, at, err := c.post(ctx, c.chatURL, model, body, timeout)
	if err != nil {
		return reply{}, err
	}
	var r completion
	if err := c.decodeReply(at, text, &r); err != nil {
		return reply{}, err
	}
	u := usageOf(model, r.Usage)
	c.record(ctx, u)
	out := reply{served: served(role, model, r.Model, r.ID), usage: u}
	if len(r.Choices) > 0 {
		out.text = contentText(r.Choices[0].Message.Content)
	}
	if strings.TrimSpace(out.text) == "" {
		return reply{}, c.fail(protocol.ErrModelUnavailable, "%s: the reply holds no message text", at)
	}
	return out, nil
}

// contentText reads a message content given as a string or as a list of text
// parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func served(role, model string, build, id *string) protocol.Served {
	s := protocol.Served{Role: role, Model: model}
	if build != nil {
		s.Build = *build
	}
	if id != nil {
		s.ID = *id
	}
	return s
}

// ---------------------------------------------------------------------------
// ask

// AskArgs asks the ask model about PNG images.
type AskArgs struct {
	Prompt string   `json:"prompt"`
	Images []string `json:"images,omitempty"` // base64 PNGs
}

// Asked is the ask model's answer.
type Asked struct {
	Text   string          `json:"text"`
	Served protocol.Served `json:"served"`
	Usage  Usage           `json:"usage"`
}

func (a AskArgs) validate() error {
	if strings.TrimSpace(a.Prompt) == "" {
		return invalid("ask: prompt is empty")
	}
	for i, img := range a.Images {
		if img == "" {
			return invalid("ask: images[%d] is empty", i)
		}
	}
	return nil
}

// Ask sends the prompt, then the images, to the ask model.
func (c *Client) Ask(ctx context.Context, args AskArgs) (*Asked, error) {
	if err := args.validate(); err != nil {
		return nil, err
	}
	parts := []Part{TextPart(args.Prompt)}
	for _, img := range args.Images {
		parts = append(parts, PNGPart(img))
	}
	r, err := c.complete(ctx, RoleAsk, c.cfg.Models.Ask, parts, askMaxTokens, c.timeout(0))
	if err != nil {
		return nil, err
	}
	return &Asked{Text: r.text, Served: r.served, Usage: r.usage}, nil
}

// ---------------------------------------------------------------------------
// ground

// groundScale is the range of a grounding answer across and down the image.
const groundScale = 1000.0

// Area is the desk-coordinate rectangle an image shows.
type Area struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// GroundArgs asks for the point of a described element in a PNG that shows
// Area.
type GroundArgs struct {
	PNG    string `json:"png_b64"`
	Target string `json:"target"`
	Area   Area   `json:"area"`
}

// Grounded is the element's point in desk coordinates.
type Grounded struct {
	X      int             `json:"x"`
	Y      int             `json:"y"`
	Served protocol.Served `json:"served"`
	Usage  Usage           `json:"usage"`
}

func (g GroundArgs) validate() error {
	if g.PNG == "" {
		return invalid("ground: png_b64 is empty")
	}
	if strings.TrimSpace(g.Target) == "" {
		return invalid("ground: target is empty")
	}
	a := g.Area
	for _, v := range []float64{a.Left, a.Top, a.Width, a.Height} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return invalid("ground: area holds a value that is not a finite number")
		}
	}
	if a.Width < 1 || a.Height < 1 {
		return invalid("ground: area width and height must be at least 1")
	}
	return nil
}

func groundPrompt(target string) string {
	return fmt.Sprintf("Find the %s in this screenshot. Reply with only a JSON object of the form {\"point_2d\": [x, y]} giving the center of that element, where x runs from 0 at the left edge to 1000 at the right edge of the image and y from 0 at the top to 1000 at the bottom.", target)
}

// Ground sends the image, then a prompt asking for one point on a 0-1000
// scale, to the ground model, and maps the answer onto the area.
func (c *Client) Ground(ctx context.Context, args GroundArgs) (*Grounded, error) {
	if err := args.validate(); err != nil {
		return nil, err
	}
	parts := []Part{PNGPart(args.PNG), TextPart(groundPrompt(args.Target))}
	r, err := c.complete(ctx, RoleGround, c.cfg.Models.Ground, parts, groundMaxTokens, c.timeout(0))
	if err != nil {
		return nil, err
	}
	x, y, err := GroundPoint(r.text, args.Target, args.Area)
	if err != nil {
		return nil, err
	}
	return &Grounded{X: x, Y: y, Served: r.served, Usage: r.usage}, nil
}

// GroundPoint reads the first "x, y" number pair of a grounding reply, on the
// 0-1000 scale, and maps it onto area whatever the image's pixel size: 0 is
// the area's first pixel and 1000 its last (left+width-1).
func GroundPoint(text, target string, area Area) (int, int, error) {
	nums := scanNumbers(text)
	for i := 0; i+1 < len(nums); i++ {
		a, b := nums[i], nums[i+1]
		if strings.TrimSpace(text[a.end:b.start]) != "," {
			continue
		}
		if a.v < 0 || a.v > groundScale || b.v < 0 || b.v > groundScale {
			return 0, 0, &Error{Code: protocol.ErrModelUnavailable, Message: fmt.Sprintf(
				"ground: the model placed %s at (%s, %s), outside the 0 to 1000 scale", target, fmtNum(a.v), fmtNum(b.v))}
		}
		return place(area.Left, area.Width, a.v), place(area.Top, area.Height, b.v), nil
	}
	return 0, 0, &Error{Code: protocol.ErrModelUnavailable, Message: fmt.Sprintf(
		"ground: the reply gives no point for %s: %s", target, snippet(text, replySnippet))}
}

func place(start, length, at float64) int {
	first := math.Round(start)
	last := math.Round(start+length) - 1
	v := math.Round(start + at/groundScale*length)
	return int(math.Max(first, math.Min(v, last)))
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

type number struct {
	start, end int
	v          float64
}

// scanNumbers finds the decimal numbers of text that do not continue a word,
// such as the 2 of point_2d.
func scanNumbers(text string) []number {
	var out []number
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }
	wordByte := func(b byte) bool { return isDigit(b) || b == '_' || (b|0x20 >= 'a' && b|0x20 <= 'z') }
	for i := 0; i < len(text); {
		start := i
		j := i
		if text[j] == '-' && j+1 < len(text) && isDigit(text[j+1]) {
			j++
		}
		if !isDigit(text[j]) || (start > 0 && wordByte(text[start-1])) {
			i = start + 1
			continue
		}
		for j < len(text) && isDigit(text[j]) {
			j++
		}
		if j+1 < len(text) && text[j] == '.' && isDigit(text[j+1]) {
			j++
			for j < len(text) && isDigit(text[j]) {
				j++
			}
		}
		if v, err := strconv.ParseFloat(text[start:j], 64); err == nil {
			out = append(out, number{start: start, end: j, v: v})
		}
		i = j
	}
	return out
}
