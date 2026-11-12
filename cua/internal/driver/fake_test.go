package driver

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary doubles as a fake cua-driver: with fakeEnv set it runs
// fakeMain instead of the tests. Marker files beside the socket steer it:
//
//	S.fail       serve exits 3 before listening
//	S.nolisten   serve never listens
//	S.slow       serve waits 3 s before listening
//	S.hang       stop never answers
//	S.ignoreterm both driver processes ignore TERM
//
// It records S.starts (one x per start), S.fake-pid (the serving process),
// S.argv and S.env. Like a driver that forks, serve runs the server in a
// child process and waits for it.
const (
	fakeEnv   = "FENNEC_FAKE_CUA_DRIVER"
	fakeChild = "FENNEC_FAKE_CUA_DRIVER_CHILD"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		os.Exit(fakeMain(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func fakeMain(args []string) int {
	if len(args) == 0 {
		return 2
	}
	sock := flagValue(args, "--socket")
	switch args[0] {
	case "stop":
		if exists(sock + ".hang") {
			time.Sleep(30 * time.Second)
		}
		data, err := os.ReadFile(sock + ".fake-pid")
		if err != nil {
			return 1
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		_ = syscall.Kill(pid, syscall.SIGTERM)
		return 0
	case "serve":
	default:
		return 2
	}
	if exists(sock + ".ignoreterm") {
		signal.Ignore(syscall.SIGTERM)
	}
	if os.Getenv(fakeChild) != "1" {
		f, _ := os.OpenFile(sock+".starts", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		_, _ = f.WriteString("x")
		_ = f.Close()
		if exists(sock + ".fail") {
			return 3
		}
		self, _ := os.Executable()
		cmd := exec.Command(self, args...)
		cmd.Env = append(os.Environ(), fakeChild+"=1")
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			return 1
		}
		return 0
	}
	pid := strconv.Itoa(os.Getpid())
	_ = os.WriteFile(sock+".fake-pid", []byte(pid), 0o600)
	_ = os.WriteFile(sock+".argv", []byte(strings.Join(args, " ")), 0o600)
	var env []string
	for _, k := range []string{EnvTelemetry, EnvEmbedded, EnvConsentAlert, EnvWindowWatch} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	_ = os.WriteFile(sock+".env", []byte(strings.Join(env, "\n")), 0o600)
	if p := flagValue(args, "--pid-file"); p != "" {
		_ = os.WriteFile(p, []byte(pid), 0o600)
	}
	if exists(sock + ".slow") {
		time.Sleep(3 * time.Second)
	}
	if exists(sock + ".nolisten") {
		time.Sleep(time.Hour)
	}
	if os.Getenv(EnvEmbedded) == "1" && exists(sock) {
		return 4 // an embedded driver will not start over a leftover socket
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return 5
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return 6
		}
		go func() {
			defer conn.Close()
			r := bufio.NewReader(conn)
			for {
				if _, err := r.ReadBytes('\n'); err != nil {
					return
				}
				reply, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"pid": os.Getpid()}})
				_, _ = conn.Write(append(reply, '\n'))
			}
		}()
	}
}

// fakeDir is a short directory: unix socket paths are limited.
func fakeDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "drv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeSupervisor is a supervisor of the fake driver on a fresh socket.
func fakeSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Supervisor{Bin: self, Socket: filepath.Join(fakeDir(t), "driver.sock"), Env: []string{fakeEnv + "=1"}}
}

func fakePID(sock string) int {
	data, err := os.ReadFile(sock + ".fake-pid")
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// until polls cond every 50 ms for up to d.
func until(d time.Duration, cond func() bool) bool {
	give := time.Now().Add(d)
	for time.Now().Before(give) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

func gone(pid int) bool { return until(5*time.Second, func() bool { return !alive(pid) }) }

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}
