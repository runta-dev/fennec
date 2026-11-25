package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/cloudarchive"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeArchiveCaller struct {
	calls  []string
	status []archiveStatus
}

func (f *fakeArchiveCaller) Call(_ context.Context, method string, params, result any) error {
	f.calls = append(f.calls, method)
	var reply any
	switch method {
	case "cloud.archive.inspect":
		reply = cloudarchive.Manifest{Format: cloudarchive.Format, Version: 1, Counts: cloudarchive.Counts{Requests: 3, Sessions: 2}}
	case "cloud.archive.export", "cloud.archive.import":
		reply = archiveStatus{State: "running"}
	case "cloud.archive.status":
		reply, f.status = f.status[0], f.status[1:]
	}
	if result == nil {
		return nil
	}
	data, _ := json.Marshal(reply)
	return json.Unmarshal(data, result)
}

func TestFenCloudExportFollowsTheJobAndMovesTheFile(t *testing.T) {
	dir := t.TempDir()
	exported := filepath.Join(dir, "fennec-20261125-090000.fenarchive")
	os.WriteFile(exported, []byte("archive"), 0600)
	caller := &fakeArchiveCaller{status: []archiveStatus{
		{State: "running", Bytes: 1 << 20, Total: 4 << 20},
		{State: "done", Bytes: 4 << 20, Total: 4 << 20, Path: exported, Name: filepath.Base(exported)},
	}}
	target := filepath.Join(dir, "chosen")
	os.Mkdir(target, 0700)
	var output, progress bytes.Buffer
	if err := cloudArchiveCommand(context.Background(), caller, []string{"export", "--no-sessions", "--output", target}, &output, &progress, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(target, filepath.Base(exported))
	if strings.TrimSpace(output.String()) != moved || !strings.Contains(progress.String(), "4 / 4 MB") {
		t.Fatalf("output %q progress %q", output.String(), progress.String())
	}
	if data, err := os.ReadFile(moved); err != nil || string(data) != "archive" {
		t.Fatal("archive not moved", err)
	}
}

func TestFenCloudImportReportsTheCloudError(t *testing.T) {
	caller := &fakeArchiveCaller{status: []archiveStatus{{State: "failed", Error: "not a complete Fennec archive"}}}
	err := cloudArchiveCommand(context.Background(), caller, []string{"import", "/tmp/x.fenarchive"}, &bytes.Buffer{}, &bytes.Buffer{}, time.Millisecond)
	if err == nil || err.Error() != "not a complete Fennec archive" {
		t.Fatal(err)
	}
	if strings.Join(caller.calls, " ") != "cloud.archive.inspect cloud.archive.import cloud.archive.status" {
		t.Fatal(caller.calls)
	}
}

func TestManifestWriterReportsTheFirstLine(t *testing.T) {
	var found *cloudarchive.Manifest
	var buffer bytes.Buffer
	w := &manifestWriter{buffer: &buffer, found: func(m cloudarchive.Manifest) { found = &m }}
	w.Write([]byte(`{"format":"fennec-cloud-archive","version":1,"counts":{"by`))
	w.Write([]byte("tes\":42}}\nlater error\n"))
	if found == nil || found.Counts.Bytes != 42 {
		t.Fatal(found)
	}
	if err := remoteError(nil, &buffer); err != nil {
		t.Fatal(err)
	}
}

func TestPruneArchivesKeepsTheNewestTwo(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"fennec-20261101-000000.fenarchive", "fennec-20261110-000000.fenarchive", "fennec-20261120-000000.fenarchive", "fennec-20261125-000000.fenarchive", "fennec-logs-20261101-000000.zip"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0600)
	}
	pruneArchives(dir, filepath.Join(dir, "fennec-20261125-000000.fenarchive"))
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, " ") != "fennec-20261120-000000.fenarchive fennec-20261125-000000.fenarchive fennec-logs-20261101-000000.zip" {
		t.Fatal(names)
	}
}
