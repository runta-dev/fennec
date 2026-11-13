package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"fennec/cua/internal/driver"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/protocol"
)

// driverApps lists apps through the driver's list_apps; fields the driver
// leaves out stay empty.
func driverApps(c *driver.Client) engine.AppLister {
	if c == nil {
		return nil
	}
	return func(ctx context.Context) ([]protocol.AppInfo, error) {
		raw, err := c.Call(ctx, "apps", "list_apps", map[string]any{})
		if err != nil {
			return nil, err
		}
		return parseApps(raw), nil
	}
}

// parseApps reads list_apps' answer: a list, or an object holding one
// under apps.
func parseApps(raw json.RawMessage) []protocol.AppInfo {
	type entry struct {
		Name       string `json:"name"`
		AppName    string `json:"app_name"`
		PID        int    `json:"pid"`
		BundleID   string `json:"bundle_id"`
		LaunchPath string `json:"launch_path"`
		Path       string `json:"path"`
		Running    *bool  `json:"running"`
		IsRunning  *bool  `json:"is_running"`
		LastUsed   string `json:"last_used"`
		UseCount   int    `json:"use_count"`
	}
	var list []entry
	if json.Unmarshal(raw, &list) != nil {
		var wrapped struct {
			Apps []entry `json:"apps"`
		}
		if json.Unmarshal(raw, &wrapped) != nil {
			return nil
		}
		list = wrapped.Apps
	}
	out := make([]protocol.AppInfo, 0, len(list))
	for _, e := range list {
		name := e.Name
		if name == "" {
			name = e.AppName
		}
		if name == "" {
			continue
		}
		path := e.LaunchPath
		if path == "" {
			path = e.Path
		}
		running := e.PID > 0
		if e.Running != nil {
			running = *e.Running
		} else if e.IsRunning != nil {
			running = *e.IsRunning
		}
		out = append(out, protocol.AppInfo{Name: name, BundleID: e.BundleID, Path: path, PID: e.PID, Running: running, LastUsed: e.LastUsed, UseCount: e.UseCount})
	}
	return out
}

// pasteboard is the general pasteboard's text through pbpaste and pbcopy.
type pasteboard struct{}

func (pasteboard) Text(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "/usr/bin/pbpaste").Output()
	return string(out), err
}

func (pasteboard) SetText(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/pbcopy")
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd.Run()
}
