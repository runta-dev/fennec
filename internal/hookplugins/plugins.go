// Package hookplugins assembles locally registered adapters. The transport never
// learns executable paths, arguments, environment values, or approval scope keys.
package hookplugins

import (
	"fennec/internal/cmuxhooks"
	"fennec/internal/hookbridge"
	"fmt"
	"strings"
)

type Context struct {
	Agent string           `json:"agent,omitempty"`
	Cmux  *cmuxhooks.Local `json:"cmux,omitempty"`
}

func Capture() Context { return Context{Cmux: cmuxhooks.Capture()} }
func CaptureAgent(agent string) Context {
	return Context{Agent: agent, Cmux: cmuxhooks.CaptureAgent(agent)}
}
func (ctx Context) Validate() error {
	if ctx.Agent != "" && ctx.Agent != "codex" && ctx.Agent != "claude" {
		return fmt.Errorf("invalid hook agent")
	}
	if ctx.Cmux == nil {
		return nil
	}
	if !cmuxhooks.ValidContext(ctx.Cmux) {
		return fmt.Errorf("invalid cmux context")
	}
	size := 0
	for _, v := range ctx.Cmux.Env {
		size += len(v)
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid hook environment")
		}
	}
	if size > 128<<10 {
		return fmt.Errorf("hook context too large")
	}
	return nil
}
func Catalog(home string, ctx Context) (map[string]hookbridge.Executor, error) {
	if err := ctx.Validate(); err != nil {
		return nil, err
	}
	result := map[string]hookbridge.Executor{}
	custom, err := hookbridge.ConfigExecutors(home)
	if err != nil {
		return nil, err
	}
	for _, executor := range custom {
		result[executor.Binding().ID] = executor
	}
	if ctx.Agent == "claude" {
		adapters, err := cmuxhooks.ClaudeAdapters(ctx.Cmux)
		if err != nil {
			return nil, err
		}
		for _, adapter := range adapters {
			result[adapter.Binding().ID] = adapter
		}
		return result, nil
	}
	adapters, err := cmuxhooks.Adapters(ctx.Cmux)
	if err != nil {
		return nil, err
	}
	for _, adapter := range adapters {
		result[adapter.Binding().ID] = adapter
	}
	return result, nil
}
func Prepare(home, session, agent string, args []string, ctx Context) ([]string, []hookbridge.Binding, []hookbridge.ClaudeSettings, error) {
	if agent != "claude" {
		args, bindings, err := Rewrite(home, session, agent, args, ctx)
		return args, bindings, nil, err
	}
	adapters, err := cmuxhooks.ClaudeAdapters(ctx.Cmux)
	if err != nil {
		return nil, nil, nil, err
	}
	return hookbridge.PrepareClaude(args, session, func(event, command string) (*hookbridge.Binding, error) {
		key, err := cmuxhooks.MatchClaude(event, command)
		if err != nil {
			return nil, err
		}
		if key == "" {
			return nil, nil
		}
		adapter := adapters[key]
		if adapter == nil {
			return nil, fmt.Errorf("cmux context is unavailable; restart this cmux terminal")
		}
		binding := adapter.Binding()
		return &binding, nil
	})
}
func Rewrite(home, session, agent string, args []string, ctx Context) ([]string, []hookbridge.Binding, error) {
	if agent != "codex" {
		return append([]string(nil), args...), nil, nil
	}
	cfg, err := hookbridge.LoadConfig(home)
	if err != nil {
		return nil, nil, err
	}
	custom, err := hookbridge.ConfigExecutors(home)
	if err != nil {
		return nil, nil, err
	}
	adapters, err := cmuxhooks.Adapters(ctx.Cmux)
	if err != nil {
		return nil, nil, err
	}
	return hookbridge.RewriteCodex(args, session, func(event, command string) (*hookbridge.Binding, error) {
		for _, definition := range cfg.Hooks {
			if definition.Agent == agent && definition.Event == event && definition.Command == command {
				if definition.Location == "sandbox" {
					return nil, nil
				}
				executor := custom[definition.Name]
				if executor == nil {
					return nil, fmt.Errorf("hook %s is unavailable", definition.Name)
				}
				binding := executor.Binding()
				return &binding, nil
			}
		}
		if ctx.Cmux == nil {
			return nil, nil
		}
		matched, err := cmuxhooks.Match(command, home, event)
		if err != nil {
			return nil, err
		}
		if matched == "" {
			return nil, nil
		}
		binding := adapters[matched].Binding()
		return &binding, nil
	})
}
