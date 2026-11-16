package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fennec/internal/commandstream"
	"fennec/internal/runta"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type comparisonSample struct {
	Phase     string    `json:"phase"`
	Block     int       `json:"block"`
	Variant   string    `json:"variant"`
	Started   time.Time `json:"started_utc"`
	PingMS    float64   `json:"ping_ms"`
	GatewayMS float64   `json:"gateway_ms"`
	EchoMS    float64   `json:"echo_ms"`
}
type comparisonBlock struct {
	Variant  string  `json:"variant"`
	Index    int     `json:"index"`
	Sent     int64   `json:"verified_upload_bytes"`
	Received int64   `json:"verified_download_bytes"`
	Duration float64 `json:"load_seconds"`
}
type comparisonReport struct {
	Boundary  string                             `json:"boundary"`
	Baseline  string                             `json:"baseline_terminal"`
	Candidate string                             `json:"candidate_terminal"`
	Started   time.Time                          `json:"started_utc"`
	Duration  float64                            `json:"duration_seconds"`
	Samples   []comparisonSample                 `json:"samples"`
	Blocks    []comparisonBlock                  `json:"blocks"`
	Summary   map[string]map[string]distribution `json:"summary"`
	Error     string                             `json:"error,omitempty"`
}

// Open one verified connection for every treatment and comparator. This probe
// never opens an agent session or modifies the installed daemon or terminal.
func comparisonConnection(ctx context.Context) (*ssh.Client, net.Conn, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	root := filepath.Join(home, ".config/fennec")
	data, err := os.ReadFile(filepath.Join(root, "preparation.json"))
	if err != nil {
		return nil, nil, err
	}
	var state struct {
		ID string `json:"runtime_id"`
	}
	if json.Unmarshal(data, &state) != nil || state.ID == "" {
		return nil, nil, fmt.Errorf("paired runtime unavailable")
	}
	key, err := os.ReadFile(filepath.Join(root, "cloud/runtime_key"))
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid paired identity")
	}
	verify, err := knownhosts.New(filepath.Join(root, "cloud/known_hosts"))
	if err != nil {
		return nil, nil, err
	}
	address := net.JoinHostPort(runta.ActiveSSHHost(home), "22")
	raw, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, err
	}
	success := false
	defer func() {
		if !success {
			raw.Close()
		}
	}()
	if tcp, ok := raw.(*net.TCPConn); ok {
		if err = tcp.SetNoDelay(runta.SSHNoDelayEnabled(home)); err != nil {
			return nil, nil, err
		}
	}
	context.AfterFunc(ctx, func() { raw.Close() })
	raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{User: state.ID, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify})
	if err != nil {
		return nil, nil, err
	}
	raw.SetDeadline(time.Time{})
	success = true
	return ssh.NewClient(conn, channels, requests), raw, nil
}

func compareVersions(ctx context.Context) error {
	if !safePath(*baseline) || !safePath(*terminal) || !safePath(*remote) || *count > 40 {
		return fmt.Errorf("comparison requires staged baseline, terminal, remote and 1-40 samples per block")
	}
	result := comparisonReport{Boundary: "Same authenticated SSH connection, resident services kept warm. Identical continuous binary cat traffic: baseline uses ordinary SSH exec, candidate uses the paced resident command stream. Eight load blocks ordered ABBA BAAB, no overlapping test transfers; both idle and recovery measure both services. Every sent/received byte is verified and exits must succeed. Rotated sequential gateway/guest/ping measurements are contemporaneous comparators, not per-packet component latency. Other live traffic remains uncontrolled. Throughput is reported because rate limiting is an intentional tradeoff.", Baseline: *baseline, Candidate: *terminal, Started: time.Now().UTC()}
	err := runComparison(ctx, &result)
	if err != nil {
		result.Error = err.Error()
	}
	result.Duration = time.Since(result.Started).Seconds()
	result.Summary = map[string]map[string]distribution{}
	for _, phase := range []string{"idle", "bulk", "recovery"} {
		for _, variant := range []string{"baseline", "candidate"} {
			var p, g, e []float64
			for _, s := range result.Samples {
				if s.Phase == phase && s.Variant == variant {
					p = append(p, s.PingMS)
					g = append(g, s.GatewayMS)
					e = append(e, s.EchoMS)
				}
			}
			if len(p) > 0 {
				result.Summary[phase+"/"+variant] = map[string]distribution{"ping": summarize(p), "gateway": summarize(g), "echo": summarize(e)}
			}
		}
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}
func runComparison(ctx context.Context, result *comparisonReport) error {
	client, raw, err := comparisonConnection(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	servers := map[string]*terminalstream.Transport{}
	for _, v := range []struct{ name, path string }{{"baseline", *baseline}, {"candidate", *terminal}} {
		t, err := terminalstream.StartSSHContext(ctx, client, v.path+fmt.Sprintf(" serve --protocol=%d", terminalstream.ProtocolVersion))
		if err != nil {
			return err
		}
		defer t.Close()
		if err = t.Ready(ctx); err != nil {
			return err
		}
		servers[v.name] = t
	}
	if servers["baseline"].Supports(terminalstream.CapabilityTrafficPackets) || !servers["candidate"].Supports(terminalstream.CapabilityCommand) {
		return fmt.Errorf("unexpected baseline/candidate capabilities")
	}
	echo, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		return err
	}
	defer echo.Close()
	go ssh.DiscardRequests(requests)
	go io.Copy(io.Discard, echo.Stderr())
	if _, err = echo.SendRequest("exec", false, ssh.Marshal(struct{ Command string }{*remote + " -role echo -timeout " + deadline.String()})); err != nil {
		return err
	}
	sample := func(phase, variant string, block int) error {
		index := len(result.Samples)
		s := comparisonSample{Phase: phase, Variant: variant, Block: block, Started: time.Now().UTC()}
		for turn := 0; turn < 3; turn++ {
			raw.SetDeadline(time.Now().Add(5 * time.Second))
			start := time.Now()
			switch (index + turn) % 3 {
			case 0:
				step, cancel := context.WithTimeout(ctx, 5*time.Second)
				rtt, e := servers[variant].Ping(step)
				cancel()
				if e != nil {
					return e
				}
				s.PingMS = ms(rtt)
			case 1:
				if _, _, e := client.SendRequest("keepalive@openssh.com", true, nil); e != nil {
					return e
				}
				s.GatewayMS = ms(time.Since(start))
			case 2:
				data := []byte(fmt.Sprintf("{\"type\":\"ping\",\"id\":\"measure-%d\"}\n", index+1))
				got := make([]byte, len(data))
				if _, e := echo.Write(data); e != nil {
					return e
				}
				if _, e := io.ReadFull(echo, got); e != nil {
					return e
				}
				if !bytes.Equal(data, got) {
					return fmt.Errorf("comparison echo changed")
				}
				s.EchoMS = ms(time.Since(start))
			}
			raw.SetDeadline(time.Time{})
		}
		result.Samples = append(result.Samples, s)
		return nil
	}
	idle := func(phase string) error {
		for i := 0; i < *count*2; i++ {
			order := []string{"baseline", "candidate"}
			if i%2 != 0 {
				order[0], order[1] = order[1], order[0]
			}
			for _, v := range order {
				if err := sample(phase, v, -1); err != nil {
					return err
				}
			}
		}
		fmt.Fprintln(os.Stderr, "comparison phase completed:", phase)
		return nil
	}
	if err = idle("idle"); err != nil {
		return err
	}
	for index, variant := range []string{"baseline", "candidate", "candidate", "baseline", "candidate", "baseline", "baseline", "candidate"} {
		load, err := beginComparisonLoad(ctx, client, servers["candidate"], variant)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "comparison block %d %s started\n", index, variant)
		for range *count {
			if err = sample("bulk", variant, index); err != nil {
				load.abort()
				return err
			}
		}
		block, err := load.finish(ctx)
		block.Index = index
		block.Variant = variant
		result.Blocks = append(result.Blocks, block)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "comparison block %d %s verified %d bytes each direction in %.2fs\n", index, variant, block.Received, block.Duration)
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return idle("recovery")
}

type verifiedOutput struct {
	data  []byte
	count atomic.Int64
	ready chan struct{}
	once  sync.Once
}

func (v *verifiedOutput) Write(data []byte) (int, error) {
	offset := v.count.Load()
	for i, b := range data {
		if b != v.data[(int(offset)+i)%len(v.data)] {
			return 0, fmt.Errorf("comparison load payload changed")
		}
	}
	v.count.Add(int64(len(data)))
	v.once.Do(func() { close(v.ready) })
	return len(data), nil
}

type comparisonLoad struct {
	stop    atomic.Bool
	sent    atomic.Int64
	out     *verifiedOutput
	started time.Time
	sender  chan error
	done    chan error
	abort   func()
}

func beginComparisonLoad(ctx context.Context, client *ssh.Client, t *terminalstream.Transport, variant string) (*comparisonLoad, error) {
	payload := make([]byte, 64<<10)
	for i := range payload {
		payload[i] = byte(i*31 + i/89)
	}
	input, write := io.Pipe()
	out := &verifiedOutput{data: payload, ready: make(chan struct{})}
	load := &comparisonLoad{out: out, started: time.Now(), sender: make(chan error, 1), done: make(chan error, 1)}
	var run func() error
	var closeSession func() error
	if variant == "baseline" {
		s, err := client.NewSession()
		if err != nil {
			input.Close()
			write.Close()
			return nil, err
		}
		s.Stdin = input
		s.Stdout = out
		s.Stderr = io.Discard
		run = func() error { return s.Run("cat") }
		closeSession = s.Close
	} else {
		conn, err := t.DialSocket(ctx, terminalstream.SocketCommand, "")
		if err != nil {
			input.Close()
			write.Close()
			return nil, err
		}
		s := commandstream.NewStream(ctx, conn)
		s.Stdin = input
		s.Stdout = out
		s.Stderr = io.Discard
		run = func() error { return s.Run("cat") }
		closeSession = s.Close
	}
	load.abort = func() { input.Close(); write.Close(); closeSession() }
	stop := context.AfterFunc(ctx, load.abort)
	go func() {
		err := run()
		stop()
		input.Close()
		write.Close()
		closeSession()
		load.done <- err
	}()
	go func() {
		for !load.stop.Load() {
			n, err := write.Write(payload)
			load.sent.Add(int64(n))
			if err != nil {
				load.sender <- err
				return
			}
		}
		load.sender <- write.Close()
	}()
	select {
	case <-out.ready:
		return load, nil
	case err := <-load.done:
		load.abort()
		return nil, fmt.Errorf("load ended before producing output: %v", err)
	case <-ctx.Done():
		load.abort()
		return nil, ctx.Err()
	}
}
func (l *comparisonLoad) finish(ctx context.Context) (comparisonBlock, error) {
	defer l.abort()
	l.stop.Store(true)
	var err error
	select {
	case err = <-l.sender:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err == nil {
		select {
		case err = <-l.done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	block := comparisonBlock{Sent: l.sent.Load(), Received: l.out.count.Load(), Duration: time.Since(l.started).Seconds()}
	if err == nil && block.Sent != block.Received {
		err = fmt.Errorf("comparison transfer incomplete")
	}
	return block, err
}
