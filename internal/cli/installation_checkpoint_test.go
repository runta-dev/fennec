package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparationIgnoresObservationButRetainsCredentialAndPolicyChanges(t *testing.T) {
	base := []byte(`[{"id":"one","agent":"codex","auth":{"access_token":"fixture","seen":"nested"},"seen":"yesterday","on":true}]`)
	want := preparationCredentialContent("logins.json", base)
	for _, updated := range []string{
		`[ { "on":true,"seen":"today","auth":{"seen":"nested","access_token":"fixture"},"agent":"codex","id":"one" } ]`,
		`[{"id":"one","agent":"codex","auth":{"access_token":"fixture","seen":"nested"},"on":true}]`,
	} {
		if !bytes.Equal(want, preparationCredentialContent("logins.json", []byte(updated))) {
			t.Fatal("observation metadata invalidates configuration")
		}
	}
	for _, change := range [][2]string{{"fixture", "rotated"}, {"nested", "changed"}, {"one", "two"}, {"codex", "claude"}, {"true", "false"}} {
		updated := bytes.ReplaceAll(base, []byte(change[0]), []byte(change[1]))
		if bytes.Equal(want, preparationCredentialContent("logins.json", updated)) {
			t.Fatal("authentication, identity or policy change was ignored")
		}
	}
	if bytes.Equal(preparationCredentialContent("providers.json", []byte(`{"seen":1}`)), preparationCredentialContent("providers.json", []byte(`{"seen":2}`))) {
		t.Fatal("timestamp filtering leaked into another store")
	}
}

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

func TestExecutableUpdatePreservesConfigurationButCredentialChangeDoesNot(t *testing.T) {
	home, artifacts := t.TempDir(), t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		write(filepath.Join(artifacts, "fennec-terminal-linux-"+arch), "version-one")
	}
	digest := func(token, runtime, model string, magpie bool) (string, string) {
		t.Helper()
		full, configuration, err := preparationFingerprints(home, token, runtime, model, artifacts, magpie)
		if err != nil {
			t.Fatal(err)
		}
		return full, configuration
	}
	beforeFull, beforeConfig := digest("account-a", "runtime-a", "model-a", true)
	write(filepath.Join(artifacts, "fennec-terminal-linux-amd64"), "version-two")
	afterFull, afterConfig := digest("account-a", "runtime-a", "model-a", true)
	if beforeFull == afterFull || beforeConfig != afterConfig {
		t.Fatal("binary update conflated with configuration update")
	}
	for _, scope := range [][3]string{{"account-b", "runtime-a", "model-a"}, {"account-a", "runtime-b", "model-a"}, {"account-a", "runtime-a", "model-b"}} {
		_, configuration := digest(scope[0], scope[1], scope[2], true)
		if configuration == beforeConfig {
			t.Fatal("configuration reused across identity/model scope")
		}
	}
	_, nativeConfig := digest("account-a", "runtime-a", "model-a", false)
	if nativeConfig == beforeConfig {
		t.Fatal("configuration reused across integration modes")
	}
	write(filepath.Join(home, ".config/magpie/logins.json"), `{"fixture":"rotated"}`)
	_, rotated := digest("account-a", "runtime-a", "model-a", true)
	if rotated == beforeConfig {
		t.Fatal("changed credentials reused")
	}
}
