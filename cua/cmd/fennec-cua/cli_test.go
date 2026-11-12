package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"fennec/cua/internal/cli"
	"fennec/cua/internal/mcpserver"
)

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cuac")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestBridgeStartsThroughApp(t *testing.T) {
	cases := map[string]string{
		"/Applications/Fennec.app/Contents/MacOS/fennec-cua":            "/Applications/Fennec.app",
		"/Applications/Fennec.app/Contents/Helpers/fennec-cua":          "/Applications/Fennec.app",
		"/Users/a/Apps/Nested.app/Contents/Frameworks/X.app/Contents/b": "/Users/a/Apps/Nested.app/Contents/Frameworks/X.app",
		"/usr/local/bin/fennec-cua":                                     "",
		"/tmp/Fennec.app/fennec-cua":                                    "",
		"/tmp/NotAnApp/Contents/MacOS/fennec-cua":                       "",
	}
	for exe, want := range cases {
		got, ok := cli.AppBundle(exe)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v, want %q", exe, got, ok, want)
		}
	}
	inApp := "/Applications/Fennec.app/Contents/MacOS/fennec-cua"
	cmd := cli.StartCommand(inApp)
	if cmd.Path != "/usr/bin/open" || strings.Join(cmd.Args[1:], " ") != "-g -a /Applications/Fennec.app" {
		t.Errorf("bundled start: %s %v", cmd.Path, cmd.Args)
	}
	bare := cli.StartCommand("/usr/local/bin/fennec-cua")
	if bare.Path != "/usr/local/bin/fennec-cua" || strings.Join(bare.Args[1:], " ") != "serve" {
		t.Errorf("bare start: %s %v", bare.Path, bare.Args)
	}
	// The app, opened by LaunchServices with no arguments under launchd,
	// serves in place; nothing else does.
	if !cli.LaunchedByLaunchd(1, inApp, nil) {
		t.Error("launchd start of the bundle does not serve")
	}
	if cli.LaunchedByLaunchd(4242, inApp, nil) || cli.LaunchedByLaunchd(1, "/usr/local/bin/fennec-cua", nil) || cli.LaunchedByLaunchd(1, inApp, []string{"mcp"}) {
		t.Error("a launch that is not LaunchServices' serves in place")
	}
	if mcpserver.StartWait != 10*time.Second {
		t.Errorf("start wait %s", mcpserver.StartWait)
	}
	// The bridge waits for a daemon that takes a while to listen.
	sock := filepath.Join(shortDir(t), "cua.sock")
	start := func() (<-chan error, error) {
		go func() {
			time.Sleep(300 * time.Millisecond)
			ln, err := net.Listen("unix", sock)
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = ln.Close() })
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			r := bufio.NewReader(c)
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				var m struct {
					ID json.RawMessage `json:"id"`
				}
				_ = json.Unmarshal([]byte(line), &m)
				_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":` + string(m.ID) + `,"result":{"late":true}}` + "\n"))
			}
		}()
		return nil, nil
	}
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe"}}` + "\n")
	if err := mcpserver.Bridge(context.Background(), in, &out, mcpserver.BridgeConfig{Socket: sock, Start: start}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"late":true`) {
		t.Errorf("bridge output %q", out.String())
	}
}

func TestBridgeDetachedStdio(t *testing.T) {
	cmd := cli.StartCommand("/usr/local/bin/fennec-cua")
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Error("the daemon would hold the bridge's stdio")
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("the daemon would share the bridge's session")
	}
	// A started daemon runs in its own session.
	dir := shortDir(t)
	script := filepath.Join(dir, "fake-daemon")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc := cli.StartCommand(script)
	exited, err := cli.Start(proc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Process.Kill(); <-exited })
	if sid, err := unix.Getsid(proc.Process.Pid); err != nil || sid != proc.Process.Pid {
		t.Errorf("session %d of %d: %v", sid, proc.Process.Pid, err)
	}
	// The bridge ends, closing its stdout, when its client closes stdin,
	// while the daemon keeps serving.
	sock := filepath.Join(dir, "cua.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var sessions atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			sessions.Add(1)
			go func() {
				_ = mcpserver.Serve(context.Background(), c, c, &echo{}, mcpserver.Info{})
				_ = c.Close()
			}()
		}
	}()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- mcpserver.Bridge(context.Background(), inR, outW, mcpserver.BridgeConfig{Socket: sock})
		_ = outW.Close()
	}()
	_, _ = inW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"))
	sc := bufio.NewScanner(outR)
	if !sc.Scan() || !strings.Contains(sc.Text(), `"id":1`) {
		t.Fatalf("ping: %q", sc.Text())
	}
	_ = inW.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the bridge outlived its stdin")
	}
	if sc.Scan() {
		t.Errorf("output after the end: %q", sc.Text())
	}
	if c, err := net.Dial("unix", sock); err != nil {
		t.Errorf("the daemon stopped with the bridge: %v", err)
	} else {
		_ = c.Close()
	}
}

type echo struct{}

func (*echo) Tools(*mcpserver.Session) []mcpserver.Tool { return nil }
func (*echo) Call(context.Context, *mcpserver.Session, string, json.RawMessage) mcpserver.Result {
	return mcpserver.Result{}
}

func TestDaemonEarlyExit(t *testing.T) {
	sock := filepath.Join(shortDir(t), "cua.sock")
	start := func() (<-chan error, error) {
		return cli.Start(exec.Command("/bin/sh", "-c", "exit 3"))
	}
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe"}}` + "\n")
	begin := time.Now()
	if err := mcpserver.Bridge(context.Background(), in, &out, mcpserver.BridgeConfig{Socket: sock, Start: start}); err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID    int `json:"id"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &m); err != nil || m.ID != 1 || !strings.Contains(m.Error.Message, "exit status 3") || !strings.Contains(m.Error.Message, "before answering") {
		t.Fatalf("reply %q %v", out.String(), err)
	}
	if time.Since(begin) > 3*time.Second {
		t.Errorf("waited %s for a daemon that had exited", time.Since(begin))
	}
}

func TestEmbeddedNoDaemon(t *testing.T) {
	t.Setenv(mcpserver.EmbeddedEnv, "1")
	if !cli.Embedded() {
		t.Fatal("embedded mode not read from the environment")
	}
	dir := shortDir(t)
	sock := filepath.Join(dir, "cua.sock")
	var started atomic.Int32
	begin := time.Now()
	err := mcpserver.Bridge(context.Background(), strings.NewReader(""), io.Discard, mcpserver.BridgeConfig{
		Socket:   sock,
		Embedded: cli.Embedded(),
		Start:    func() (<-chan error, error) { started.Add(1); return nil, nil },
	})
	if !errors.Is(err, mcpserver.ErrEmbeddedNoDaemon) || !strings.Contains(err.Error(), "embedded mode") {
		t.Fatalf("err %v", err)
	}
	if time.Since(begin) > time.Second || started.Load() != 0 {
		t.Errorf("took %s, started %d", time.Since(begin), started.Load())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v behind", entries)
	}
	t.Setenv(mcpserver.EmbeddedEnv, "0")
	if cli.Embedded() {
		t.Error("0 counts as embedded")
	}
}

func TestDoArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, `{}`},
		{[]string{`{"scope":"desktop","nodes":true}`}, `{"nodes":true,"scope":"desktop"}`},
		{[]string{"scope=desktop", "nodes=true", "max_edge=7"}, `{"max_edge":7,"nodes":true,"scope":"desktop"}`},
		{[]string{"window=Untitled 2", "find=button \"Save\""}, `{"find":"button \"Save\"","window":"Untitled 2"}`},
		{[]string{"steps=[{\"key\":\"cmd+s\"}]"}, `{"steps":[{"key":"cmd+s"}]}`},
		{[]string{"text=", "eq=a=b"}, `{"eq":"a=b","text":""}`},
	}
	for _, c := range cases {
		got, err := cli.ParseDoArgs(c.args)
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal([]byte(c.want), &b)
		ga, _ := json.Marshal(a)
		gb, _ := json.Marshal(b)
		if string(ga) != string(gb) {
			t.Errorf("%v: %s, want %s", c.args, got, c.want)
		}
	}
	for _, bad := range [][]string{{"desktop"}, {"scope=desktop", "nodes"}, {"=x"}, {`{"scope":`}, {`{"a":1}`, "b=2"}} {
		if got, err := cli.ParseDoArgs(bad); err == nil {
			t.Errorf("%v accepted as %s", bad, got)
		}
	}
}

func TestSaveCapture(t *testing.T) {
	dir := t.TempDir()
	images := map[string][]byte{
		"png":  append([]byte("\x89PNG\r\n\x1a\n"), 1, 2, 3, 0, 255),
		"jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 9, 8, 7},
		"webp": []byte("RIFF\x04\x00\x00\x00WEBPVP8L\x00"),
	}
	for ext, raw := range images {
		path, err := cli.SaveCapture(dir, base64.StdEncoding.EncodeToString(raw))
		if err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		if filepath.Ext(path) != "."+ext || filepath.Dir(path) != dir {
			t.Errorf("%s saved as %s", ext, path)
		}
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, raw) {
			t.Errorf("%s: bytes differ", ext)
		}
	}
	if _, err := cli.SaveCapture(dir, "not base64!"); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Errorf("invalid base64: %v", err)
	}
	if _, err := cli.SaveCapture(dir, base64.StdEncoding.EncodeToString([]byte("GIF89a"))); err == nil {
		t.Error("an unknown format was saved")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("files %v", entries)
	}
}
