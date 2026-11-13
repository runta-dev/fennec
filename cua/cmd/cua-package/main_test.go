package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fennec/cua/internal/chromium"
)

func writeZip(t *testing.T, files map[string]string, links map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	for name, target := range links {
		h := &zip.FileHeader{Name: name}
		h.SetMode(os.ModeSymlink | 0o755)
		w, _ := zw.CreateHeader(h)
		_, _ = w.Write([]byte(target))
	}
	zw.Close()
	f.Close()
	return p
}

func TestUnzipApp(t *testing.T) {
	good := writeZip(t, map[string]string{
		"chrome-mac/Chromium.app/Contents/MacOS/Chromium":                      "bin",
		"chrome-mac/Chromium.app/Contents/Frameworks/F.framework/Versions/1/F": "fw",
		"chrome-mac/other.txt": "skip",
	}, map[string]string{"chrome-mac/Chromium.app/Contents/Frameworks/F.framework/Versions/Current": "1"})
	dir := t.TempDir()
	if err := unzipApp(good, dir); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "Chromium.app/Contents/Frameworks/F.framework/Versions/Current/F")); string(data) != "fw" {
		t.Errorf("link %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.txt")); err == nil {
		t.Error("extracted a file outside the app")
	}
	for name, z := range map[string]string{
		"escaping path": writeZip(t, map[string]string{"chrome-mac/Chromium.app/../../evil": "x"}, nil),
		"escaping link": writeZip(t, nil, map[string]string{"chrome-mac/Chromium.app/Contents/x": "../../../etc"}),
		"absolute link": writeZip(t, nil, map[string]string{"chrome-mac/Chromium.app/Contents/y": "/etc"}),
		"no app":        writeZip(t, map[string]string{"chrome-mac/x": "x"}, nil),
	} {
		if err := unzipApp(z, t.TempDir()); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestVerify(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	_ = os.WriteFile(p, []byte("hello"), 0o600)
	b := chromium.Build{Size: 5, SHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	if err := verify(p, b); err != nil {
		t.Error(err)
	}
	b.SHA256 = strings.Repeat("0", 64)
	if err := verify(p, b); err == nil {
		t.Error("a wrong hash passed")
	}
	b.Size = 6
	if err := verify(p, b); err == nil {
		t.Error("a wrong size passed")
	}
}

func TestEntitlements(t *testing.T) {
	jit := "com.apple.security.cs.allow-jit"
	for name, want := range map[string]string{
		"Chromium Helper (Renderer).app":          jit,
		"Chromium Helper (Aperitif Renderer).app": jit,
		"Chromium Helper (GPU).app":               jit,
		"Chromium Helper (Aperitif GPU).app":      jit,
		"Chromium Helper.app":                     "",
		"Chromium Helper (Alerts).app":            "",
		"Chromium.app":                            "com.apple.security.device.camera",
	} {
		got := strings.Join(entitlementsFor(name), " ")
		if (want == "" && got != "") || !strings.Contains(got, want) {
			t.Errorf("%s: %s", name, got)
		}
	}
	if p := string(plist([]string{jit})); !strings.Contains(p, "<key>"+jit+"</key><true/>") {
		t.Errorf("plist %s", p)
	}
	m := identityLine.FindStringSubmatch(`  1) 0123456789ABCDEF0123456789ABCDEF01234567 "Fennec Local Development" (CSSMERR_TP_NOT_TRUSTED)`)
	if m == nil || m[2] != "Fennec Local Development" {
		t.Errorf("identity line %v", m)
	}
}
