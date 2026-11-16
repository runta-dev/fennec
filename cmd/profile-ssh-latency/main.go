// profile-ssh-latency compares exact resident pings, ordinary guest echo and
// gateway keepalive on one authenticated WAN SSH connection. It never launches
// an agent or changes the installed application.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fennec/internal/runta"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

var role = flag.String("role", "client", "client, server, or echo")
var remote = flag.String("remote", "", "absolute path to this probe staged on the cloud")
var terminal = flag.String("terminal", "", "optional installed/staged terminal binary for idle-only measurements")
var count = flag.Int("samples", 60, "samples per phase")
var loaded = flag.Bool("load", true, "also measure sustained large resident replies from the probe server")
var deadline = flag.Duration("timeout", 10*time.Minute, "total bound for the probe and its owned server")

const blobSize = 256 << 10

// Deterministic bytes let the receiver verify every complete load reply.
func payload() json.RawMessage {
	b := make([]byte, blobSize)
	for i := range b {
		b[i] = byte(33 + (i*31+i/89)%90)
	}
	raw, _ := json.Marshal(struct {
		Data []byte `json:"data"`
	}{b})
	return raw
}

type sample struct {
	Phase     string    `json:"phase"`
	Index     int       `json:"index"`
	Started   time.Time `json:"started_utc"`
	PingMS    float64   `json:"ping_ms"`
	EchoMS    float64   `json:"echo_ms"`
	GatewayMS float64   `json:"gateway_ms"`
}
type report struct {
	Boundary     string                             `json:"boundary"`
	Started      time.Time                          `json:"started_utc"`
	Duration     float64                            `json:"duration_seconds"`
	Packets      bool                               `json:"traffic_packets"`
	Samples      []sample                           `json:"samples"`
	Summary      map[string]map[string]distribution `json:"summary"`
	LoadBytes    int64                              `json:"verified_load_bytes"`
	LoadDuration float64                            `json:"load_seconds"`
	Error        string                             `json:"error,omitempty"`
}
type distribution struct {
	Count  int     `json:"count"`
	Min    float64 `json:"min_ms"`
	Median float64 `json:"median_ms"`
	P95    float64 `json:"p95_ms"`
	Max    float64 `json:"max_ms"`
}

func summarize(v []float64) distribution {
	sort.Float64s(v)
	if len(v) == 0 {
		return distribution{}
	}
	return distribution{len(v), v[0], v[len(v)/2], v[(len(v)*95+99)/100-1], v[len(v)-1]}
}
func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if *deadline <= 0 || *deadline > 30*time.Minute || *count < 1 || *count > 1000 {
		return fmt.Errorf("invalid probe bounds")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *deadline)
	defer cancel()
	switch *role {
	case "echo":
		// Process exit is also bounded when the parent/client disappears.
		context.AfterFunc(ctx, func() { os.Exit(70) })
		_, err := io.Copy(os.Stdout, os.Stdin)
		return err
	case "server":
		context.AfterFunc(ctx, func() { os.Exit(70) })
		data := payload()
		return terminalstream.ServeWithOperations(ctx, os.Stdin, os.Stdout, nil, nil, nil, func(_ context.Context, op cloudruntime.LocalOperation) (json.RawMessage, error) {
			if op.Kind != "list" {
				return nil, fmt.Errorf("unsupported probe operation")
			}
			return data, nil
		})
	case "client":
	default:
		return fmt.Errorf("invalid role")
	}
	if !safePath(*remote) || *terminal != "" && !safePath(*terminal) || *terminal != "" && *loaded {
		return fmt.Errorf("use a staged absolute remote path; terminal override requires -load=false")
	}
	result := report{Boundary: "One authenticated Go SSH connection; exact matched resident ping through production writer, equal-length guest echo and gateway keepalive, rotated sequential order. Synthetic bulk replies are resident-only; no aggregate SSH coverage claimed.", Started: time.Now().UTC()}
	err := measure(ctx, &result)
	result.Duration = time.Since(result.Started).Seconds()
	if err != nil {
		result.Error = err.Error()
	}
	result.Summary = make(map[string]map[string]distribution)
	for _, phase := range []string{"idle", "bulk", "recovery"} {
		var p, e, g []float64
		for _, s := range result.Samples {
			if s.Phase == phase {
				p = append(p, s.PingMS)
				e = append(e, s.EchoMS)
				g = append(g, s.GatewayMS)
			}
		}
		if len(p) > 0 {
			result.Summary[phase] = map[string]distribution{"ping": summarize(p), "echo": summarize(e), "gateway": summarize(g)}
		}
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}
func safePath(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsAny(path, " '\"\n\r\t;$`\\")
}
func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func measure(ctx context.Context, result *report) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".config/fennec")
	rawState, err := os.ReadFile(filepath.Join(root, "preparation.json"))
	if err != nil {
		return err
	}
	var state struct {
		ID string `json:"runtime_id"`
	}
	if json.Unmarshal(rawState, &state) != nil || state.ID == "" {
		return fmt.Errorf("paired runtime unavailable")
	}
	key, err := os.ReadFile(filepath.Join(root, "cloud/runtime_key"))
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("invalid paired SSH identity")
	}
	verify, err := knownhosts.New(filepath.Join(root, "cloud/known_hosts"))
	if err != nil {
		return err
	}
	address := net.JoinHostPort(runta.ActiveSSHHost(home), "22")
	raw, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer raw.Close()
	if tcp, ok := raw.(*net.TCPConn); ok {
		if err = tcp.SetNoDelay(runta.SSHNoDelayEnabled(home)); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{User: state.ID, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify})
	if err != nil {
		return err
	}
	raw.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, channels, requests)
	defer client.Close()
	command := *remote + " -role server -timeout " + deadline.String()
	if *terminal != "" {
		command = *terminal + fmt.Sprintf(" serve --protocol=%d", terminalstream.ProtocolVersion)
	}
	transport, err := terminalstream.StartSSHContext(ctx, client, command)
	if err != nil {
		return err
	}
	defer transport.Close()
	if err = transport.Ready(ctx); err != nil {
		return err
	}
	result.Packets = transport.Supports(terminalstream.CapabilityTrafficPackets)
	echo, req, err := client.OpenChannel("session", nil)
	if err != nil {
		return err
	}
	defer echo.Close()
	go ssh.DiscardRequests(req)
	go io.Copy(io.Discard, echo.Stderr())
	if _, err = echo.SendRequest("exec", false, ssh.Marshal(struct{ Command string }{*remote + " -role echo -timeout " + deadline.String()})); err != nil {
		return err
	}
	phase := func(name string) error {
		for i := 0; i < *count; i++ {
			s := sample{Phase: name, Index: i, Started: time.Now().UTC()}
			// Rotation avoids always sampling a ping directly after a keepalive.
			for j := 0; j < 3; j++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				raw.SetDeadline(time.Now().Add(5 * time.Second))
				switch (i + j) % 3 {
				case 0:
					step, cancel := context.WithTimeout(ctx, 5*time.Second)
					d, e := transport.Ping(step)
					cancel()
					if e != nil {
						return fmt.Errorf("%s ping %d: %w", name, i, e)
					}
					s.PingMS = ms(d)
				case 1:
					body, _ := json.Marshal(terminalstream.Frame{Type: "ping", ID: fmt.Sprintf("measure-%d", i+1)})
					body = append(body, '\n')
					got := make([]byte, len(body))
					start := time.Now()
					if _, e := echo.Write(body); e != nil {
						return e
					}
					if _, e := io.ReadFull(echo, got); e != nil {
						return e
					}
					s.EchoMS = ms(time.Since(start))
					if !bytes.Equal(body, got) {
						return fmt.Errorf("echo content changed")
					}
				case 2:
					start := time.Now()
					if _, _, e := client.SendRequest("keepalive@openssh.com", true, nil); e != nil {
						return e
					}
					s.GatewayMS = ms(time.Since(start))
				}
				raw.SetDeadline(time.Time{})
			}
			result.Samples = append(result.Samples, s)
			if (i+1)%15 == 0 {
				fmt.Fprintf(os.Stderr, "phase=%s samples=%d\n", name, i+1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		return nil
	}
	if err = phase("idle"); err != nil {
		return err
	}
	if !*loaded {
		return nil
	}
	var transferred atomic.Int64
	var stopLoad atomic.Bool
	loadDone := make(chan error, 1)
	started := time.Now()
	go func() {
		want := sha256.Sum256(payload())
		for !stopLoad.Load() {
			data, e := transport.Operate(ctx, cloudruntime.LocalOperation{Kind: "list"})
			if e != nil {
				loadDone <- e
				return
			}
			if sha256.Sum256(data) != want {
				loadDone <- fmt.Errorf("bulk content changed")
				return
			}
			transferred.Add(int64(len(data)))
		}
		loadDone <- nil
	}()
	err = phase("bulk")
	stopLoad.Store(true)
	if err != nil {
		return err
	}
	select {
	case e := <-loadDone:
		if e != nil {
			return e
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	result.LoadBytes = transferred.Load()
	result.LoadDuration = time.Since(started).Seconds()
	return phase("recovery")
}
