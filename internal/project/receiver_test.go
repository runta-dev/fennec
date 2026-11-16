package project

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
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
		_, err := Receive(context.Background(), home, strings.Repeat("a", 32), bytes.NewReader(input.Bytes()))
		return err
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
	if _, err := Receive(context.Background(), home, strings.Repeat("b", 32), &input); err == nil {
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
	if _, err := Receive(context.Background(), home, strings.Repeat("c", 32), &input); err == nil {
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

func TestReceiverRejectsInvalidManifestAndArchive(t *testing.T) {
	sum := sha256.Sum256([]byte("good"))
	file := File{Path: "dir/file", Size: 4, SHA256: hex.EncodeToString(sum[:])}
	for _, test := range []struct {
		name   string
		change func(*Manifest)
		member string
		data   string
	}{
		{name: "traversal", change: func(m *Manifest) { m.Files[0].Path = "../escape" }},
		{name: "duplicate", change: func(m *Manifest) { m.Files = append(m.Files, m.Files[0]); m.Bytes *= 2 }},
		{name: "size", change: func(m *Manifest) { m.Bytes++ }},
		{name: "working directory", change: func(m *Manifest) { m.WorkingDirectory = "../escape" }},
		{name: "digest", change: func(m *Manifest) { m.Files[0].SHA256 = strings.Repeat("f", 64) }},
		{name: "unlisted member", member: "extra"},
		{name: "bad content", data: "evil"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := Manifest{WorkingDirectory: ".", Bytes: 4, Files: []File{file}}
			if test.change != nil {
				test.change(&m)
			}
			var input bytes.Buffer
			json.NewEncoder(&input).Encode(m)
			tw := tar.NewWriter(&input)
			name := test.member
			if name == "" {
				name = file.Path
			}
			data := test.data
			if data == "" {
				data = "good"
			}
			tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 4})
			tw.Write([]byte(data))
			tw.Close()
			home := t.TempDir()
			if _, err := Receive(context.Background(), home, strings.Repeat("d", 32), &input); err == nil {
				t.Fatal("invalid snapshot published")
			}
			stages, _ := filepath.Glob(filepath.Join(home, ".local/fennec/projects/.receiving-*"))
			if len(stages) != 0 {
				t.Fatal("staging leaked")
			}
		})
	}
}

func TestReceiverRequiresArchiveEndAndSerializesPublishers(t *testing.T) {
	var input bytes.Buffer
	json.NewEncoder(&input).Encode(Manifest{WorkingDirectory: "."})
	header := input.Len()
	tar.NewWriter(&input).Close()
	for _, cut := range []int{header, header + 512} {
		if _, err := Receive(context.Background(), t.TempDir(), strings.Repeat("e", 32), bytes.NewReader(input.Bytes()[:cut])); err == nil {
			t.Fatal("missing end block accepted")
		}
	}
	home := t.TempDir()
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := Receive(context.Background(), home, strings.Repeat("e", 32), bytes.NewReader(input.Bytes()))
			done <- err
		}()
	}
	successes := 0
	for range 2 {
		if <-done == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatal("publication was not exclusive", successes)
	}
}
