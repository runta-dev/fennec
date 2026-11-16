// profile-tui-load measures real protocol/PTY echo under CPU and output load.
// It creates only its own bounded child processes; no agent prompt is sent.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fennec/internal/cpusched"
	"fennec/internal/runta"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var role = flag.String("role", "client", "client, server, echo, burn, flood")
var duration = flag.Duration("duration", 30*time.Minute, "loaded measurement duration")
var workers = flag.Int("workers", 2, "CPU workers; use the cloud CPU count")
var terminal = flag.String("terminal", "", "candidate cloud fennec-terminal for workload-exec")
var remote = flag.String("remote", "", "already staged Linux probe path; use the Mac's paired cloud SSH identity")
var budget = flag.Duration("max-added-p95", 50*time.Millisecond, "busy minus idle echo P95 gate")

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if *role == "echo" || *role == "burn" || *role == "flood" {
		if err := checkWorkload(); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration+2*time.Minute)
	defer cancel()
	switch *role {
	case "echo":
		if _, err := term.MakeRaw(int(os.Stdin.Fd())); err != nil {
			return err
		}
		_, _ = os.Stdout.Write([]byte("READY\n"))
		_, err := io.Copy(os.Stdout, os.Stdin)
		return err
	case "burn":
		runtime.GOMAXPROCS(1)
		var data [32]byte
		for ctx.Err() == nil {
			for range 4096 {
				data = sha256.Sum256(data[:])
			}
		}
		return nil
	case "flood":
		rng := rand.New(rand.NewSource(17))
		data := make([]byte, 2048)
		tick := time.NewTicker(time.Second / 16)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
			}
			for i := range data {
				data[i] = byte(32 + rng.Intn(95))
			}
			if _, err := os.Stdout.Write(data); err != nil {
				return err
			}
		}
	case "server":
		input, err := pollable(os.Stdin)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := pollable(os.Stdout)
		if err != nil {
			return err
		}
		defer output.Close()
		home, _ := os.UserHomeDir()
		report := cpusched.Interactive(home)
		if len(report.Errors) != 0 {
			return fmt.Errorf("scheduling degraded: %v", report.Errors)
		}
		if err := checkPlacement("control", -5); err != nil {
			return err
		}
		placementErrors := make(chan error, 1)
		go func() {
			tick := time.NewTicker(5 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if err := checkPlacement("control", -5); err != nil {
						placementErrors <- err
						cancel()
						return
					}
				}
			}
		}()
		exe, _ := os.Executable()
		// An independent wall deadline terminates owned load even if the client
		// loses its connection. Serve also closes and reaps each attachment.
		err = terminalstream.Serve(ctx, input, output, func(ctx context.Context, req cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			if len(req.Arguments) != 1 || req.Arguments[0] != "echo" && req.Arguments[0] != "burn" && req.Arguments[0] != "flood" {
				return cloudruntime.LocalTerminal{}, nil, fmt.Errorf("invalid probe role")
			}
			cmd := exec.Command(*terminal, "workload-exec", exe, "-role", req.Arguments[0], "-duration", duration.String())
			return cloudruntime.LocalTerminal{Address: "t1", State: "live", Agent: "codex"}, cmd, nil
		})
		select {
		case placementErr := <-placementErrors:
			return placementErr
		default:
			return err
		}
	}
	if *role != "client" || *terminal == "" || *duration <= 0 || *workers < 1 || *workers > 64 {
		return fmt.Errorf("invalid probe options")
	}
	exe, _ := os.Executable()
	command := exec.CommandContext(ctx, exe, "-role", "server", "-terminal", *terminal, "-duration", duration.String())
	boundary := "cloud-local protocol + PTY (not agent rendering or WAN)"
	var transport *terminalstream.Transport
	var err error
	if *remote != "" {
		if !strings.HasPrefix(*remote, "/tmp/") || strings.ContainsAny(*remote+*terminal, " '"+"\n\r\t;$`\\") {
			return fmt.Errorf("unsafe staged path")
		}
		home, _ := os.UserHomeDir()
		root := filepath.Join(home, ".config/fennec")
		raw, err := os.ReadFile(filepath.Join(root, "preparation.json"))
		if err != nil {
			return err
		}
		var state struct {
			ID string `json:"runtime_id"`
		}
		if json.Unmarshal(raw, &state) != nil || state.ID == "" {
			return fmt.Errorf("runtime unavailable")
		}
		client, err := connectSSH(ctx, home, root, state.ID)
		if err != nil {
			return err
		}
		defer client.Close()
		stopIO := context.AfterFunc(ctx, func() { client.Close() })
		defer stopIO()
		transport, err = terminalstream.StartSSHContext(ctx, client, *remote+" -role server -terminal "+*terminal+" -duration "+duration.String())
		boundary = fmt.Sprintf("Mac-to-cloud Go SSH (TCP_NODELAY=%t) + protocol + PTY (not agent rendering)", runta.SSHNoDelayEnabled(home))
		if err != nil {
			return err
		}
	} else {
		command.Stderr = os.Stderr
		transport, err = terminalstream.Start(command)
	}
	if err != nil {
		return err
	}
	defer transport.Close()
	if err := transport.Ready(ctx); err != nil {
		return fmt.Errorf("resident ready: %w", err)
	}
	var next int
	launch := func(kind string) (*terminalstream.Session, error) {
		next++
		id := fmt.Sprintf("%032x", next)
		request := cloudruntime.LocalLaunch{ID: id, Agent: "codex", LocalDirectory: "/tmp", Device: "1234abcd", Arguments: []string{kind}}
		return transport.Launch(terminalstream.Frame{Type: "launch", ID: id, Launch: &request, Rows: 24, Columns: 80})
	}
	echo, err := launch("echo")
	if err != nil {
		return err
	}
	var output bytes.Buffer
	readUntil := func(needle []byte) error {
		step, done := context.WithTimeout(ctx, 3*time.Second)
		defer done()
		for !bytes.Contains(output.Bytes(), needle) {
			f, err := echo.Next(step)
			if err != nil {
				return err
			}
			if f.Type == "error" || f.Type == "exit" {
				return fmt.Errorf("echo stopped: %s", f.Message)
			}
			if f.Type == "data" {
				output.Write(f.Data)
				if err := echo.Send(terminalstream.Frame{Type: "credit", Bytes: len(f.Data)}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := readUntil([]byte("READY\n")); err != nil {
		return fmt.Errorf("initial PTY echo: %w", err)
	}
	measure := func(seq int) (float64, error) {
		output.Reset()
		key := []byte(fmt.Sprintf("key-%08d\n", seq))
		started := time.Now()
		if err := echo.Send(terminalstream.Frame{Type: "input", Data: key}); err != nil {
			return 0, err
		}
		if err := readUntil(key); err != nil {
			return 0, err
		}
		if !bytes.Equal(output.Bytes(), key) {
			return 0, fmt.Errorf("input lost, duplicated or reordered")
		}
		return float64(time.Since(started).Microseconds()) / 1000, nil
	}
	var idle, busy, heartbeats []float64
	for i := 0; i < 25; i++ {
		ms, err := measure(i)
		if err != nil {
			return fmt.Errorf("idle echo sample %d: %w", i, err)
		}
		if i >= 5 {
			idle = append(idle, ms)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var bulkBytes atomic.Int64
	loadErr := make(chan error, *workers+2)
	for i := 0; i < *workers+2; i++ {
		kind := "burn"
		if i >= *workers {
			kind = "flood"
		}
		s, err := launch(kind)
		if err != nil {
			return err
		}
		go func() {
			for {
				f, err := s.Next(ctx)
				if err != nil {
					loadErr <- err
					return
				}
				if f.Type == "exit" || f.Type == "error" {
					loadErr <- fmt.Errorf("load child exited early")
					return
				}
				if f.Type == "data" {
					bulkBytes.Add(int64(len(f.Data)))
					if err := s.Send(terminalstream.Frame{Type: "credit", Bytes: len(f.Data)}); err != nil {
						loadErr <- err
						return
					}
				}
			}
		}()
	}
	started := time.Now()
	progress := time.Now()
	for time.Since(started) < *duration {
		select {
		case err := <-loadErr:
			return err
		default:
		}
		ms, err := measure(25 + len(busy))
		if err != nil {
			return fmt.Errorf("loaded echo sample %d after %s, bulk bytes %d: %w", len(busy), time.Since(started), bulkBytes.Load(), err)
		}
		busy = append(busy, ms)
		step, done := context.WithTimeout(ctx, 3*time.Second)
		ping := time.Now()
		err = transport.Probe(step)
		done()
		if err != nil {
			return fmt.Errorf("loaded heartbeat sample %d: %w", len(busy), err)
		}
		heartbeats = append(heartbeats, float64(time.Since(ping).Microseconds())/1000)
		if time.Since(progress) > time.Minute {
			fmt.Fprintf(os.Stderr, "samples=%d elapsed=%s bulk_bytes=%d\n", len(busy), time.Since(started).Round(time.Second), bulkBytes.Load())
			progress = time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}
	p95 := func(values []float64) float64 {
		sort.Float64s(values)
		return values[min(len(values)-1, len(values)*95/100)]
	}
	added := p95(busy) - p95(idle)
	result := map[string]any{"boundary": boundary, "duration_seconds": time.Since(started).Seconds(), "cpu_workers": *workers, "flood_sessions": 2, "bulk_bytes": bulkBytes.Load(), "samples": len(busy), "idle_echo_p95_ms": p95(idle), "loaded_echo_p95_ms": p95(busy), "added_echo_p95_ms": added, "heartbeat_p95_ms": p95(heartbeats), "unexpected_disconnects": 0, "passed": added <= float64(budget.Microseconds())/1000}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if result["passed"] != true {
		return fmt.Errorf("added echo P95 exceeds %s", *budget)
	}
	return nil
}

// Use the same TCP_NODELAY policy and Go SSH implementation as production.
// Plain OpenSSH -T leaves Nagle buffering enabled and is a different baseline.
// This diagnostic reuses the already paired identity and pinned known_hosts;
// no key enrollment, host-key acceptance or authentication fallback occurs.
func connectSSH(ctx context.Context, home, root, id string) (*ssh.Client, error) {
	key, err := os.ReadFile(filepath.Join(root, "cloud/runtime_key"))
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	verify, err := knownhosts.New(filepath.Join(root, "cloud/known_hosts"))
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(runta.ActiveSSHHost(home), "22")
	raw, err := (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(runta.SSHNoDelayEnabled(home))
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{User: id, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify})
	if err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return ssh.NewClient(conn, channels, requests), nil
}

func pollable(file *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

// Check the exec boundary and every existing Go thread, not merely the
// parent's requested settings. All role children inherit the workload group.
func checkWorkload() error {
	return checkPlacement("workload", 0)
}

func checkPlacement(expectedGroup string, expectedNice int) error {
	group, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !strings.Contains(string(group), "0::/fennec/"+expectedGroup+"\n") {
		return fmt.Errorf("probe escaped %s group: %v", expectedGroup, err)
	}
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return err
	}
	for _, task := range tasks {
		data, err := os.ReadFile("/proc/self/task/" + task.Name() + "/stat")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			return fmt.Errorf("invalid task stat")
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 17 {
			return fmt.Errorf("short task stat")
		}
		nice, err := strconv.Atoi(fields[16])
		if err != nil || nice != expectedNice {
			return fmt.Errorf("%s thread priority %s, expected %d", expectedGroup, fields[16], expectedNice)
		}
	}
	return nil
}
