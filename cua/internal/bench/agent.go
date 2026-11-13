// Package bench runs computer-use benchmark tasks: one LLM agent loop that
// drives any backend through its MCP tools, so cua-driver, shuttle and
// fennec-cua are compared with the same model, prompt and task.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fennec/cua/internal/mcpclient"
)

// Model is an OpenAI-compatible chat completions endpoint.
type Model struct {
	BaseURL string // e.g. http://127.0.0.1:3425/v1
	Name    string // e.g. zai/glm-5.3-flashx
	APIKey  string
	HTTP    *http.Client
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (m Model) chat(ctx context.Context, msgs []chatMessage, tools []chatTool) (*chatResponse, error) {
	body, err := json.Marshal(map[string]any{"model": m.Name, "messages": msgs, "tools": tools, "max_tokens": 8192})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(m.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	if m.APIKey != "" {
		req.Header.Set("authorization", "Bearer "+m.APIKey)
	}
	hc := m.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("model reply %d: %.300s", resp.StatusCode, data)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("model: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("model reply %d without choices: %.300s", resp.StatusCode, data)
	}
	return &out, nil
}

// Step is one turn of a run's transcript.
type Step struct {
	Tool    string `json:"tool,omitempty"`
	Args    string `json:"args,omitempty"`
	Result  string `json:"result,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Images  int    `json:"images,omitempty"`
	Text    string `json:"text,omitempty"` // the model's own words
	Ms      int64  `json:"ms"`
}

// Transcript is what one agent run did.
type Transcript struct {
	Steps        []Step `json:"steps"`
	Turns        int    `json:"turns"`
	ToolCalls    int    `json:"tool_calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Final        string `json:"final"`
	Stop         string `json:"stop"` // done, max_turns, timeout, context_overflow, model_error, backend_error
}

// AgentOptions bound a run.
type AgentOptions struct {
	MaxTurns int
	// KeepImages is how many of the latest screenshots stay in context.
	KeepImages int
	// Instructions are the MCP server's own instructions from its
	// initialize result; MCP hosts add them to the system prompt.
	Instructions string
}

const systemPrompt = `You operate a macOS computer through the tools you are given. Complete the user's task on that computer, then reply with a short final message that starts with DONE, or with FAIL and the reason if the task cannot be done. Look before you act, check that each action had the effect you wanted, and do not ask the user questions: nobody will answer.`

// Run lets the model complete task with the backend's tools.
func Run(ctx context.Context, m Model, backend *mcpclient.Client, task string, opt AgentOptions) Transcript {
	if opt.MaxTurns == 0 {
		opt.MaxTurns = 60
	}
	if opt.KeepImages == 0 {
		opt.KeepImages = 2
	}
	var tr Transcript
	list, err := backend.ToolDefs(ctx)
	if err != nil {
		tr.Stop, tr.Final = "backend_error", err.Error()
		return tr
	}
	tools := make([]chatTool, 0, len(list))
	for _, d := range list {
		var t chatTool
		t.Type = "function"
		t.Function.Name = d.Name
		t.Function.Description = d.Description
		t.Function.Parameters = d.InputSchema
		if len(t.Function.Parameters) == 0 {
			t.Function.Parameters = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, t)
	}
	system := systemPrompt
	if opt.Instructions != "" {
		system += "\n\nInstructions from the tool server:\n" + opt.Instructions
	}
	msgs := []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: task}}
	for tr.Turns < opt.MaxTurns {
		if ctx.Err() != nil {
			tr.Stop = "timeout"
			return tr
		}
		tr.Turns++
		start := time.Now()
		resp, err := m.chatRetry(ctx, trimImages(msgs, opt.KeepImages), tools)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				tr.Stop = "timeout"
			case contextOverflow(err):
				// The conversation outgrew the model: the run's own doing.
				tr.Stop, tr.Final = "context_overflow", err.Error()
			default:
				tr.Stop, tr.Final = "model_error", err.Error()
			}
			return tr
		}
		tr.InputTokens += resp.Usage.PromptTokens
		tr.OutputTokens += resp.Usage.CompletionTokens
		msg := resp.Choices[0].Message
		text, _ := msg.Content.(string)
		if len(msg.ToolCalls) == 0 {
			tr.Steps = append(tr.Steps, Step{Text: text, Ms: time.Since(start).Milliseconds()})
			tr.Final, tr.Stop = text, "done"
			return tr
		}
		msgs = append(msgs, chatMessage{Role: "assistant", Content: text, ToolCalls: msg.ToolCalls})
		var images []map[string]any
		for _, call := range msg.ToolCalls {
			tr.ToolCalls++
			t0 := time.Now()
			var args map[string]any
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
			res, err := backend.Tool(ctx, call.Function.Name, args)
			st := Step{Tool: call.Function.Name, Args: call.Function.Arguments, Text: text}
			reply := ""
			if err != nil {
				reply, st.IsError = "error: "+err.Error(), true
			} else {
				reply, st.IsError = res.Text(), res.IsError
				for _, c := range res.Content {
					if c.Type == "image" && c.Data != "" {
						images = append(images, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + c.MimeType + ";base64," + c.Data}})
						st.Images++
					}
				}
			}
			if reply == "" {
				reply = "(no text)"
			}
			st.Result = clip(reply, 4000)
			st.Ms = time.Since(t0).Milliseconds()
			tr.Steps = append(tr.Steps, st)
			msgs = append(msgs, chatMessage{Role: "tool", ToolCallID: call.ID, Content: clip(reply, 100000)})
		}
		// Tool messages carry text only; screenshots follow as a user turn.
		if len(images) > 0 {
			parts := append([]map[string]any{{"type": "text", "text": "Screenshot(s) returned by the tool call(s) above."}}, images...)
			msgs = append(msgs, chatMessage{Role: "user", Content: parts})
		}
	}
	tr.Stop = "max_turns"
	return tr
}

// trimImages keeps only the latest keep image-bearing user turns, replacing
// older ones with a note, so context stays bounded.
func trimImages(msgs []chatMessage, keep int) []chatMessage {
	out := make([]chatMessage, len(msgs))
	copy(out, msgs)
	seen := 0
	for i := len(out) - 1; i >= 0; i-- {
		parts, ok := out[i].Content.([]map[string]any)
		if !ok || out[i].Role != "user" {
			continue
		}
		seen++
		if seen > keep {
			out[i].Content = "(an earlier screenshot was removed to save context)"
			_ = parts
		}
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d more bytes)", len(s)-n)
}

// modelRetryDelays are the waits between attempts of one model call; a
// gateway hiccup should not end a run.
var modelRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

// chatRetry calls the model, retrying failures other than a conversation
// that is too long for it.
func (m Model) chatRetry(ctx context.Context, msgs []chatMessage, tools []chatTool) (*chatResponse, error) {
	for i := 0; ; i++ {
		resp, err := m.chat(ctx, msgs, tools)
		if err == nil || ctx.Err() != nil || contextOverflow(err) || i == len(modelRetryDelays) {
			return resp, err
		}
		select {
		case <-time.After(modelRetryDelays[i]):
		case <-ctx.Done():
			return nil, err
		}
	}
}

// contextOverflow reports whether a model error says the request is longer
// than the model's context window.
func contextOverflow(err error) bool {
	s := strings.ToLower(err.Error())
	for _, k := range []string{"context length", "context_length", "maximum context", "too many tokens", "prompt is too long", "context window", "input is too long", "exceeds the context"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
