package chromium

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake Chromium: with FAKE_CHROMIUM set it
// serves CDP on fds 3 and 4 like --remote-debugging-pipe.
func init() {
	if os.Getenv("FAKE_CHROMIUM") != "" {
		fakeChromium()
		os.Exit(0)
	}
}

func fakeChromium() {
	in := os.NewFile(3, "cdp-in")
	out := os.NewFile(4, "cdp-out")
	if in == nil || out == nil {
		os.Exit(3)
	}
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "--user-data-dir=") {
			dir := strings.TrimPrefix(a, "--user-data-dir=")
			_ = os.WriteFile(filepath.Join(dir, "args"), []byte(strings.Join(os.Args[1:], "\n")), 0o600)
			_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
	}
	pages := 0
	if os.Getenv("FAKE_CHROMIUM") == "pages" {
		pages = 1
	}
	rd := bufio.NewReader(in)
	for {
		frame, err := rd.ReadBytes(0)
		if err != nil {
			os.Exit(0)
		}
		var m struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(frame[:len(frame)-1], &m)
		var res any = map[string]any{}
		switch m.Method {
		case "Browser.getVersion":
			res = map[string]any{"product": "Chrome/0-fake"}
		case "Target.getTargets":
			infos := []any{}
			for i := 0; i < pages; i++ {
				infos = append(infos, map[string]any{"targetId": "T", "type": "page"})
			}
			res = map[string]any{"targetInfos": infos}
		}
		data, _ := json.Marshal(map[string]any{"id": m.ID, "result": res})
		_, _ = out.Write(append(data, 0))
		if m.Method == "Browser.close" {
			os.Exit(0)
		}
	}
}

func fakeOpts(t *testing.T, mode string) Options {
	t.Helper()
	t.Setenv("FAKE_CHROMIUM", mode)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Options{Bin: self, ProfileDir: filepath.Join(t.TempDir(), "profile"), IdleTimeout: -1, Log: t.Logf}
}

func readPID(t *testing.T, o Options) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(o.ProfileDir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range string(data) {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestArgs(t *testing.T) {
	a := strings.Join(Args(Options{ProfileDir: "/p", MockKeychain: true}), " ")
	for _, want := range []string{"--remote-debugging-pipe", "--user-data-dir=/p", "--no-first-run", "--no-default-browser-check", "--use-mock-keychain", "--no-startup-window"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %s in %s", want, a)
		}
	}
	for _, never := range []string{"--remote-debugging-port", "--enable-automation", "--headless"} {
		if strings.Contains(a, never) {
			t.Errorf("%s in %s", never, a)
		}
	}
	h := strings.Join(Args(Options{ProfileDir: "/p", Headless: true}), " ")
	if !strings.Contains(h, "--headless") || strings.Contains(h, "--use-mock-keychain") || strings.Contains(h, "--no-startup-window") {
		t.Errorf("headless %s", h)
	}
	env := strings.Join(Env([]string{"PATH=/bin", "GOOGLE_API_KEY=real"}), " ")
	if env != "PATH=/bin GOOGLE_API_KEY=no GOOGLE_DEFAULT_CLIENT_ID=no GOOGLE_DEFAULT_CLIENT_SECRET=no" {
		t.Errorf("env %s", env)
	}
}

func TestLocate(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Fennec.app", "Contents")
	bin := filepath.Join(app, "Helpers", "Chromium.app", "Contents", "MacOS", "Chromium")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvApp, "")
	self := filepath.Join(app, "MacOS", "fennec-cua")
	if got := Locate(self); got != bin {
		t.Errorf("bundled %q", got)
	}
	// A staging directory: Chromium.app beside the binary.
	stage := t.TempDir()
	sbin := filepath.Join(stage, "Chromium.app", "Contents", "MacOS", "Chromium")
	_ = os.MkdirAll(filepath.Dir(sbin), 0o755)
	_ = os.WriteFile(sbin, []byte("x"), 0o755)
	if got := Locate(filepath.Join(stage, "fennec-cua")); got != sbin {
		t.Errorf("staged %q", got)
	}
	if got := Locate(filepath.Join(t.TempDir(), "fennec-cua")); got != "" {
		t.Errorf("none %q", got)
	}
	t.Setenv(EnvApp, "/dev/Chromium.app")
	if got := Locate(self); got != "/dev/Chromium.app/Contents/MacOS/Chromium" {
		t.Errorf("override %q", got)
	}
	t.Setenv(EnvApp, "off")
	if got := Locate(self); got != "" {
		t.Errorf("off %q", got)
	}
}

func TestSupervision(t *testing.T) {
	o := fakeOpts(t, "plain")
	b := New(o)
	defer b.Close()
	ctx := context.Background()
	c1, g1, err := b.Conn(ctx)
	if err != nil || g1 != 1 {
		t.Fatalf("start %v %d", err, g1)
	}
	if st, _ := os.Stat(o.ProfileDir); st.Mode().Perm() != 0o700 {
		t.Errorf("profile mode %v", st.Mode())
	}
	args, _ := os.ReadFile(filepath.Join(o.ProfileDir, "args"))
	if !strings.Contains(string(args), "--remote-debugging-pipe") {
		t.Errorf("args %s", args)
	}
	// The same connection while it lives.
	if c, g, _ := b.Conn(ctx); c != c1 || g != 1 {
		t.Error("a second connection")
	}
	// A crash: the next Conn starts it again.
	_ = syscall.Kill(readPID(t, o), syscall.SIGKILL)
	<-c1.Done()
	time.Sleep(50 * time.Millisecond)
	c2, g2, err := b.Conn(ctx)
	if err != nil || g2 != 2 || c2 == c1 {
		t.Fatalf("restart %v %d", err, g2)
	}
	if _, err := c2.Call(ctx, "", "Browser.getVersion", nil); err != nil {
		t.Error(err)
	}
	// Repeated crashes stop the restarts.
	for i := 0; i < 2; i++ {
		c, _, err := b.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = syscall.Kill(readPID(t, o), syscall.SIGKILL)
		<-c.Done()
		time.Sleep(50 * time.Millisecond)
	}
	if _, _, err := b.Conn(ctx); err == nil || !strings.Contains(err.Error(), "three times") {
		t.Errorf("crash loop %v", err)
	}
	// Close shuts it down for good.
	b.Close()
	if _, _, err := b.Conn(ctx); err == nil {
		t.Error("Conn after Close")
	}
}

func TestIdleShutdown(t *testing.T) {
	o := fakeOpts(t, "plain")
	o.IdleTimeout = 200 * time.Millisecond
	b := New(o)
	defer b.Close()
	c, _, err := b.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("an idle browser with no pages kept running")
	}
	if b.Running() {
		t.Error("still running")
	}
	// A browser with a page open stays.
	o2 := fakeOpts(t, "pages")
	o2.IdleTimeout = 200 * time.Millisecond
	b2 := New(o2)
	defer b2.Close()
	c2, _, err := b2.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c2.Done():
		t.Error("a browser with a page was shut down")
	case <-time.After(700 * time.Millisecond):
	}
}

func TestStaleOwner(t *testing.T) {
	o := fakeOpts(t, "plain")
	_ = os.MkdirAll(o.ProfileDir, 0o700)
	// A lock held by a live process that is not our Chromium is an error.
	if err := os.Symlink("host-"+strconv.Itoa(os.Getppid()), filepath.Join(o.ProfileDir, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	b := New(o)
	defer b.Close()
	if _, _, err := b.Conn(context.Background()); err == nil || !strings.Contains(err.Error(), "in use by another program") {
		t.Errorf("foreign owner %v", err)
	}
	// A lock of a dead process is left to Chromium.
	_ = os.Remove(filepath.Join(o.ProfileDir, "SingletonLock"))
	_ = os.Symlink("host-999999", filepath.Join(o.ProfileDir, "SingletonLock"))
	if _, _, err := b.Conn(context.Background()); err != nil {
		t.Errorf("stale lock %v", err)
	}
}

func TestBuilds(t *testing.T) {
	for _, arch := range []string{"arm64", "amd64"} {
		b, ok := BuildFor(arch)
		if !ok || len(b.SHA256) != 64 || b.Size <= 0 || !strings.HasPrefix(b.URL(), "https://storage.googleapis.com/chromium-browser-snapshots/") {
			t.Errorf("%s: %+v", arch, b)
		}
	}
}
