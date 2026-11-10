package cli

import (
	"encoding/json"
	"fennec/internal/syncengine"
	"io"
	"strings"
	"testing"
)

func TestLaunchProjectStopsOnConflicts(t *testing.T) {
	input := `{"token":"test","runtime_id":"r","device":"d","agent":"codex","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	called := false
	err := launchProjectWith(strings.NewReader(input), io.Discard, func(_ io.Reader, w io.Writer) error { return json.NewEncoder(w).Encode(syncReceipt{Ready: false}) }, func(io.Reader, io.Writer) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("agent started without synchronization")
	}
}
func TestLaunchProjectPassesMappedCwdAndArgs(t *testing.T) {
	input := `{"token":"test","runtime_id":"r","device":"d","agent":"codex","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","directory":"/local/project/sub","version":"0.166.0","arguments":["--model","p/m"]}`
	ready := syncengine.Session{Mode: "two-way-safe", Status: "watching", SuccessfulCycles: 1, Alpha: syncengine.Endpoint{Connected: true, Scanned: true}, Beta: syncengine.Endpoint{Connected: true, Scanned: true}}
	called := false
	err := launchProjectWith(strings.NewReader(input), io.Discard, func(_ io.Reader, w io.Writer) error {
		return json.NewEncoder(w).Encode(syncReceipt{Ready: true, Sync: ready, Mapping: syncengine.ProjectMapping{CloudRoot: "/cloud/project", CloudDirectory: "/cloud/project/sub"}})
	}, func(r io.Reader, _ io.Writer) error {
		called = true
		var req sessionRequest
		json.NewDecoder(r).Decode(&req)
		if req.Directory != "/cloud/project/sub" || len(req.Arguments) != 2 || req.Token != "test" || req.Version != "0.166.0" {
			t.Fatalf("wrong launch request")
		}
		return nil
	})
	if err != nil || !called {
		t.Fatal(err)
	}
}

func TestInvalidVersionStopsBeforeProjectUpload(t *testing.T) {
	input := `{"token":"test","runtime_id":"r","device":"d","agent":"codex","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":"latest"}`
	called := false
	operation := func(io.Reader, io.Writer) error { called = true; return nil }
	err := launchProjectWith(strings.NewReader(input), io.Discard, operation, operation)
	if err == nil || called {
		t.Fatal("invalid version reached sync or launch")
	}
}

func TestCloudWorkspaceLaunchDoesNotUploadLocalDirectory(t *testing.T) {
	input := `{"token":"test","runtime_id":"r","device":"aabbccdd","agent":"claude","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","directory":"/Users/example","cloud_workspace":true}`
	launched := false
	err := launchProjectWith(strings.NewReader(input), io.Discard, func(io.Reader, io.Writer) error { t.Fatal("unexpected local upload"); return nil }, func(r io.Reader, w io.Writer) error {
		var req sessionRequest
		if err := json.NewDecoder(r).Decode(&req); err != nil {
			return err
		}
		launched = req.CloudWorkspace
		if req.LocalDirectory != "/Users/example" || req.Device != "aabbccdd" {
			t.Fatal("local context lost", req.LocalDirectory, req.Device)
		}
		return nil
	})
	if err != nil || !launched {
		t.Fatal(err)
	}
}

func TestCloudWorkspaceRejectsInvalidContextBeforeLaunch(t *testing.T) {
	for _, input := range []string{
		`{"token":"test","runtime_id":"r","device":"bad","agent":"claude","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","directory":"/tmp","cloud_workspace":true}`,
		`{"token":"test","runtime_id":"r","device":"aabbccdd","agent":"claude","session_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","directory":"relative","cloud_workspace":true}`,
	} {
		called := false
		op := func(io.Reader, io.Writer) error { called = true; return nil }
		if err := launchProjectWith(strings.NewReader(input), io.Discard, op, op); err == nil || called {
			t.Fatal("invalid context reached launch", err)
		}
	}
}
