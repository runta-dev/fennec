package project

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiverPublishesVerifiedArchiveAndRefusesOverwrite(t *testing.T) {
	source := t.TempDir()
	home := t.TempDir()
	os.WriteFile(filepath.Join(source, "file"), []byte("hello"), 0600)
	hash := sha256.Sum256([]byte("hello"))
	m := Manifest{Root: source, WorkingDirectory: "subdir", Bytes: 5, Files: []File{{Path: "file", Size: 5, SHA256: hex.EncodeToString(hash[:])}}}
	var input bytes.Buffer
	json.NewEncoder(&input).Encode(m)
	if err := WriteArchive(context.Background(), m, &input); err != nil {
		t.Fatal(err)
	}
	run := func() error {
		cmd := exec.Command("python3", "-c", Receiver, strings.Repeat("a", 32))
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = bytes.NewReader(input.Bytes())
		return cmd.Run()
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".local/fennec/projects", strings.Repeat("a", 32), "file")
	os.WriteFile(target, []byte("cloud edit"), 0600)
	if run() == nil {
		t.Fatal("existing snapshot overwritten")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "cloud edit" {
		t.Fatal("lost edits")
	}
}
func TestReceiverRejectsArchiveSymlink(t *testing.T) {
	home := t.TempDir()
	var input bytes.Buffer
	hash := sha256.Sum256(nil)
	json.NewEncoder(&input).Encode(Manifest{WorkingDirectory: ".", Files: []File{{Path: "link", SHA256: hex.EncodeToString(hash[:])}}})
	tw := tar.NewWriter(&input)
	tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/escape"})
	tw.Close()
	cmd := exec.Command("python3", "-c", Receiver, strings.Repeat("b", 32))
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = &input
	if cmd.Run() == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(home, ".local/fennec/projects", strings.Repeat("b", 32))); !os.IsNotExist(err) {
		t.Fatal("invalid snapshot published")
	}
}

func TestReceiverRejectsTruncatedArchive(t *testing.T) {
	home := t.TempDir()
	var input bytes.Buffer
	hash := sha256.Sum256([]byte("hello"))
	json.NewEncoder(&input).Encode(Manifest{WorkingDirectory: ".", Bytes: 5, Files: []File{{Path: "file", Size: 5, SHA256: hex.EncodeToString(hash[:])}}})
	tw := tar.NewWriter(&input)
	tw.WriteHeader(&tar.Header{Name: "file", Typeflag: tar.TypeReg, Size: 5, Mode: 0600})
	input.Write([]byte("he")) // deliberate disconnect partway through file data
	cmd := exec.Command("python3", "-c", Receiver, strings.Repeat("c", 32))
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = &input
	if cmd.Run() == nil {
		t.Fatal("truncated archive accepted")
	}
	target := filepath.Join(home, ".local/fennec/projects", strings.Repeat("c", 32))
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("partial snapshot published")
	}
	stages, _ := filepath.Glob(filepath.Join(home, ".local/fennec/projects/.receiving-*"))
	if len(stages) != 0 {
		t.Fatal("temporary extraction leaked")
	}
}
