package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLocalMagpieModelSelectionNeverTargetsCloud(t *testing.T) {
	fixture, home := t.TempDir(), t.TempDir()
	source := filepath.Join(fixture, "magpie.go")
	code := `package main
import("fmt";"os")
func main(){a:=os.Args[1:];if len(a)<2||a[0]!="codex"||a[1]!="model"{os.Exit(2)};p:=os.Getenv("FENNEC_MAGPIE_FIXTURE_FILE");if len(a)==3{if os.WriteFile(p,[]byte(a[2]),0600)!=nil{os.Exit(3)}};b,e:=os.ReadFile(p);if e!=nil{os.Exit(4)};fmt.Println("Codex model",string(b))}`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", filepath.Join(fixture, "magpie"), source).CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, output)
	}
	modelFile := filepath.Join(fixture, "model")
	if err := os.WriteFile(modelFile, []byte("provider/old"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixture)
	t.Setenv("FENNEC_MAGPIE_FIXTURE_FILE", modelFile)
	newDesktopSettingsWithMagpie(home, func() bool { return true })
	// No Runta account, channel or controller: a local choice must stay local.
	service := daemonServices{home: home}
	handler := service.handlers(nil)["model.set"]
	result, err := handler(context.Background(), json.RawMessage(`{"agent":"codex","model":"provider/new","location":"local"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	if value["location"] != "local" || value["model"] != "provider/new" {
		t.Fatal(value)
	}
	data, _ := os.ReadFile(modelFile)
	if string(data) != "provider/new" {
		t.Fatal("local setting not changed")
	}
	if _, err = handler(context.Background(), json.RawMessage(`{"agent":"codex","model":"--unsafe/model","location":"local"}`)); err == nil {
		t.Fatal("option-like model accepted")
	}
	if _, err = handler(context.Background(), json.RawMessage(`{"agent":"codex","model":"provider/other","location":"unknown"}`)); err == nil {
		t.Fatal("unknown location defaulted to cloud")
	}
}
