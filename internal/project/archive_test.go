package project

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveUsesOnlyVerifiedManifest(t *testing.T) {
	root := t.TempDir()
	content := []byte("source\x00data")
	os.WriteFile(filepath.Join(root, "main"), content, 0700)
	os.WriteFile(filepath.Join(root, "unlisted"), []byte("private"), 0600)
	hash := sha256.Sum256(content)
	m := Manifest{Root: root, Bytes: int64(len(content)), Files: []File{{Path: "main", Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:]), Executable: true}}}
	var out bytes.Buffer
	if err := WriteArchive(context.Background(), m, &out); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(&out)
	header, err := reader.Next()
	if err != nil || header.Name != "main" || header.Mode != 0700 {
		t.Fatal(header, err)
	}
	data, _ := io.ReadAll(reader)
	if !bytes.Equal(data, content) {
		t.Fatal("changed archive bytes")
	}
	if _, err = reader.Next(); err != io.EOF {
		t.Fatal("unlisted file included")
	}
	os.WriteFile(filepath.Join(root, "main"), []byte("changed"), 0700)
	if WriteArchive(context.Background(), m, io.Discard) == nil {
		t.Fatal("changed file accepted")
	}
}
func TestArchiveRejectsTraversalAndSensitivePaths(t *testing.T) {
	for _, path := range []string{"../secret", "/etc/passwd", ".env", "a/../secret"} {
		if WriteArchive(context.Background(), Manifest{Root: t.TempDir(), Files: []File{{Path: path}}}, io.Discard) == nil {
			t.Fatal(path)
		}
	}
}
