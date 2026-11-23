package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fennec/internal/preview"
	"fennec/internal/terminalstream"
)

func TestPreviewEntryPointChild(t *testing.T) {
	if os.Getenv("FENNEC_PREVIEW_CHILD") != "1" {
		return
	}
	if os.Getenv("FENNEC_PREVIEW_MODE") == "register" {
		os.Args = []string{"fennec-terminal", "preview", "--session", "amber-cedar", "--title", "Report", "report.txt"}
	} else {
		os.Args = []string{"fennec-terminal", "serve"}
	}
	code, err := run()
	if err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	os.Exit(code)
}

func TestPreviewRegisteredByAgentLoadsOverResidentHTTP(t *testing.T) {
	// This service opens a Unix socket. macOS's default test temp directory
	// plus the test name exceeds sockaddr_un's path limit.
	home, err := os.MkdirTemp("/tmp", "fn-preview-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	workspace := filepath.Join(home, ".fen", "amber-cedar")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "report.txt"), []byte("preview from a real command"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	register := exec.CommandContext(ctx, executable, "-test.run=^TestPreviewEntryPointChild$")
	register.Dir = workspace
	register.Env = append(os.Environ(), "HOME="+home, "FENNEC_PREVIEW_CHILD=1", "FENNEC_PREVIEW_MODE=register")
	raw, err := register.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("preview registration: %v: %s", err, exit.Stderr)
		}
		t.Fatal(err)
	}
	parts := preview.Parse(strings.TrimSpace(string(raw)))
	if len(parts) != 1 || parts[0].Ref == "" {
		t.Fatal(string(raw))
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPreviewEntryPointChild$")
	command.Env = append(os.Environ(), "HOME="+home, "FENNEC_PREVIEW_CHILD=1", "FENNEC_PREVIEW_MODE=serve")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	resident, err := terminalstream.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer resident.Close()
	if err = resident.Ready(ctx); err != nil {
		resident.Close()
		t.Fatalf("preview resident service: %v: %s", err, stderr.String())
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return resident.DialSocket(ctx, terminalstream.SocketPreview, "")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	endpoint := "http://preview.fennec/v1/sessions/amber-cedar/previews/" + parts[0].Ref
	request, _ := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var metadata preview.Metadata
	err = json.NewDecoder(response.Body).Decode(&metadata)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || metadata.Title != "Report" {
		t.Fatal(metadata, response.StatusCode, err)
	}
	request, _ = http.NewRequestWithContext(ctx, "GET", endpoint+"/content", nil)
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "preview from a real command" {
		t.Fatal(string(body), err)
	}
	if _, err = resident.Ping(ctx); err != nil {
		t.Fatal("preview broke resident operations", err)
	}
}
