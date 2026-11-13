package mcpclient

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// resultMark starts the text part that carries an engine tool's answer
// inside a js result.
const resultMark = "fennec-cua engine result: "

// EngineCode is the js cell that calls one fennec-cua engine tool (observe,
// act, wait, run, flow, status, or a plugin or service tool) through the
// REPL's fennec.call and writes its answer, marked, as JSON; a capture comes
// back as an image too.
func EngineCode(name string, args any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	if raw, ok := args.(json.RawMessage); ok && len(strings.TrimSpace(string(raw))) == 0 {
		args = map[string]any{}
	}
	a, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	n, _ := json.Marshal(name)
	return `{
  let out;
  try {
    out = { result: await fennec.call(` + string(n) + `, ` + string(a) + `) };
  } catch (e) {
    const err = { code: e.code || "internal", message: String(e.message || e).replace(/^[a-z_]+: /, "") };
    if (e.hint) err.hint = e.hint;
    out = { error: err };
    if (e.report) out.report = e.report;
  }
  nodeRepl.write(` + "`" + resultMark + "`" + ` + JSON.stringify(out));
  const img = out.result && out.result.image;
  if (img && img.data) await nodeRepl.emitImage({ data: img.data, mime: img.mime });
}`, nil
}

// replError reads a js call's own failure ("Error: busy: ...").
var replError = regexp.MustCompile(`^Error: ([a-z_]+): ([\s\S]*)$`)

// EngineResult turns a js result made by EngineCode back into the answer
// the engine tool gives: its JSON text (an {"error": ...} object, with the
// report when there is one, on failure), isError, and any capture.
func EngineResult(r ToolResult) ToolResult {
	out := ToolResult{}
	for _, c := range r.Content {
		if c.Type == "image" {
			out.Content = append(out.Content, c)
		}
	}
	for _, c := range r.Content {
		if c.Type != "text" || !strings.HasPrefix(c.Text, resultMark) {
			continue
		}
		var got struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
			Report json.RawMessage `json:"report"`
		}
		body := strings.TrimPrefix(c.Text, resultMark)
		if json.Unmarshal([]byte(body), &got) != nil {
			break
		}
		text := string(got.Result)
		if len(got.Error) > 0 {
			out.IsError = true
			env := map[string]json.RawMessage{"error": got.Error}
			if len(got.Report) > 0 {
				env["report"] = got.Report
			}
			data, _ := json.Marshal(env)
			text = string(data)
		}
		if text == "" {
			text = "null"
		}
		out.Structured = json.RawMessage(text)
		out.Content = append([]Content{{Type: "text", Text: text}}, out.Content...)
		return out
	}
	// The cell never answered: the call itself failed (busy, stopped,
	// timed out) before or while it ran.
	msg := ""
	for i := len(r.Content) - 1; i >= 0; i-- {
		if r.Content[i].Type == "text" {
			msg = r.Content[i].Text
			break
		}
	}
	code, text := "internal", strings.TrimPrefix(msg, "Error: ")
	if m := replError.FindStringSubmatch(msg); m != nil {
		code, text = m[1], m[2]
	}
	data, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": text}})
	out.IsError = true
	out.Structured = data
	out.Content = append([]Content{{Type: "text", Text: string(data)}}, out.Content...)
	return out
}

// EngineTool calls a fennec-cua engine tool through the js REPL: the way
// the CLI and harnesses reach observe, act and the rest, which the MCP
// surface does not offer by name.
func (c *Client) EngineTool(ctx context.Context, name string, args any) (ToolResult, error) {
	code, err := EngineCode(name, args)
	if err != nil {
		return ToolResult{}, err
	}
	r, err := c.Tool(ctx, "js", map[string]any{"code": code, "title": "Calling " + name})
	if err != nil {
		return r, err
	}
	return EngineResult(r), nil
}
