// Package fennecmobile exposes the Fennec client to the iPhone app through
// gomobile. It reuses the Mac client's packages: account for the Runta device
// login, runta for discovering and connecting to the cloud computer, runtime
// for the fennec-terminal operations, and cli for parsing session listings.
// Results cross the binding as JSON strings.
package fennecmobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fennec/internal/account"
	"fennec/internal/cli"
	"fennec/internal/commandstream"
	"fennec/internal/runta"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/sessionid"
	"fennec/internal/terminalstream"
	"fennec/internal/transcript"

	"golang.org/x/crypto/ssh"
)

// TranscriptEventKind identifies structured device notices in cached or live messages.
func TranscriptEventKind(text string) string { return transcript.EventKind(text) }

// TokenStore keeps the Runta session token. The app backs it with the Keychain.
type TokenStore interface {
	LoadToken() (string, error)
	SaveToken(token string) error
	ClearToken() error
}

type credentials struct{ store TokenStore }

func (c credentials) Read(context.Context) (string, error) {
	token, err := c.store.LoadToken()
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", account.ErrNotFound
	}
	return token, nil
}
func (c credentials) Write(_ context.Context, token string) error { return c.store.SaveToken(token) }
func (c credentials) Delete(context.Context) error                { return c.store.ClearToken() }

// Auth wraps account.Manager, the same device-flow login the Mac app uses.
type Auth struct {
	manager     *account.Manager
	environment runta.Environment
	cancel      context.CancelFunc
}

// NewAuth accepts the Mac's environment names ("production", "integration",
// "beta") or an HTTPS API origin, resolved by runta.ResolveEnvironment.
func NewAuth(environment string, store TokenStore) (*Auth, error) {
	env, err := runta.ResolveEnvironment(environment)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := account.New(ctx, credentials{store}, env.API)
	if err := manager.SetEndpoint(ctx, env.API, env.Dashboard); err != nil {
		cancel()
		return nil, err
	}
	return &Auth{manager: manager, environment: env, cancel: cancel}, nil
}

// Environment returns {"name","api","dashboard","ssh_host"}.
func (a *Auth) Environment() string {
	data, _ := json.Marshal(a.environment)
	return string(data)
}

// EnvironmentKey is "" for production, else a filesystem-safe name.
func (a *Auth) EnvironmentKey() string { return a.environment.Key() }

// Close stops background work; call when switching environments.
func (a *Auth) Close() { a.cancel() }

func snapshotJSON(s account.Snapshot) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// Restore loads a saved token. Call once at launch.
func (a *Auth) Restore() error { return a.manager.Restore(context.Background()) }

// Login starts the device flow; the snapshot's verification_url is the
// dashboard page to open.
func (a *Auth) Login() string { return snapshotJSON(a.manager.Login()) }

// LoginMethod opens Google, GitHub, or email-code sign-in in the system browser.
// Provider sessions stay in the browser; only the Runta device credential is saved.
func (a *Auth) LoginMethod(method string) (string, error) {
	state, err := a.manager.LoginMethod(method)
	return snapshotJSON(state), err
}

func (a *Auth) Snapshot() string { return snapshotJSON(a.manager.Snapshot()) }

// WaitForChange blocks until the login state changes or the timeout passes,
// then returns the current snapshot.
func (a *Auth) WaitForChange(timeoutMillis int64) string {
	changes := a.manager.Changes()
	select {
	case <-changes:
	case <-time.After(time.Duration(timeoutMillis) * time.Millisecond):
	}
	return a.Snapshot()
}

// PairingAPI returns the Runta API origin in a QR code from Fennec on the
// Mac, so the app can switch to that environment before Redeem.
func PairingAPI(pairingURL string) (string, error) {
	_, api, err := account.ParsePairingURL(pairingURL)
	return api, err
}

// Redeem signs in with a QR code from Fennec on the Mac. The user approves
// it on the Runta dashboard; this polls until then. The code must belong to
// this Auth's environment.
func (a *Auth) Redeem(pairingURL string) (string, error) {
	code, api, err := account.ParsePairingURL(pairingURL)
	if err != nil {
		return "", err
	}
	if strings.TrimRight(api, "/") != strings.TrimRight(a.environment.API, "/") {
		return "", fmt.Errorf("pairing code is for another Runta environment")
	}
	return snapshotJSON(a.manager.Redeem(code)), nil
}

func (a *Auth) CancelLogin() error {
	return a.manager.CancelAuthorization(context.Background())
}

func (a *Auth) Logout() error { return a.manager.Logout(context.Background()) }

func (a *Auth) DashboardURL() string { return a.manager.DashboardURL() }

// Profile returns {"user_id","display_name","email"}.
func (a *Auth) Profile() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	profile, err := a.manager.Profile(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(profile)
	return string(data), err
}

// Billing returns the Mac panel's account numbers as JSON: currency,
// balance and spend (minor units, last 30 days), cpu/memory/disk usage
// ("253.34 vCPU-h"), or web_only / reauth_required when not available here.
func (a *Auth) Billing() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	billing, err := a.manager.Billing(ctx)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(billing)
	return string(data), err
}

// Sessions talks to the user's shared Fennec cloud computer over SSH, using a
// device key kept under dataDir, exactly like the Mac daemon.
type Sessions struct {
	auth      *Auth
	directory string

	mu             sync.Mutex
	client         *ssh.Client
	runtimeID      string
	token          string
	paths          map[string]string    // address/session -> conversation log
	pathsAt        map[string]time.Time // when each was resolved
	metrics        cloudruntime.RemoteSampler
	residentMu     sync.Mutex
	residentClient *ssh.Client
	transport      *terminalstream.Transport
}

// Metrics samples the cloud computer: the Mac panel's SystemMetrics as JSON
// (cpu_percent, vcpus, memory, disk, network, cpu_history, mem_history).
// Call about every two seconds while the account page is open.
func (s *Sessions) Metrics() (string, error) {
	output, err := s.exec(cloudruntime.RemoteMetricsScript)
	if err != nil {
		return "", err
	}
	metrics, err := s.metrics.Sample(string(output))
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(metrics)
	return string(data), err
}

func NewSessions(auth *Auth, dataDir string) *Sessions {
	directory := filepath.Join(dataDir, "ssh")
	if key := auth.environment.Key(); key != "" {
		directory = filepath.Join(dataDir, "ssh-"+key)
	}
	return &Sessions{auth: auth, directory: directory}
}

var errNoComputer = errors.New("no Fennec cloud computer yet; start an agent from Fennec on your Mac first")

// runtimeClient returns a Runta API client for the signed-in account.
func (s *Sessions) runtimeClient() (*runta.Client, string, uint64, error) {
	_, token, epoch, ok := s.auth.manager.Session()
	if !ok {
		return nil, "", 0, errors.New("sign in first")
	}
	return &runta.Client{Endpoint: s.auth.manager.Endpoint(), Token: token}, token, epoch, nil
}

// findShared selects the runtime the Mac app created (runta.EnsureShared's
// marker) without creating one from the phone.
func findShared(ctx context.Context, client *runta.Client) (runta.Runtime, error) {
	user, err := client.CurrentUser(ctx)
	if err != nil {
		return runta.Runtime{}, err
	}
	runtimes, err := client.List(ctx)
	if err != nil {
		return runta.Runtime{}, err
	}
	var matches []runta.Runtime
	for _, r := range runtimes {
		if r.Owner != user || r.Environment["FENNEC_PURPOSE"] != "shared-v1" {
			continue
		}
		switch r.Status {
		case "deleted", "deleting", "error", "failed", "crashed":
			continue
		}
		matches = append(matches, r)
	}
	if len(matches) == 0 {
		return runta.Runtime{}, errNoComputer
	}
	if len(matches) > 1 {
		return runta.Runtime{}, errors.New("multiple Fennec cloud computers; open Fennec on your Mac to reconcile them")
	}
	return matches[0], nil
}

func (s *Sessions) connect(ctx context.Context) (*ssh.Client, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, token, epoch, err := s.runtimeClient()
	if err != nil {
		return nil, "", err
	}
	if s.client != nil && s.token == token {
		return s.client, s.runtimeID, nil
	}
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	shared, err := findShared(ctx, client)
	if err != nil {
		var apiErr *runta.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 401 {
			s.auth.manager.RejectCredential(token, epoch)
			return nil, "", errors.New("your session expired; sign in again")
		}
		return nil, "", err
	}
	if shared.Status != "running" {
		return nil, "", fmt.Errorf("the cloud computer is %s", shared.Status)
	}
	connection, err := client.ConnectOwned(ctx, shared.ID, s.directory)
	if err != nil {
		return nil, "", err
	}
	s.client, s.runtimeID, s.token = connection, shared.ID, token
	return connection, shared.ID, nil
}

func (s *Sessions) drop(client *ssh.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == client {
		s.client.Close()
		s.client = nil
	}
}

// run executes a fennec-terminal script on the cloud computer, reconnecting
// once if the cached SSH connection went stale.
func (s *Sessions) run(script func(runtimeID string) (string, error)) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		client, runtimeID, err := s.connect(ctx)
		if err != nil {
			return nil, "", err
		}
		command, err := script(runtimeID)
		if err != nil {
			return nil, "", err
		}
		if _, err := s.resident(ctx, client); err != nil {
			return nil, "", err
		}
		session, err := commandstream.NewSession(ctx, client)
		if err != nil {
			s.drop(client)
			lastErr = err
			continue
		}
		stop := context.AfterFunc(ctx, func() { session.Close() })
		var stderr strings.Builder
		session.Stderr = &stderr
		output, err := session.Output(command)
		stop()
		session.Close()
		if err != nil {
			if message := strings.TrimSpace(stderr.String()); message != "" {
				return nil, "", errors.New(message)
			}
			s.drop(client)
			return nil, "", err // Never replay a command whose outcome is unknown.
		}
		return output, runtimeID, nil
	}
	return nil, "", lastErr
}

// List returns a JSON array of cli.SessionSummary for live sessions.
func (s *Sessions) List() (string, error) {
	raw, runtimeID, err := s.run(func(string) (string, error) { return cloudruntime.ListScript, nil })
	if err != nil {
		return "", err
	}
	sessions, err := cli.ParseSessions(raw, runtimeID)
	if err != nil {
		return "", err
	}
	// Herdr's own line about what the agent is doing, and since when.
	var listing struct {
		Terminals []struct {
			Address  string          `json:"address"`
			Status   json.RawMessage `json:"status"`
			StatusMS int64           `json:"status_ms"`
		} `json:"terminals"`
	}
	json.Unmarshal(raw, &listing)
	type row struct {
		cli.SessionSummary
		Activity string `json:"activity,omitempty"`
		StatusMS int64  `json:"status_ms,omitempty"`
	}
	rows := make([]row, 0, len(sessions))
	for _, session := range sessions {
		r := row{SessionSummary: session}
		for _, t := range listing.Terminals {
			if t.Address != session.Address {
				continue
			}
			var status struct {
				Msg string `json:"msg"`
			}
			json.Unmarshal(t.Status, &status)
			r.Activity, r.StatusMS = strings.TrimSpace(status.Msg), t.StatusMS
		}
		rows = append(rows, r)
	}
	data, err := json.Marshal(rows)
	return string(data), err
}

// Read returns {"terminal":{...},"screen":{...}} for one session.
func (s *Sessions) Read(address, sessionID string) (string, error) {
	raw, _, err := s.run(func(string) (string, error) { return cloudruntime.ReadScript(address, sessionID) })
	return string(raw), err
}

// Send types text into the session and presses Return.
func (s *Sessions) Send(address, sessionID, text string) (string, error) {
	raw, _, err := s.run(func(string) (string, error) { return cloudruntime.SendScript(address, sessionID, text) })
	return string(raw), err
}

// Stop ends the cloud process for the session.
func (s *Sessions) Stop(address, sessionID string) (string, error) {
	raw, _, err := s.run(func(string) (string, error) { return cloudruntime.StopScript(address, sessionID) })
	return string(raw), err
}

func (s *Sessions) Close() {
	s.residentMu.Lock()
	if s.transport != nil {
		s.transport.Close()
		s.transport = nil
	}
	s.residentMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
}

// TerminalOutput receives a live terminal stream. Output may run on any
// goroutine; Closed is called once when the stream ends.
type TerminalOutput interface {
	Output(data []byte)
	Closed(message string)
}

// Terminal is this iPhone's own viewer of a Herdr terminal. Herdr gives the
// PTY the size of whichever non-observer viewer was active last
// (herdr-core viewers.rs apply_size), so attaching, typing or resizing here
// redraws the agent at the phone's size, and returning to the Mac's terminal
// takes it back.
type Terminal struct {
	session *ssh.Session
	stdin   io.WriteCloser
	once    sync.Once
}

var (
	addressPattern = regexp.MustCompile(`^t[0-9a-z]+$`)
	sessionPattern = regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`)
)

// Attach opens a new viewer through fennec-terminal attach (the entry
// `fen attach` uses), on a PTY of cols x rows.
func (s *Sessions) Attach(address, sessionID string, cols, rows int, out TerminalOutput) (*Terminal, error) {
	if !addressPattern.MatchString(address) || !sessionPattern.MatchString(sessionID) {
		return nil, errors.New("invalid terminal")
	}
	if cols < 20 || rows < 5 {
		return nil, errors.New("terminal is too small")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, _, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	session, err := client.NewSession()
	if err != nil {
		s.drop(client)
		if client, _, err = s.connect(ctx); err != nil {
			return nil, err
		}
		if session, err = client.NewSession(); err != nil {
			return nil, err
		}
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		session.Close()
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, err
	}
	if err := session.Start("exec \"$HOME/.local/fennec/bin/fennec-terminal\" attach " + address + " " + sessionID); err != nil {
		session.Close()
		return nil, err
	}
	terminal := &Terminal{session: session, stdin: stdin}
	go func() {
		buffer := make([]byte, 32<<10)
		for {
			n, err := stdout.Read(buffer)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buffer[:n])
				out.Output(chunk)
			}
			if err != nil {
				break
			}
		}
		message := ""
		if err := session.Wait(); err != nil {
			if _, exit := err.(*ssh.ExitError); !exit {
				message = err.Error()
			}
		}
		out.Closed(message)
	}()
	return terminal, nil
}

// Write sends keys typed on the phone.
func (t *Terminal) Write(data []byte) error {
	_, err := t.stdin.Write(data)
	return err
}

// Resize reports a new phone terminal size (rotation, keyboard).
func (t *Terminal) Resize(cols, rows int) error {
	if cols < 20 || rows < 5 {
		return nil
	}
	return t.session.WindowChange(rows, cols)
}

// Focus tells Herdr this viewer is in front again, which makes its size win.
func (t *Terminal) Focus() error { return t.Write([]byte("\x1b[I")) }

// Close detaches this viewer; the agent keeps running.
func (t *Terminal) Close() {
	t.once.Do(func() { t.session.Close() })
}

// Launch starts agent ("codex", "claude", "pi", ...) in a new cloud session
// ~/.fen/<word>-<word>, the workspace a Mac launch gets, with no Mac starting
// directory. The Mac is the most recently registered device on the cloud
// computer. Returns the terminal as JSON (address, id, labels).
func (s *Sessions) Launch(agent string) (string, error) {
	raw, err := s.exec(`ls -t "$HOME/.ssh/fennec/" 2>/dev/null | sed -n 's/^\([a-f0-9]\{8,64\}\)\.conf$/\1/p' | head -1`)
	if err != nil {
		return "", err
	}
	device := strings.TrimSpace(string(raw))
	if device == "" {
		return "", errors.New("connect Fennec on your Mac to this cloud computer first")
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		id, err := sessionid.Random()
		if err != nil {
			return "", err
		}
		script, err := cloudruntime.CloudLaunchScript(id, agent, "", device, nil)
		if err != nil {
			return "", err
		}
		out, err := s.exec(script)
		if err == nil {
			return string(out), nil
		}
		lastErr = err // a taken name is refused by the launch journal; try another
	}
	return "", lastErr
}
