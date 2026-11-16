//go:build website_record

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPackPreservesTerminalByteBoundaries(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "private.ndjson"), filepath.Join(dir, "public.json")
	pieces := [][]byte{[]byte("private shell setup\x1b[H\x1b[J"), {0xe2, 0x94}, {0x80, 0x1b, '[', '3', '1', 'm'}, []byte("result")}
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	for i, b := range pieces {
		enc.Encode(chunk{int64(i) * 5000, base64.StdEncoding.EncodeToString(b)})
	}
	os.WriteFile(in, raw.Bytes(), 0600)
	if err := pack(in, out, 0, 0, 0, 10000); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	var result struct {
		Chunks []chunk `json:"chunks"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	last := int64(-1)
	for _, c := range result.Chunks {
		b, _ := base64.StdEncoding.DecodeString(c.Data)
		actual.Write(b)
		if c.Time < last {
			t.Fatal("timestamps moved backwards")
		}
		last = c.Time
	}
	expected := append([]byte("\x1b[H\x1b[J"), bytes.Join(pieces[1:], nil)...)
	if !bytes.Equal(actual.Bytes(), expected) {
		t.Fatalf("terminal bytes changed: %q", actual.Bytes())
	}
}
func TestPackRejectsPrivateMarkerAcrossChunks(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "private.ndjson"), filepath.Join(dir, "public.json")
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	for i, b := range []string{"API_", "KEY=secret"} {
		enc.Encode(chunk{int64(i) * 100, base64.StdEncoding.EncodeToString([]byte(b))})
	}
	os.WriteFile(in, raw.Bytes(), 0600)
	if pack(in, out, 0, 0, 0, 10000) == nil {
		t.Fatal("private data published")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("output should not exist")
	}
}
