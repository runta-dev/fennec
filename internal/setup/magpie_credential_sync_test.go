package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMagpieSyncPreservesNewerCloudGrant(t *testing.T) {
	home := t.TempDir()
	now := time.Now().Unix()
	entry := func(issued int64) string {
		data, _ := json.Marshal([]map[string]any{{"agent": "codex", "id": "fixture", "auth": fakeLoginAuth("fixture", issued)}})
		return string(data)
	}
	push := func(issued int64) (CredentialMigrationReport, error) {
		return SyncMagpieCredentials(context.Background(), home, map[string]string{"logins.json": encodeNative(entry(issued))})
	}
	if _, err := push(now - 100); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".config/magpie/logins.json")
	if err := os.WriteFile(path, []byte(entry(now-10)), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := push(now - 20)
	if err != nil || report.Preserved != 1 {
		t.Fatal("newer cloud grant was not preserved", report, err)
	}
	data, _ := os.ReadFile(path)
	value, _ := parseCredentialJSON(data)
	_, issued, _, _ := subscriptionTokenMetadata(value.([]any)[0].(map[string]any)["auth"])
	if issued != now-10 {
		t.Fatal("cloud token regressed")
	}
	public, _ := json.Marshal(report)
	if strings.Contains(string(public), "token") || strings.Contains(string(public), "fixture") {
		t.Fatal("credential material in public report")
	}
	if _, err = push(now); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	value, _ = parseCredentialJSON(data)
	_, issued, _, _ = subscriptionTokenMetadata(value.([]any)[0].(map[string]any)["auth"])
	if issued != now {
		t.Fatal("newer local grant not applied")
	}
}

func TestGatherMagpieCredentialsIgnoresUnrelatedCatalog(t *testing.T) {
	home := t.TempDir()
	nativeFixture(t, home, ".config/magpie/providers.json", `{"fixture":"token"}`)
	nativeFixture(t, home, ".config/magpie/plugin-providers.json", `incomplete catalog write`)
	files, err := GatherMagpieCredentials(home)
	if err != nil || len(files) != 1 || files["providers.json"] == "" {
		t.Fatal("catalog blocked credential sync", err)
	}
	for _, name := range []string{"../outside", "migrations.json", "plugin-providers.json"} {
		if _, err = SyncMagpieCredentials(context.Background(), t.TempDir(), map[string]string{name: encodeNative(`{}`)}); err == nil {
			t.Fatal("accepted unrelated file")
		}
	}
}
