package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckedServiceStartsOnlyAfterSuccessfulHealthCheck(t *testing.T) {
	for _, check := range []string{"test \"a'b\" = \"a'b\"", "false", "exit 17"} {
		command := exec.Command("/bin/sh", "-c", checkedServiceCommand(check, "printf service-started"))
		output, err := command.Output()
		if check == "test \"a'b\" = \"a'b\"" {
			if err != nil || string(output) != "service-started" {
				t.Fatalf("valid check failed: %q %v", output, err)
			}
		} else {
			if err == nil || len(output) != 0 {
				t.Fatalf("service started after failed check: %q %v", output, err)
			}
		}
	}
}

func TestPreparationFingerprintInvalidation(t *testing.T) {
	home := t.TempDir()
	artifacts := t.TempDir()
	for _, name := range []string{"fennec-terminal-linux-amd64", "fennec-terminal-linux-arm64"} {
		os.WriteFile(filepath.Join(artifacts, name), []byte("binary"), 0600)
	}
	fingerprint := func(token, runtime, model string) string {
		t.Helper()
		value, err := preparationFingerprint(home, token, runtime, model, artifacts)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	initial := fingerprint("account-a", "runtime-a", "model-a")
	if initial != fingerprint("account-a", "runtime-a", "model-a") {
		t.Fatal("unstable fingerprint")
	}
	for _, input := range [][3]string{{"account-b", "runtime-a", "model-a"}, {"account-a", "runtime-b", "model-a"}, {"account-a", "runtime-a", "model-b"}} {
		if initial == fingerprint(input[0], input[1], input[2]) {
			t.Fatal("scope/input change ignored")
		}
	}
	os.MkdirAll(filepath.Join(home, ".config/magpie"), 0700)
	os.WriteFile(filepath.Join(home, ".config/magpie/logins.json"), []byte(`{"secret":"changed"}`), 0600)
	if initial == fingerprint("account-a", "runtime-a", "model-a") {
		t.Fatal("credential change ignored")
	}
	checkpoint := preparationCheckpoint{Fingerprint: initial, Events: []map[string]string{{"stage": "agents_installed"}}}
	if err := savePreparationCheckpoint(home, checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPreparationCheckpoint(home, initial); !ok {
		t.Fatal("checkpoint missing")
	}
	if _, ok := readPreparationCheckpoint(home, "wrong"); ok {
		t.Fatal("accepted wrong scope")
	}
	info, _ := os.Stat(checkpointPath(home))
	if info.Mode().Perm() != 0600 {
		t.Fatal("checkpoint permissions")
	}
	data, _ := os.ReadFile(checkpointPath(home))
	if strings.Contains(string(data), "secret") {
		t.Fatal("credential persisted")
	}
}

func TestPreparationHealthRequiresRemoteMarkerAndBinaries(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local/bin")
	os.MkdirAll(bin, 0700)
	os.MkdirAll(filepath.Join(home, ".local/fennec/bin"), 0700)
	os.MkdirAll(filepath.Join(home, ".config/fennec"), 0700)
	for _, name := range []string{"node", "magpie", "herdr-core", "curl", "codex"} {
		os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700)
	}
	os.WriteFile(filepath.Join(home, ".local/fennec/bin/fennec-terminal"), []byte("#!/bin/sh\n"), 0700)
	digest := strings.Repeat("a", 64)
	run := func() error {
		cmd := exec.Command("/bin/sh", "-c", checkpointHealthScript(digest, []string{"codex"}))
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		return cmd.Run()
	}
	if run() == nil {
		t.Fatal("missing marker accepted")
	}
	os.WriteFile(filepath.Join(home, ".config/fennec/prepared-runtime"), []byte(digest), 0600)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(home, ".local/fennec/bin/fennec-terminal"))
	if run() == nil {
		t.Fatal("missing binary accepted")
	}
	if checkpointHealthScript(digest, []string{"codex;exit 0"}) != "exit 1" {
		t.Fatal("unsafe identifier")
	}
}

func TestNativeCheckpointDoesNotRequireMagpie(t *testing.T) {
	script := checkpointHealthScriptForMarker(strings.Repeat("a", 64), []string{"codex"}, "prepared-runtime", false)
	if strings.Contains(script, "magpie") || strings.Contains(script, "3425") {
		t.Fatal("native health depends on gateway", script)
	}
	if !strings.Contains(script, "herdr-core") || !strings.Contains(script, "command -v codex") {
		t.Fatal("native readiness checks removed")
	}
}

func TestMissingNativeCredentialsRetainPreparedInfrastructure(t *testing.T) {
	for _, status := range []string{"configured", "credentials_required", "local_provider_unavailable", "local_credential_command", "unsupported_agent"} {
		if !stablePreparedRoute(status) {
			t.Fatalf("stable state %s causes repeated installation", status)
		}
	}
	for _, status := range []string{"credentials_unavailable", "installation_failed", "verification_failed", ""} {
		if stablePreparedRoute(status) {
			t.Fatalf("unverified failure %s cached as stable", status)
		}
	}
}
