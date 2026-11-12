package driver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// startFake starts s and stops it when the test ends.
func startFake(t *testing.T, s *Supervisor) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); s.Wait() })
	return cancel
}

func TestDriverRestart(t *testing.T) {
	s := fakeSupervisor(t)
	stop := startFake(t, s)
	if !answers(s.Socket) {
		t.Fatal("the driver does not listen on its own socket after Start")
	}
	first := fakePID(s.Socket)
	_ = syscall.Kill(first, syscall.SIGKILL)
	if !gone(first) {
		t.Fatal("the killed driver lives on")
	}
	var second int
	if !until(5*time.Second, func() bool {
		second = fakePID(s.Socket)
		return second != first && answers(s.Socket)
	}) {
		t.Fatal("the driver did not come back")
	}
	stop()
	s.Wait()
	if !gone(second) {
		t.Error("the driver outlived its supervisor")
	}
}

func TestRestartsDriverOnExit(t *testing.T) {
	s := fakeSupervisor(t)
	startFake(t, s)
	for i := 0; i < 2; i++ {
		pid := fakePID(s.Socket)
		_ = syscall.Kill(pid, syscall.SIGTERM)
		if !until(5*time.Second, func() bool { p := fakePID(s.Socket); return p != pid && answers(s.Socket) }) {
			t.Fatalf("exit %d: the driver was not restarted", i+1)
		}
	}
	if n := len(readFile(s.Socket + ".starts")); n != 3 {
		t.Errorf("%d starts, want 3", n)
	}
}

func readFile(p string) string {
	data, _ := os.ReadFile(p)
	return string(data)
}

func TestDriverBackoff(t *testing.T) {
	var b Backoff
	var got []time.Duration
	for i := 0; i < 8; i++ {
		got = append(got, b.After(time.Second))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("delays %v", got)
		}
	}
	if d := b.After(30 * time.Second); d != time.Second {
		t.Errorf("after a 30 s run the delay is %s, want 1s", d)
	}
	if d := b.After(time.Second); d != 2*time.Second {
		t.Errorf("then %s, want 2s", d)
	}

	// A driver that keeps failing is started again after those delays.
	s := fakeSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var delays []time.Duration
	s.pause = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		delays = append(delays, d)
		if len(delays) == 4 {
			cancel()
			return false
		}
		return ctx.Err() == nil
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	touch(t, s.Socket+".fail")
	_ = syscall.Kill(fakePID(s.Socket), syscall.SIGKILL)
	s.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(delays) != 4 || delays[0] != time.Second || delays[1] != 2*time.Second || delays[2] != 4*time.Second || delays[3] != 8*time.Second {
		t.Errorf("restart delays %v", delays)
	}
	if n := len(readFile(s.Socket + ".starts")); n != 4 {
		t.Errorf("%d starts, want the first and three failing restarts", n)
	}
}

func TestDriverStartFails(t *testing.T) {
	// No bundled driver: nothing starts.
	if err := (&Supervisor{Socket: "/tmp/none.sock"}).Start(context.Background()); !errors.Is(err, ErrNoDriver) {
		t.Errorf("without a driver: %v", err)
	}
	dir := fakeDir(t)
	if BundledBin(filepath.Join(dir, "fennec-cua")) != "" {
		t.Error("a missing bundled driver was found")
	}
	if err := os.WriteFile(filepath.Join(dir, BinName), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if BundledBin(filepath.Join(dir, "fennec-cua")) != filepath.Join(dir, BinName) {
		t.Error("the bundled driver beside the binary was not found")
	}

	// Exits before listening.
	s := fakeSupervisor(t)
	touch(t, s.Socket+".fail")
	err := s.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), "before it listened") {
		t.Errorf("exit before listening: %v", err)
	}

	// Never listens: the start fails at the bound and leaves no driver.
	s = fakeSupervisor(t)
	s.ListenWait = 400 * time.Millisecond
	touch(t, s.Socket+".nolisten")
	start := time.Now()
	err = s.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not listen") {
		t.Fatalf("no listen: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("the start took %s", time.Since(start))
	}
	if pid := fakePID(s.Socket); !gone(pid) {
		t.Error("a driver that never listened was left running")
	}
	if ListenWait != 10*time.Second {
		t.Errorf("default listen wait %s", ListenWait)
	}
}

func TestDriverStop(t *testing.T) {
	// The driver's stop command never answers: it is killed anyway,
	// together with the process it forked.
	s := fakeSupervisor(t)
	touch(t, s.Socket+".ignoreterm")
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	server := fakePID(s.Socket)
	touch(t, s.Socket+".hang")
	start := time.Now()
	cancel()
	s.Wait()
	if took := time.Since(start); took < stopGrace-100*time.Millisecond || took > stopGrace+2*time.Second {
		t.Errorf("stop took %s, want the %s grace", took, stopGrace)
	}
	if !gone(server) {
		t.Error("the forked driver outlived the stop")
	}

	// A stop while the driver restarts leaves no half-started driver.
	s = fakeSupervisor(t)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first := fakePID(s.Socket)
	touch(t, s.Socket+".slow")
	touch(t, s.Socket+".hang")
	_ = syscall.Kill(first, syscall.SIGKILL)
	var second int
	if !until(5*time.Second, func() bool { second = fakePID(s.Socket); return second != first && second != 0 }) {
		t.Fatal("the driver was not restarting")
	}
	cancel()
	s.Wait()
	if !gone(second) {
		t.Error("the half-started driver outlived the stop")
	}
}

// leaveDriver starts a fake driver on sock as a killed daemon would have
// left it.
func leaveDriver(t *testing.T, s *Supervisor) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(s.Bin, s.Args()...)
	cmd.Env = append(os.Environ(), s.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	if !until(5*time.Second, func() bool { return answers(s.Socket) }) {
		t.Fatal("the left driver did not listen")
	}
	return cmd
}

func TestDriverStale(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		s := fakeSupervisor(t)
		s.Embedded = embedded
		leaveDriver(t, s)
		stale := fakePID(s.Socket)
		startFake(t, s)
		if !gone(stale) {
			t.Errorf("embedded %v: the left driver was not stopped", embedded)
		}
		if p := fakePID(s.Socket); p == stale || !answers(s.Socket) {
			t.Errorf("embedded %v: the left driver was not replaced", embedded)
		}
	}
}

func TestStaleSocketOnly(t *testing.T) {
	dir := fakeDir(t)
	live := filepath.Join(dir, "live.sock")
	dead := filepath.Join(dir, "dead.sock")
	file := filepath.Join(dir, "file.sock")
	link := filepath.Join(dir, "link.sock")
	deadLink := filepath.Join(dir, "deadlink.sock")
	missing := filepath.Join(dir, "missing.sock")
	ln, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	deadSocket(t, dead)
	touch(t, file)
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.sock")
	deadSocket(t, other)
	if err := os.Symlink(other, deadLink); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{live, dead, file, link, deadLink, missing} {
		ClearStaleSocket(p)
	}
	if !exists(live) || exists(dead) || !exists(file) || !exists(link) || !exists(deadLink) || exists(missing) {
		t.Errorf("live %v dead %v file %v link %v deadlink %v", exists(live), exists(dead), exists(file), exists(link), exists(deadLink))
	}
}

// deadSocket leaves a socket file nobody listens on.
func deadSocket(t *testing.T, p string) {
	t.Helper()
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
}

func TestRemovesDeadSocket(t *testing.T) {
	// An embedded fake refuses to start over a leftover socket, so it
	// starts only when the dead socket was removed first, on the first
	// start and on a restart.
	s := fakeSupervisor(t)
	s.Embedded = true
	deadSocket(t, s.Socket)
	startFake(t, s)
	first := fakePID(s.Socket)
	_ = syscall.Kill(first, syscall.SIGKILL) // leaves its socket behind
	if !until(5*time.Second, func() bool { p := fakePID(s.Socket); return p != first && answers(s.Socket) }) {
		t.Fatal("the driver did not restart over its dead socket")
	}
	// A socket that answers is left alone.
	if ClearStaleSocket(s.Socket) || !answers(s.Socket) {
		t.Error("a live socket was removed")
	}
}

func TestDriverArgs(t *testing.T) {
	var logs bytes.Buffer
	s := fakeSupervisor(t)
	s.OverlayOff = true
	s.Log = log.New(&logs, "", 0)
	want := "serve --socket " + s.Socket + " --pid-file " + strings.TrimSuffix(s.Socket, ".sock") + ".pid --glide-ms 1"
	if got := strings.Join(s.Args(), " "); got != want {
		t.Errorf("args %q", got)
	}
	startFake(t, s)
	argv := readFile(s.Socket + ".argv")
	if argv != want || strings.Contains(argv, "--no-overlay") {
		t.Errorf("the driver ran with %q", argv)
	}
	if !strings.Contains(logs.String(), "overlay = false has no effect on macOS") {
		t.Errorf("no overlay warning: %q", logs.String())
	}
	if pid := readFile(strings.TrimSuffix(s.Socket, ".sock") + ".pid"); pid == "" {
		t.Error("no pid file")
	}
}

func envOf(s *Supervisor) map[string]string {
	m := map[string]string{}
	for _, kv := range s.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestDriverEnv(t *testing.T) {
	cases := []struct {
		embedded, foreground bool
		consent, watch       string
	}{
		{false, false, "1", ""},
		{false, true, "1", "50"},
		{true, false, "", ""},
		{true, true, "", "50"},
	}
	for _, c := range cases {
		s := &Supervisor{Socket: "/tmp/x.sock", Embedded: c.embedded, Foreground: c.foreground}
		env := envOf(s)
		if env[EnvTelemetry] != "false" || env[EnvConsentAlert] != c.consent || env[EnvWindowWatch] != c.watch {
			t.Errorf("%+v: %v", c, env)
		}
		if _, ok := env[EnvEmbedded]; ok != c.embedded {
			t.Errorf("%+v: embedded flag %v", c, env)
		}
	}
	// The driver process sees it.
	s := fakeSupervisor(t)
	s.Foreground = true
	startFake(t, s)
	got := readFile(s.Socket + ".env")
	for _, kv := range []string{EnvTelemetry + "=false", EnvConsentAlert + "=1", EnvWindowWatch + "=50"} {
		if !strings.Contains(got, kv) {
			t.Errorf("driver env lacks %s: %q", kv, got)
		}
	}
	if strings.Contains(got, EnvEmbedded) {
		t.Errorf("an owned driver was told it is embedded: %q", got)
	}
}

func TestBundledDriverChecksum(t *testing.T) {
	dir := fakeDir(t)
	bin := filepath.Join(dir, BinName)
	content := []byte("a cua-driver release")
	if err := os.WriteFile(bin, content, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	pin := hex.EncodeToString(sum[:])
	if err := VerifyBinary(bin, strings.ToUpper(pin)); err != nil {
		t.Errorf("matching pin: %v", err)
	}
	other := sha256.Sum256([]byte("another build"))
	err := VerifyBinary(bin, hex.EncodeToString(other[:]))
	if err == nil || !strings.Contains(err.Error(), pin) || !strings.Contains(err.Error(), "pinned release") {
		t.Errorf("mismatch: %v", err)
	}
	if err := VerifyBinary(bin, "abc"); err == nil {
		t.Error("a malformed pin passed")
	}
	if err := VerifyBinary(filepath.Join(dir, "missing"), pin); err == nil {
		t.Error("a missing binary passed")
	}
}

func TestCallWaitsForRestart(t *testing.T) {
	s := fakeSupervisor(t)
	startFake(t, s)
	c := &Client{Socket: s.Socket}
	defer c.Close()
	first := fakePID(s.Socket)
	_ = syscall.Kill(first, syscall.SIGKILL)
	if !gone(first) {
		t.Fatal("the driver did not die")
	}
	c.EndLane(LaneInput)
	start := time.Now()
	res, err := c.Call(context.Background(), LaneInput, "get_screen_size", nil)
	if err != nil {
		t.Fatalf("a call during the restart failed: %v", err)
	}
	if !strings.Contains(string(res), `"pid"`) || strings.Contains(string(res), `"pid":`+strconv.Itoa(first)) {
		t.Errorf("answered by %s", res)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("answered before the restart")
	}
}
