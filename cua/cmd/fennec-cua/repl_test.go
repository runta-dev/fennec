package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"fennec/cua/internal/driver"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/protocol"
)

// listAppsReply is cua-driver 0.47.14's socket reply to list_apps on a
// fresh benchmark VM (trimmed): an MCP tool result with the apps under
// structuredContent and a text summary in content.
const listAppsReply = `{"ok":true,"result":{"content":[{"text":"Found 3 app(s): 1 running, 2 installed-not-running.\n- Finder (pid 351) [com.apple.finder]","type":"text"}],"structuredContent":{"apps":[` +
	`{"active":false,"bundle_id":"com.apple.finder","kind":"desktop","launch_path":"/System/Library/CoreServices/Finder.app","name":"Finder","pid":351,"running":true},` +
	`{"active":false,"bundle_id":"com.apple.calculator","kind":"desktop","last_used":"2026-08-13T02:51:55Z","launch_path":"/System/Applications/Calculator.app","name":"Calculator","pid":0,"running":false},` +
	`{"active":false,"bundle_id":"com.apple.reminders","kind":"desktop","last_used":"2026-08-13T02:51:55Z","launch_path":"/System/Applications/Reminders.app","name":"Reminders","pid":0,"running":false}]}}}`

// serveDriver answers every driver call on a unix socket with reply.
func serveDriver(t *testing.T, reply string) string {
	t.Helper()
	sock := filepath.Join(shortDir(t), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					if _, err := r.ReadBytes('\n'); err != nil {
						return
					}
					if _, err := c.Write([]byte(reply + "\n")); err != nil {
						return
					}
				}
			}()
		}
	}()
	return sock
}

func TestDriverAppsReadsStructuredContent(t *testing.T) {
	c := &driver.Client{Socket: serveDriver(t, listAppsReply)}
	defer c.Close()
	apps, err := driverApps(c)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.AppInfo{
		{Name: "Finder", BundleID: "com.apple.finder", Path: "/System/Library/CoreServices/Finder.app", PID: 351, Running: true},
		{Name: "Calculator", BundleID: "com.apple.calculator", Path: "/System/Applications/Calculator.app", LastUsed: "2026-08-13T02:51:55Z"},
		{Name: "Reminders", BundleID: "com.apple.reminders", Path: "/System/Applications/Reminders.app", LastUsed: "2026-08-13T02:51:55Z"},
	}
	if len(apps) != len(want) {
		t.Fatalf("apps %+v, want %d", apps, len(want))
	}
	for i := range want {
		if apps[i] != want[i] {
			t.Errorf("app %d = %+v, want %+v", i, apps[i], want[i])
		}
	}

	// Through the engine's apps tool, as cua.listApps and getState read it.
	v, perr := (&engine.Engine{}).AppsTool(driverApps(c))(context.Background(), nil, json.RawMessage(`{}`))
	if perr != nil {
		t.Fatal(perr)
	}
	res := v.(protocol.AppsResult)
	if len(res.Apps) != 3 || len(res.Errors) != 0 {
		t.Fatalf("inventory %+v", res)
	}
}

func TestParseAppsShapes(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		n         int
		fail      bool
	}{
		{"bare list", `[{"name":"Finder","pid":1}]`, 1, false},
		{"bare object", `{"apps":[{"app_name":"Finder","is_running":true}]}`, 1, false},
		{"structured", `{"content":[],"structuredContent":{"apps":[{"name":"Finder"},{"name":"Notes"}]}}`, 2, false},
		{"structured empty", `{"structuredContent":{"apps":[]}}`, 0, false},
		{"text only", `{"content":[{"type":"text","text":"Found 3 app(s)"}]}`, 0, true},
		{"other object", `{"windows":[]}`, 0, true},
	} {
		apps, err := parseApps(json.RawMessage(tc.raw))
		if (err != nil) != tc.fail || len(apps) != tc.n {
			t.Errorf("%s: %d apps, err %v; want %d, fail %v", tc.name, len(apps), err, tc.n, tc.fail)
		}
	}
}
