package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTart is a tart stand-in that answers --version and list.
func fakeTart(t *testing.T, list string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tart")
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo 2.41.1 ;;\nlist) cat <<'EOF'\n" + list + "\nEOF\n;;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeVM(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, "vms", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, data := range map[string]string{"config.json": `{"macAddress":"aa"}`, "nvram.bin": "nv", "disk.img": "disk"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestValidWarmFollowsTheGoldenImage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TART_HOME", home)
	ctx := context.Background()
	golden := writeVM(t, home, "g")
	warm := writeVM(t, home, WarmName("g", 1))
	tart := fakeTart(t, `[{"Name":"g","State":"stopped"},{"Name":"g-warm-1","State":"suspended"},{"Name":"g-warm-2","State":"stopped"}]`)

	if _, err := ValidWarm(ctx, tart, "g", 1); err == nil || !strings.Contains(err.Error(), "no build record") {
		t.Fatalf("without a record: %v", err)
	}
	id, err := GoldenID(ctx, tart, "g")
	if err != nil {
		t.Fatal(err)
	}
	stamp, _ := json.Marshal(warmStamp{Golden: "g", GoldenID: id})
	if err := os.WriteFile(filepath.Join(warm, warmStampFile), stamp, 0o644); err != nil {
		t.Fatal(err)
	}
	if name, err := ValidWarm(ctx, tart, "g", 1); err != nil || name != "g-warm-1" {
		t.Fatalf("valid image: %q, %v", name, err)
	}
	// Booting the golden image rewrites its disk.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(golden, "disk.img"), later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidWarm(ctx, tart, "g", 1); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("after a golden change: %v", err)
	}
	if _, err := ValidWarm(ctx, tart, "g", 2); err == nil || !strings.Contains(err.Error(), "not suspended") {
		t.Fatalf("a stopped image: %v", err)
	}
	if _, err := ValidWarm(ctx, tart, "g", 3); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("a missing image: %v", err)
	}
}
