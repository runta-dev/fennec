package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/localssh"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type onlineDelivery struct {
	Generation string            `json:"generation"`
	Since      int64             `json:"since"`
	Targets    map[string]string `json:"targets"`
}

// The outbox survives resident replacement. Persist an attempt before touching
// input: an uncertain receipt must never become an automatic duplicate prompt.
// Each call sends at most one message; blocked terminals remain pending and a
// newer recovery replaces obsolete queued messages.
func (r *LocalRuntime) notifyMacOnline(ctx context.Context, h localssh.LinkHealth) (json.RawMessage, error) {
	return r.deliverMacOnline(ctx, h, func() error { return localssh.ProbeMac(ctx, r.Home, localssh.LinkHealth{Device: h.Device}) })
}

func (r *LocalRuntime) deliverMacOnline(ctx context.Context, h localssh.LinkHealth, probe func() error) (json.RawMessage, error) {
	base := filepath.Join(r.Home, ".local/fennec/notifications")
	if err := os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(base, h.Device+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(base, h.Device+".json")
	state := onlineDelivery{}
	if raw, e := os.ReadFile(path); e == nil {
		if json.Unmarshal(raw, &state) != nil {
			return nil, fmt.Errorf("invalid notification outbox")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if h.Since < state.Since {
		return json.RawMessage(`{"obsolete":1}`), nil
	}
	listing, err := r.Query(ctx, "terminal.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result struct {
		Terminals []LocalTerminal `json:"terminals"`
	}
	if json.Unmarshal(listing, &result) != nil {
		return nil, fmt.Errorf("invalid notification targets")
	}
	if state.Generation != h.Generation {
		state = onlineDelivery{Generation: h.Generation, Since: h.Since, Targets: map[string]string{}}
		for _, t := range result.Terminals {
			if onlineTarget(t) {
				state.Targets[t.Address] = "pending"
			}
		}
	}
	save := func() error {
		raw, e := json.Marshal(state)
		if e != nil {
			return e
		}
		if e = os.WriteFile(path+".next", raw, 0600); e != nil {
			return e
		}
		return os.Rename(path+".next", path)
	}
	if err = save(); err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for _, t := range result.Terminals {
		present[t.Address] = true
	}
	for address, status := range state.Targets {
		if status == "pending" && !present[address] {
			state.Targets[address] = "finished"
		}
	}
	for _, t := range result.Terminals {
		if state.Targets[t.Address] != "pending" {
			continue
		}
		if !onlineTarget(t) {
			state.Targets[t.Address] = "finished"
			continue
		}
		var status struct {
			State string `json:"state"`
			App   string `json:"app"`
		}
		if json.Unmarshal(t.Status, &status) != nil || (status.State != "idle" && status.State != "working") || !notificationAgent(reportedAgent(status.App)) {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Recheck immediately before input, not just at the beginning of a batch.
		if err = probe(); err != nil {
			return nil, err
		}
		state.Targets[t.Address] = "uncertain"
		if err = save(); err != nil {
			return nil, err
		}
		alias := localssh.RegisteredAlias(r.Home, h.Device)
		text := fmt.Sprintf("<fennec-event type=\"mac-online\" device=\"%s\" generation=\"%s\">\nMac is back online. Mac Relay has recovered and cloud-to-Mac connectivity has been verified.\nSSH alias: %s\nOperations previously blocked by Mac connectivity can now be retried.\n</fennec-event>", h.Device, h.Generation, alias)
		_, sendErr := r.Query(ctx, "terminal.send", map[string]any{"terminal": t.Address, "text": text, "force": false, "if": status.State})
		if sendErr == nil {
			state.Targets[t.Address] = "delivered"
		} else {
			var refusal *herdrRefusal
			if errors.As(sendErr, &refusal) && (refusal.Data.Kind == "blocked" || refusal.Data.Kind == "status_mismatch") {
				state.Targets[t.Address] = "pending"
			}
		}
		if err = save(); err != nil {
			return nil, err
		}
		break
	}
	if err = save(); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, s := range state.Targets {
		counts[s]++
	}
	return json.Marshal(counts)
}

func onlineTarget(t LocalTerminal) bool {
	return t.State == "live" && sessionIDPattern.MatchString(t.Labels["fennec.session"]) &&
		notificationAgent(t.Labels["fennec.agent"])
}

// reportedAgent maps the name an agent gives in its OSC 7501 status report to
// the fennec.agent name: Claude Code reports "claude-code" (herdr's
// programs.json lists the same aliases).
func reportedAgent(app string) string {
	if agent, ok := map[string]string{"claude-code": "claude", "agent": "cursor", "cursor-agent": "cursor", "grok-build": "grok", "kimi-code": "kimi", "kimicodecli": "kimi"}[app]; ok {
		return agent
	}
	return app
}

func notificationAgent(agent string) bool {
	return map[string]bool{"codex": true, "claude": true, "pi": true, "opencode": true, "kimi": true, "cursor": true, "grok": true}[agent]
}
