package standalone

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeApp builds a minimal standalone app (Info.plist, fennec-cua, a skill)
// under dir.
func fakeApp(t *testing.T, dir, version string) string {
	t.Helper()
	app := filepath.Join(dir, AppName)
	for _, d := range []string{"Contents/MacOS", "Contents/Resources/skills/" + SkillName + "/references"} {
		if err := os.MkdirAll(filepath.Join(app, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"Contents/Info.plist":                                         string(InfoPlist(version)),
		"Contents/MacOS/fennec-cua":                                   "#!/bin/sh\necho " + version + "\n",
		"Contents/Resources/skills/" + SkillName + "/SKILL.md":        "---\nname: fennec-cua\n---\n",
		"Contents/Resources/skills/" + SkillName + "/references/a.md": "a",
	}
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(app, p), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

// copyTree stands in for ditto in tests.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o755); err != nil {
			return err
		}
		// ditto keeps extended attributes: carry the quarantine over.
		if Quarantined(p) {
			return unix.Setxattr(target, QuarantineAttr, []byte("0081;00000000;Safari;"), 0)
		}
		return nil
	})
}

func TestInfoPlistAndIsApp(t *testing.T) {
	plist := string(InfoPlist("1.2.3 <dev>"))
	for _, want := range []string{"<string>" + BundleID + "</string>", "<key>LSUIElement</key>\n\t<true/>", "<string>fennec-cua</string>", "1.2.3 &lt;dev&gt;", "NSScreenCaptureUsageDescription"} {
		if !strings.Contains(plist, want) {
			t.Errorf("Info.plist lacks %q", want)
		}
	}
	dir := t.TempDir()
	if !IsApp(fakeApp(t, dir, "1")) {
		t.Error("the app is not recognised")
	}
	if IsApp(dir) {
		t.Error("a directory without the Info.plist is taken for the app")
	}
}

func TestInstallReplaceAndUninstall(t *testing.T) {
	root := t.TempDir()
	src := fakeApp(t, filepath.Join(root, "archive"), "2")
	// A downloaded archive's files carry the quarantine attribute.
	if err := unix.Setxattr(Executable(src), QuarantineAttr, []byte("0081;00000000;Safari;"), 0); err != nil {
		t.Skipf("cannot set extended attributes here: %v", err)
	}
	lay := Layout{AppDir: filepath.Join(root, "Applications"), BinDir: filepath.Join(root, "bin")}
	// An older install is replaced; the daemon is stopped before.
	fakeApp(t, lay.AppDir, "1")
	stops := 0
	verified := ""
	in := &Installer{Layout: lay, Copy: copyTree, Verify: func(app string) error { verified = app; return nil }, StopDaemon: func() error { stops++; return nil }}
	res, err := in.Install(src, "/usr/bin:"+lay.BinDir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Copied || res.Unquarantined != 1 || !res.OnPath || stops != 1 {
		t.Fatalf("result %+v, %d stops", res, stops)
	}
	if !strings.HasSuffix(verified, ".installing") {
		t.Errorf("verified %q instead of the staged copy", verified)
	}
	data, _ := os.ReadFile(lay.Exe())
	if !strings.Contains(string(data), "echo 2") {
		t.Fatalf("the installed app is not the new one: %q", data)
	}
	if Quarantined(lay.Exe()) {
		t.Error("the installed copy is still quarantined")
	}
	if target, err := os.Readlink(lay.Link()); err != nil || target != lay.Exe() {
		t.Fatalf("link %q %v", target, err)
	}
	for _, leftover := range []string{lay.App() + ".installing", lay.App() + ".previous"} {
		if _, err := os.Lstat(leftover); err == nil {
			t.Errorf("%s left behind", leftover)
		}
	}
	// Installing from the installed app only refreshes the link.
	res, err = in.Install(lay.App(), "/usr/bin")
	if err != nil || res.Copied || res.OnPath {
		t.Fatalf("reinstall in place: %+v %v", res, err)
	}

	// A signature that does not verify leaves the installed app alone.
	in.Verify = func(string) error { return errors.New("bad signature") }
	if _, err := in.Install(src, ""); err == nil {
		t.Fatal("an unverified copy was installed")
	}
	if !IsApp(lay.App()) {
		t.Fatal("a failed install removed the installed app")
	}

	// Uninstall removes the app, our link and the data paths given.
	dataDir := filepath.Join(root, "data")
	_ = os.MkdirAll(dataDir, 0o700)
	un, err := in.Uninstall([]string{dataDir, filepath.Join(root, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if len(un.Removed) != 3 {
		t.Fatalf("removed %v", un.Removed)
	}
	for _, p := range []string{lay.App(), lay.Link(), dataDir} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s remains", p)
		}
	}
}

func TestInstallRefusals(t *testing.T) {
	root := t.TempDir()
	lay := Layout{AppDir: filepath.Join(root, "Applications"), BinDir: filepath.Join(root, "bin")}
	in := &Installer{Layout: lay, Copy: copyTree, Verify: func(string) error { return nil }}
	if _, err := in.Install(root, ""); err == nil {
		t.Error("a directory that is not the app was installed")
	}
	// A file someone else put where the link goes is never replaced.
	src := fakeApp(t, filepath.Join(root, "archive"), "1")
	_ = os.MkdirAll(lay.BinDir, 0o755)
	_ = os.WriteFile(lay.Link(), []byte("mine"), 0o755)
	if _, err := in.Install(src, ""); err == nil || !strings.Contains(err.Error(), "not a link") {
		t.Errorf("got %v", err)
	}
	// Uninstall keeps a link that points elsewhere.
	_ = os.Remove(lay.Link())
	_ = os.Symlink("/usr/bin/true", lay.Link())
	un, err := in.Uninstall(nil)
	if err != nil || len(un.Kept) != 1 {
		t.Fatalf("%+v %v", un, err)
	}
	if _, err := os.Lstat(lay.Link()); err != nil {
		t.Error("a foreign link was removed")
	}
}

func TestCopyTreeAndOnPath(t *testing.T) {
	root := t.TempDir()
	app := fakeApp(t, root, "1")
	dst := filepath.Join(root, "skills", SkillName)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dst, "stale.md"), []byte("x"), 0o644)
	if err := CopyTree(SkillDir(app), dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "references", "a.md")); err != nil {
		t.Error("the skill's references were not copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.md")); err == nil {
		t.Error("a stale file survived the copy")
	}
	if !OnPath("/a/b/", "/x:/a/b") || OnPath("/a/b", "/a/bc:/x") {
		t.Error("OnPath")
	}
}

func TestDiagnose(t *testing.T) {
	healthy := Facts{
		Exe: "/A/" + AppName + "/Contents/MacOS/fennec-cua", App: "/A/" + AppName,
		Driver: "/A/" + AppName + "/Contents/MacOS/cua-driver", Chromium: "/A/" + AppName + "/Contents/Helpers/Chromium.app/Contents/MacOS/Chromium",
		Command: "/h/.local/bin/fennec-cua", CommandOK: true,
		DaemonExe:     "/A/" + AppName + "/Contents/MacOS/fennec-cua",
		Permissions:   map[string]string{"accessibility": "granted", "screen_recording": "granted"},
		DriverRunning: true, BrowserAvailable: true,
	}
	cs := Diagnose(healthy)
	if !Healthy(cs) {
		t.Fatalf("healthy facts fail: %+v", cs)
	}
	for _, c := range cs {
		if c.Level != OK {
			t.Errorf("healthy facts give %+v", c)
		}
	}

	f := healthy
	f.Permissions = map[string]string{"accessibility": "denied"}
	f.Quarantined = true
	f.Chromium = ""
	f.DaemonExe = "/Applications/Fennec.app/Contents/MacOS/fennec-cua"
	cs = Diagnose(f)
	if Healthy(cs) {
		t.Fatal("missing permissions pass")
	}
	byName := map[string]Check{}
	for _, c := range cs {
		byName[c.Name] = c
	}
	if c := byName["Accessibility"]; c.Level != Fail || !strings.Contains(c.Fix, "Privacy_Accessibility") {
		t.Errorf("accessibility: %+v", c)
	}
	if c := byName["Screen Recording"]; c.Level != Fail || !strings.HasPrefix(c.Detail, "unknown") {
		t.Errorf("screen recording: %+v", c)
	}
	for _, name := range []string{"quarantine", "browser", "daemon app"} {
		if byName[name].Level != Warn {
			t.Errorf("%s: %+v", name, byName[name])
		}
	}

	f = healthy
	f.DaemonErr = errors.New("did not answer")
	cs = Diagnose(f)
	if Healthy(cs) || cs[len(cs)-1].Name != "daemon" {
		t.Fatalf("a daemon that does not answer: %+v", cs)
	}
	var sb strings.Builder
	Render(&sb, cs)
	if !strings.Contains(sb.String(), "fail  daemon") || !strings.Contains(sb.String(), "fix: run `fennec-cua serve`") {
		t.Errorf("render:\n%s", sb.String())
	}
}

func TestMCPConfig(t *testing.T) {
	all, err := MCPConfig("", "/Users/a b/.local/bin/fennec-cua")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"claude mcp add --scope user fennec-cua -- '/Users/a b/.local/bin/fennec-cua' mcp",
		"codex mcp add fennec-cua -- '/Users/a b/.local/bin/fennec-cua' mcp",
		"[mcp_servers.fennec-cua]\ncommand = \"/Users/a b/.local/bin/fennec-cua\"\nargs = [\"mcp\"]",
		`"args": [`, "FENNEC_CUA_HTTP_TOKEN",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("config lacks %q:\n%s", want, all)
		}
	}
	codex, _ := MCPConfig("codex", "/x/fennec-cua")
	if strings.Contains(codex, "claude mcp") || !strings.Contains(codex, "codex mcp add fennec-cua -- /x/fennec-cua mcp") {
		t.Errorf("codex only:\n%s", codex)
	}
	if _, err := MCPConfig("nope", "/x"); err == nil {
		t.Error("unknown client accepted")
	}
	g := PermissionGuide("/A/" + AppName)
	for _, want := range []string{"Privacy_Accessibility", "Privacy_ScreenCapture", "fennec-cua permissions --request", "fennec-cua quit"} {
		if !strings.Contains(g, want) {
			t.Errorf("guide lacks %q", want)
		}
	}
}
