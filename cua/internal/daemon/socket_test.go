package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"fennec/cua/internal/mcpserver"
)

// realTempDir is a short temporary directory with no link on its path
// (/tmp is a link on macOS), as a shared socket requires.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cua")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// runDaemon starts d and waits until it listens.
func runDaemon(t *testing.T, d *Daemon) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d.Ready = make(chan struct{})
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()
	select {
	case <-d.Ready:
	case err := <-errc:
		cancel()
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("daemon did not start")
	}
	t.Cleanup(func() { cancel(); <-errc })
}

func mode(t *testing.T, path string) (os.FileMode, uint32) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	m := fi.Mode().Perm()
	if fi.Mode()&os.ModeSocket != 0 {
		m |= os.ModeSocket
	}
	return m, fi.Sys().(*syscall.Stat_t).Gid
}

// ownGroup is a group this user is in: its primary group, by name.
func ownGroup(t *testing.T) (string, int) {
	t.Helper()
	gid := os.Getgid()
	g, err := user.LookupGroupId(strconv.Itoa(gid))
	if err != nil {
		t.Skipf("primary group %d has no name: %v", gid, err)
	}
	return g.Name, gid
}

func TestSocketModes(t *testing.T) {
	// Private: both sockets and the lock 0600, whatever the umask.
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	eng := newEngine(nil)
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper})
	for _, s := range []string{p.MCP, p.Control} {
		if m, _ := mode(t, s); m != os.ModeSocket|0o600 {
			t.Errorf("%s: %v", filepath.Base(s), m)
		}
	}
	if m, _ := mode(t, p.Lock); m != 0o600 {
		t.Errorf("lock: %v", m)
	}

	// Shared: the agent socket 0660 owned by the group, the control socket
	// and the lock still 0600.
	name, gid := ownGroup(t)
	p = PathsIn(filepath.Join(realTempDir(t), "d"))
	runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, SocketGroup: name})
	if m, g := mode(t, p.MCP); m != os.ModeSocket|0o660 || g != uint32(gid) {
		t.Errorf("shared agent socket: %v group %d", m, g)
	}
	if m, _ := mode(t, p.Control); m != os.ModeSocket|0o600 {
		t.Errorf("shared control socket: %v", m)
	}
	if m, _ := mode(t, p.Lock); m != 0o600 {
		t.Errorf("shared lock: %v", m)
	}
	// A stale lock file with a loose mode is tightened.
	p = PathsIn(filepath.Join(realTempDir(t), "d"))
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Lock, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper})
	if m, _ := mode(t, p.Lock); m != 0o600 {
		t.Errorf("an existing lock stays %v", m)
	}
}

func TestSocketGroupMode(t *testing.T) {
	name, gid := ownGroup(t)
	eng := newEngine(nil)
	for _, group := range []string{name, strconv.Itoa(gid)} {
		p := PathsIn(filepath.Join(realTempDir(t), "d"))
		runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, SocketGroup: group})
		if m, g := mode(t, p.MCP); m != os.ModeSocket|0o660 || g != uint32(gid) {
			t.Errorf("socket_group %q: %v group %d", group, m, g)
		}
		// Its members can use it: a tool call is answered.
		c := dialMCP(t, p)
		if r, err := c.Tool(context.Background(), "status", nil); err != nil || r.IsError {
			t.Errorf("status over the shared socket: %v %v", r, err)
		}
	}
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	runDaemon(t, &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper})
	if m, _ := mode(t, p.MCP); m != os.ModeSocket|0o600 {
		t.Errorf("unset socket_group: %v", m)
	}
}

func TestBindRefusesPlantedLink(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cua.sock")
	beforeVerify = func(tmp string) {
		_ = os.Remove(tmp)
		_ = os.Symlink(target, tmp)
	}
	defer func() { beforeVerify = nil }()
	ln, err := listen(path, 0o660, os.Getgid())
	if err == nil {
		_ = ln.Close()
		t.Fatal("a link planted at the bind name was accepted")
	}
	if !strings.Contains(err.Error(), "not the socket just bound") {
		t.Errorf("err %v", err)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o644 {
		t.Errorf("the link's target was changed to %v", fi.Mode().Perm())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Error("something was renamed into place")
	}
	if _, err := os.Lstat(path + ".t"); !os.IsNotExist(err) {
		t.Error("the bind name was left behind")
	}

	// Without interference, the socket appears only under its final name.
	beforeVerify = nil
	ln, err = listen(path, 0o600, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if m, _ := mode(t, path); m != os.ModeSocket|0o600 {
		t.Errorf("bound %v", m)
	}
	if _, err := os.Lstat(path + ".t"); !os.IsNotExist(err) {
		t.Error("the temporary name remains")
	}
}

func TestEndpointLengthLimit(t *testing.T) {
	base := realTempDir(t)
	pad := MaxSocketPath - len(filepath.Join(base, "x", "control.sock"))
	if pad < 1 {
		t.Skip("temporary directory too long")
	}
	// The longest allowed endpoint starts; one byte more fails at start,
	// naming the limit, with nothing created.
	ok := PathsIn(filepath.Join(base, strings.Repeat("x", pad+1)))
	if err := ok.Check(); err != nil {
		t.Fatalf("%d bytes refused: %v", len(ok.Control), err)
	}
	eng := newEngine(nil)
	runDaemon(t, &Daemon{Paths: ok, Handler: eng, Stopper: eng.Stopper})
	long := PathsIn(filepath.Join(base, strings.Repeat("y", pad+2)))
	err := (&Daemon{Paths: long, Handler: eng, Stopper: eng.Stopper}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "at most 101") || !strings.Contains(err.Error(), long.Control) {
		t.Errorf("err %v", err)
	}
	if _, err := os.Stat(long.Dir); !os.IsNotExist(err) {
		t.Error("a refused endpoint created its directory")
	}
}

func TestEndpointDirMode(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	name, _ := ownGroup(t)
	eng := newEngine(nil)
	shared := PathsIn(filepath.Join(realTempDir(t), "d"))
	for i := 0; i < 2; i++ { // the second start accepts the directory it made
		ctx, cancel := context.WithCancel(context.Background())
		d := &Daemon{Paths: shared, Handler: eng, Stopper: eng.Stopper, SocketGroup: name, Ready: make(chan struct{})}
		errc := make(chan error, 1)
		go func() { errc <- d.Run(ctx) }()
		select {
		case <-d.Ready:
		case err := <-errc:
			t.Fatalf("start %d: %v", i, err)
		}
		if m, _ := mode(t, shared.Dir); m != 0o755 {
			t.Errorf("start %d: shared dir %v under umask 077", i, m)
		}
		cancel()
		<-errc
	}
	syscall.Umask(0)
	private := PathsIn(filepath.Join(realTempDir(t), "d"))
	runDaemon(t, &Daemon{Paths: private, Handler: eng, Stopper: eng.Stopper})
	if m, _ := mode(t, private.Dir); m != 0o700 {
		t.Errorf("private dir %v under umask 0", m)
	}
}

func TestSharedSocketDirChecks(t *testing.T) {
	const own = 501
	type ent = PathEntry
	fs := func(m map[string]ent) func(string) (PathEntry, error) {
		return func(p string) (PathEntry, error) {
			if e, ok := m[p]; ok {
				return e, nil
			}
			return PathEntry{}, fmt.Errorf("stat %s: no such file", p)
		}
	}
	good := map[string]ent{
		"/":                   {UID: 0, Mode: 0o755},
		"/Users":              {UID: 0, Mode: 0o755},
		"/Users/me":           {UID: own, Mode: 0o750},
		"/Users/me/cua":       {UID: own, Mode: 0o755},
		"/tmp":                {UID: 0, Mode: 0o777 | os.ModeSticky},
		"/tmp/cua":            {UID: own, Mode: 0o755},
		"/srv":                {UID: 0, Mode: 0o777},
		"/srv/cua":            {UID: own, Mode: 0o755},
		"/opt":                {UID: 77, Mode: 0o755},
		"/opt/cua":            {UID: own, Mode: 0o755},
		"/Users/me/link":      {UID: own, Mode: 0o777, Link: true},
		"/Users/me/link/cua":  {UID: own, Mode: 0o755},
		"/Users/me/open":      {UID: own, Mode: 0o775},
		"/Users/me/cua/other": {UID: 600, Mode: 0o755},
	}
	if err := checkSharedDir("/Users/me/cua", own, fs(good)); err != nil {
		t.Errorf("a private path: %v", err)
	}
	if err := checkSharedDir("/tmp/cua", own, fs(good)); err != nil {
		t.Errorf("under a sticky world-writable parent: %v", err)
	}
	for _, c := range []struct{ dir, want string }{
		{"cua", "absolute"},
		{"/Users/me/link/cua", "/Users/me/link is a link"},
		{"/Users/me/open", "/Users/me/open owned by this user (uid 501) and writable by no one else; it is uid 501 mode 0775"},
		{"/Users/me/cua/other", "it is uid 600 mode 0755"},
		{"/srv/cua", "/srv is uid 0 mode 0777"},
		{"/opt/cua", "/opt is uid 77 mode 0755"},
	} {
		err := checkSharedDir(c.dir, own, fs(good))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.dir, err, c.want)
		}
	}

	// The real check over the real file system, and a daemon refusing a
	// directory its group can write.
	dir := realTempDir(t)
	if err := CheckSharedDir(dir, uint32(os.Getuid())); err != nil {
		t.Errorf("a private temporary directory: %v", err)
	}
	if err := CheckSharedDir(dir, uint32(os.Getuid())+1); err == nil {
		t.Error("a directory of another user passed")
	}
	if err := CheckSharedDir("/tmp", uint32(os.Getuid())); err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("/tmp is a link on macOS: %v", err)
	}
	name, _ := ownGroup(t)
	eng := newEngine(nil)
	parent := filepath.Join(dir, "open")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	p := PathsIn(filepath.Join(parent, "d"))
	err := (&Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, SocketGroup: name}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), parent) || !strings.Contains(err.Error(), "mode 0777") {
		t.Errorf("a world-writable parent: %v", err)
	}
	if _, err := os.Lstat(p.MCP); !os.IsNotExist(err) {
		t.Error("a refused start bound its socket")
	}
}

func TestSocketGroupResolution(t *testing.T) {
	name, gid := ownGroup(t)
	if got, err := ResolveGroup(name); err != nil || got != gid {
		t.Errorf("%q = %d %v", name, got, err)
	}
	if got, err := ResolveGroup(" " + strconv.Itoa(gid) + " "); err != nil || got != gid {
		t.Errorf("numeric = %d %v", got, err)
	}
	for _, bad := range []string{"no-such-group-fennec-cua", "12x", "99999999999", "a:b", "-1", ""} {
		_, err := ResolveGroup(bad)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", bad)) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// An unknown group fails the start with its name, before binding.
	eng := newEngine(nil)
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	err := (&Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, SocketGroup: "no-such-group-fennec-cua"}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no-such-group-fennec-cua") {
		t.Errorf("start with an unknown group: %v", err)
	}
	if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
		t.Error("a refused group created the endpoint")
	}
}

func TestSecondDaemonRefusedWhileStarting(t *testing.T) {
	eng := newEngine(nil)
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	starting := make(chan struct{})
	proceed := make(chan struct{})
	first := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, Ready: make(chan struct{}), Starting: func(context.Context) error {
		close(starting)
		<-proceed
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- first.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-errc })
	<-starting
	second := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper}
	err := second.Run(context.Background())
	if !errors.Is(err, ErrRunning) || !strings.Contains(err.Error(), p.MCP) {
		t.Fatalf("second daemon while the first starts: %v", err)
	}
	for _, s := range []string{p.MCP, p.Control, p.MCP + ".t", p.Control + ".t"} {
		if _, err := os.Lstat(s); !os.IsNotExist(err) {
			t.Errorf("%s exists after the refused start", s)
		}
	}
	close(proceed)
	<-first.Ready
	c := dialMCP(t, p)
	if _, err := c.Tool(context.Background(), "status", nil); err != nil {
		t.Errorf("the winner does not serve: %v", err)
	}
}

func TestConcurrentStartOneWinner(t *testing.T) {
	eng := newEngine(nil)
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	ctx, cancel := context.WithCancel(context.Background())
	const n = 6
	var mu sync.Mutex
	var winners int
	var losers []error
	ready := make(chan struct{}, n)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, Ready: make(chan struct{})}
			go func() {
				select {
				case <-d.Ready:
					mu.Lock()
					winners++
					mu.Unlock()
					ready <- struct{}{}
				case <-ctx.Done():
				}
			}()
			<-gate
			if err := d.Run(ctx); err != nil {
				mu.Lock()
				losers = append(losers, err)
				mu.Unlock()
				ready <- struct{}{}
			}
		}()
	}
	close(gate)
	for i := 0; i < n; i++ {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("starts did not settle")
		}
	}
	mu.Lock()
	if winners != 1 || len(losers) != n-1 {
		t.Errorf("%d winners, %d losers", winners, len(losers))
	}
	for _, err := range losers {
		if !errors.Is(err, ErrRunning) || !strings.Contains(err.Error(), p.MCP) {
			t.Errorf("loser: %v", err)
		}
	}
	mu.Unlock()
	c := dialMCP(t, p)
	if _, err := c.Tool(context.Background(), "status", nil); err != nil {
		t.Errorf("the winner does not serve: %v", err)
	}
	cancel()
	wg.Wait()
}

// failingAcceptor fails its first accepts, then reports closed.
type failingAcceptor struct {
	mu    sync.Mutex
	fails int
	times []time.Time
}

func (f *failingAcceptor) AcceptUnix() (*net.UnixConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.times = append(f.times, time.Now())
	if len(f.times) <= f.fails {
		return nil, &net.OpError{Op: "accept", Net: "unix", Err: os.NewSyscallError("accept", syscall.EMFILE)}
	}
	return nil, net.ErrClosed
}

func TestAcceptRetry(t *testing.T) {
	f := &failingAcceptor{fails: 4}
	var logged []string
	d := &Daemon{}
	d.Log = newTestLogger(&logged)
	done := make(chan struct{})
	go func() {
		d.accept(context.Background(), f, func(context.Context, *net.UnixConn, uint32, uint32) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("accept gave up or spun forever")
	}
	if len(f.times) != 5 {
		t.Fatalf("%d accepts", len(f.times))
	}
	// Each retry waits longer than the one before, so a descriptor
	// shortage never spins.
	for i := 1; i < 4; i++ {
		if gap := f.times[i].Sub(f.times[i-1]); gap < acceptRetryFirst {
			t.Errorf("retry %d after %v", i, gap)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "retrying") {
		t.Errorf("logged %q", logged)
	}
	// A stopping daemon ends the retry loop at once.
	ctx, cancel := context.WithCancel(context.Background())
	f = &failingAcceptor{fails: 1 << 30}
	done = make(chan struct{})
	go func() { d.accept(ctx, f, nil); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled daemon kept retrying")
	}
}

func TestOwnProcessGroup(t *testing.T) {
	attr := DetachedAttr()
	if !attr.Setsid {
		t.Fatal("the daemon would share the client's session")
	}
	cmd := exec.Command("/bin/sleep", "5")
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := unix.Getsid(pid)
	if err != nil {
		t.Fatal(err)
	}
	ownSid, _ := unix.Getsid(0)
	if pgid != pid || pgid == syscall.Getpgrp() || sid != pid || sid == ownSid {
		t.Errorf("child pgid %d sid %d; ours pgid %d sid %d", pgid, sid, syscall.Getpgrp(), ownSid)
	}
}

func TestListenBeforeModels(t *testing.T) {
	eng := newEngine(nil)
	p := PathsIn(filepath.Join(realTempDir(t), "d"))
	building := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	d := &Daemon{Paths: p, Handler: eng, Stopper: eng.Stopper, Info: mcpserver.Info{Name: "t"}, Serving: func(ctx context.Context) {
		close(building)
		select { // a slow model client build
		case <-release:
		case <-ctx.Done():
		}
	}}
	runDaemon(t, d)
	<-building
	c := dialMCP(t, p)
	start := time.Now()
	r, err := c.Tool(context.Background(), "status", nil)
	if err != nil || r.IsError {
		t.Fatalf("status during model setup: %v %v", r, err)
	}
	if time.Since(start) > time.Second {
		t.Error("the first answer waited for model setup")
	}
	if _, err := Control(context.Background(), p, "status"); err != nil {
		t.Errorf("control during model setup: %v", err)
	}
}
