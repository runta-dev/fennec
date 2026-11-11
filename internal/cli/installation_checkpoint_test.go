package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialRotationInvalidatesConfigurationNotInstallation(t *testing.T) {
	home, artifacts := t.TempDir(), t.TempDir()
	for _, name := range []string{"fennec-terminal-linux-amd64", "fennec-terminal-linux-arm64"} {
		if err := os.WriteFile(filepath.Join(artifacts, name), []byte("artifact-v1"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inputs := []string{"codex"}
	installation := func(token, runtime string) string {
		t.Helper()
		value, err := installationFingerprint(home, token, runtime, inputs, artifacts)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	configuration := func() string {
		t.Helper()
		value, err := preparationFingerprint(home, "account-a", "runtime-a", inputs, artifacts)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	beforeInstallation, beforeConfiguration := installation("account-a", "runtime-a"), configuration()
	if err := os.MkdirAll(filepath.Join(home, ".config/magpie"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config/magpie/logins.json"), []byte(`{"token":"rotated-test-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if configuration() == beforeConfiguration {
		t.Fatal("credential update was ignored")
	}
	if installation("account-a", "runtime-a") != beforeInstallation {
		t.Fatal("credentials unnecessarily invalidated installed binaries")
	}
	for _, scope := range [][2]string{{"account-b", "runtime-a"}, {"account-a", "runtime-b"}} {
		if installation(scope[0], scope[1]) == beforeInstallation {
			t.Fatal("installation reused across scope")
		}
	}
	if err := os.WriteFile(filepath.Join(artifacts, "fennec-terminal-linux-amd64"), []byte("artifact-v2"), 0600); err != nil {
		t.Fatal(err)
	}
	if installation("account-a", "runtime-a") == beforeInstallation {
		t.Fatal("changed artifact reused")
	}
}

func TestInstallationCheckpointIsIndependentOfConfiguration(t *testing.T) {
	home := t.TempDir()
	if installationCheckpointMatches(home, "scope-a") {
		t.Fatal("missing checkpoint accepted")
	}
	if err := writePublicJSON(installationCheckpointPath(home), map[string]string{"fingerprint": "scope-a"}); err != nil {
		t.Fatal(err)
	}
	if !installationCheckpointMatches(home, "scope-a") || installationCheckpointMatches(home, "scope-b") {
		t.Fatal("checkpoint scope mismatch")
	}
	if _, ok := readPreparationCheckpoint(home, "scope-a"); ok {
		t.Fatal("installation checkpoint accepted as credential/config readiness")
	}
}
