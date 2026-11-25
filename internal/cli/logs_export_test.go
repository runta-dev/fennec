package cli

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fennec/internal/account"
	"fennec/internal/applog"
	"fennec/internal/control"
	"fennec/internal/controlplane"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogsExportReturnsArchiveWithSummaryAndLogs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home := t.TempDir()
	logs := applog.Dir(home)
	os.MkdirAll(logs, 0o700)
	day := filepath.Join(logs, "fennec-"+time.Now().Format("2006-01-02")+".log")
	os.WriteFile(day, []byte("cloud computer preparation 1 failed after 3s: Runta HTTP 503\n"), 0o600)
	manager := account.New(ctx, serviceCredentials{}, "")
	c := &controlplane.Controller{Account: manager, Additional: map[string]control.Handler{}}
	installLogExport(home, "/Applications/Fennec.app/Contents/MacOS/fen", c)
	handler := c.Handlers()["logs.export"]
	if handler == nil {
		t.Fatal("logs.export not registered")
	}
	value, err := handler(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	reply := value.(map[string]string)
	if !strings.HasPrefix(reply["path"], applog.ExportDir(home)+"/") || filepath.Base(reply["path"]) != reply["name"] {
		t.Fatalf("reply = %v", reply)
	}
	archive, err := zip.OpenReader(reply["path"])
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	found := map[string]string{}
	for _, f := range archive.File {
		r, _ := f.Open()
		data, _ := io.ReadAll(r)
		r.Close()
		found[f.Name] = string(data)
	}
	if !strings.Contains(found["system.txt"], "runtime_stage: ") || !strings.Contains(found["system.txt"], "build_id: ") {
		t.Fatalf("system.txt = %q", found["system.txt"])
	}
	if !strings.Contains(found["logs/"+filepath.Base(day)], "Runta HTTP 503") {
		t.Fatalf("archive = %v", found)
	}
}
