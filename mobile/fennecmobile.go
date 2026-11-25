// Package fennecmobile exposes the Fennec client to the iPhone app through
// gomobile. It reuses the Mac client's packages: account for the Runta device
// login, runta for discovering and connecting to the cloud computer, runtime
// for the fennec-terminal operations, and cli for parsing session listings.
// Results cross the binding as JSON strings.
package fennecmobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
	keyName     string
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
	// A key the server rejects (revoked, expired) signs the phone out for good.
	manager.ForgetRejectedCredentials()
	return &Auth{manager: manager, environment: env, cancel: cancel}, nil
}

// SetDeviceKeyName names the API key this device creates when it signs in,
// e.g. "Fennec iPhone (ssxd)"; the app derives it from an ID in its Keychain.
func (a *Auth) SetDeviceKeyName(name string) { a.keyName = name }

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

// FailLogin ends the sign-in in progress as authorization_failed, keeping
// detail for the app's Details.
func (a *Auth) FailLogin(detail string) { a.manager.FailLogin("authorization_failed", detail) }

func (a *Auth) CancelLogin() error {
	return a.manager.CancelAuthorization(context.Background())
}

// Logout forgets the key on the phone at once, then revokes it on Runta
// (best effort: an offline phone is signed out all the same, and the key
// expires on its own).
func (a *Auth) Logout() error {
	_, principal, _, signedIn := a.manager.Session()
	token, ok := principal.RuntaToken()
	ok = ok && signedIn
	endpoint := a.manager.Endpoint()
	if err := a.manager.Logout(context.Background()); err != nil {
		return err
	}
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		revokeToken(ctx, endpoint, token)
	}
	return nil
}

func revokeToken(ctx context.Context, endpoint, token string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(endpoint, "/")+"/v2/auth/token", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

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
	residentBusy   bool

	timingMu sync.Mutex
	timing   listTiming
}

// listTiming is how long the last session list took, by stage, for the
// app's Integration diagnostics.
type listTiming struct {
	ConnectMS int64 `json:"connect_ms"`
	CommandMS int64 `json:"command_ms"`
	TotalMS   int64 `json:"total_ms"`
	// Reused: the list ran on an already open connection.
	Reused bool `json:"reused"`
}

// Metrics samples the cloud computer: the Mac panel's SystemMetrics as JSON
// (cpu_percent, vcpus, memory, disk, network, cpu_history, mem_history).
// Call about every two seconds while the account page is open.
func (s *Sessions) Metrics() (out0 string, err error) {
	defer func() { err = classify(err) }()
	output, err := s.read(cloudruntime.RemoteMetricsScript)
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
	_, principal, epoch, ok := s.auth.manager.Session()
	token, isRunta := principal.RuntaToken()
	if !ok || !isRunta {
		return nil, "", 0, errSignedOut
	}
	return &runta.Client{Endpoint: s.auth.manager.Endpoint(), Token: token}, token, epoch, nil
}

// findShared selects the runtime the Mac app created (runta.EnsureShared's
// marker) without creating one from the phone.
func findShared(ctx context.Context, client *runta.Client) (runta.Runtime, error) {
	// Who the account is and its runtimes are independent reads: one round
	// trip to the API, not two.
	var user string
	var userErr error
	done := make(chan struct{})
	go func() { user, userErr = client.CurrentUser(ctx); close(done) }()
	runtimes, err := client.List(ctx)
	<-done
	if userErr != nil {
		return runta.Runtime{}, userErr
	}
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
	// The cloud computer this phone used last: with its pinned host key the
	// SSH handshake starts at once, alongside the API checks, which must
	// still agree before any command runs (runta.ConnectResolved).
	hint := s.rememberedRuntime()
	var sharedID string
	connection, err := client.ConnectResolved(ctx, hint, s.directory, func(ctx context.Context) (string, error) {
		shared, err := findShared(ctx, client)
		if err != nil {
			return "", err
		}
		if shared.Status != "running" {
			return "", cloudStoppedError{shared.Status}
		}
		sharedID = shared.ID
		return shared.ID, nil
	})
	if err != nil {
		var apiErr *runta.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 401 {
			s.auth.manager.RejectCredential(token, epoch)
			return nil, "", errSessionExpired
		}
		return nil, "", err
	}
	s.client, s.runtimeID, s.token = connection, sharedID, token
	s.rememberRuntime(sharedID)
	return connection, sharedID, nil
}

func (s *Sessions) rememberedRuntime() string {
	data, err := os.ReadFile(filepath.Join(s.directory, "runtime_id"))
	if err != nil || len(data) > 128 {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s *Sessions) rememberRuntime(id string) {
	if id != "" && id != s.rememberedRuntime() {
		_ = os.WriteFile(filepath.Join(s.directory, "runtime_id"), []byte(id+"\n"), 0600)
	}
}

// Warm connects ahead of the first request (after sign-in, at launch, back
// in the foreground). A connection that died while the app was suspended is
// found with a keepalive and replaced now, not by the next list.
func (s *Sessions) Warm() {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client != nil {
		alive := make(chan bool, 1)
		go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); alive <- err == nil }()
		select {
		case ok := <-alive:
			if ok {
				s.warmResident(client)
				return
			}
		case <-time.After(3 * time.Second):
		}
		s.drop(client)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if client, _, err := s.connect(ctx); err == nil {
		s.warmResident(client)
	}
}

// warmResident starts the resident command server in the background. Until it
// is ready, commands use plain SSH channels (commandstream falls back), so
// nothing waits for it.
func (s *Sessions) warmResident(client *ssh.Client) {
	s.residentMu.Lock()
	if s.residentBusy || (s.transport != nil && s.residentClient == client) {
		s.residentMu.Unlock()
		return
	}
	s.residentBusy = true
	s.residentMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = s.resident(ctx, client)
		s.residentMu.Lock()
		s.residentBusy = false
		s.residentMu.Unlock()
	}()
}

// ListTiming returns the last session list's timing as JSON (listTiming).
func (s *Sessions) ListTiming() string {
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	data, _ := json.Marshal(s.timing)
	return string(data)
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
	return s.runInput(script, nil)
}

// runInput is run with bytes for the command's standard input.
func (s *Sessions) runInput(script func(runtimeID string) (string, error), stdin []byte) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		s.mu.Lock()
		reused := s.client != nil
		s.mu.Unlock()
		started := time.Now()
		client, runtimeID, err := s.connect(ctx)
		if err != nil {
			return nil, "", err
		}
		connected := time.Now()
		command, err := script(runtimeID)
		if err != nil {
			return nil, "", err
		}
		s.warmResident(client)
		defer func() {
			if command == cloudruntime.ListScript {
				done := time.Now()
				s.timingMu.Lock()
				s.timing = listTiming{ConnectMS: connected.Sub(started).Milliseconds(), CommandMS: done.Sub(connected).Milliseconds(), TotalMS: done.Sub(started).Milliseconds(), Reused: reused}
				s.timingMu.Unlock()
			}
		}()
		session, err := commandstream.NewSession(ctx, client)
		if err != nil {
			s.drop(client)
			lastErr = err
			continue
		}
		stop := context.AfterFunc(ctx, func() { session.Close() })
		var stderr strings.Builder
		session.Stderr = &stderr
		if stdin != nil {
			session.Stdin = bytes.NewReader(stdin)
		}
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

// runRead runs a script that only reads, reconnecting and trying again when
// the link dropped (up to three attempts). Commands with effects use run,
// which never repeats one whose outcome is unknown.
func (s *Sessions) runRead(script func(runtimeID string) (string, error)) ([]byte, string, error) {
	for attempt := 1; ; attempt++ {
		output, runtimeID, err := s.run(script)
		if err == nil || attempt == 3 || !transient(err) {
			return output, runtimeID, err
		}
		time.Sleep(time.Duration(attempt) * 700 * time.Millisecond)
	}
}

// List returns a JSON array of cli.SessionSummary for live sessions.
func (s *Sessions) List() (out0 string, err error) {
	defer func() { err = classify(err) }()
	raw, runtimeID, err := s.runRead(func(string) (string, error) { return cloudruntime.ListScript, nil })
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
			Address   string          `json:"address"`
			Status    json.RawMessage `json:"status"`
			StatusMS  int64           `json:"status_ms"`
			CreatedMS int64           `json:"created_ms"`
		} `json:"terminals"`
	}
	json.Unmarshal(raw, &listing)
	type row struct {
		cli.SessionSummary
		Activity  string `json:"activity,omitempty"`
		StatusMS  int64  `json:"status_ms,omitempty"`
		CreatedMS int64  `json:"created_ms,omitempty"`
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
			r.Activity, r.StatusMS, r.CreatedMS = strings.TrimSpace(status.Msg), t.StatusMS, t.CreatedMS
		}
		rows = append(rows, r)
	}
	data, err := json.Marshal(rows)
	return string(data), err
}

// Read returns {"terminal":{...},"screen":{...}} for one session.
func (s *Sessions) Read(address, sessionID string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	raw, _, err := s.runRead(func(string) (string, error) { return cloudruntime.ReadScript(address, sessionID) })
	return string(raw), err
}

// Send types text into the session and presses Return.
func (s *Sessions) Send(address, sessionID, text string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	raw, _, err := s.run(func(string) (string, error) { return cloudruntime.SendScript(address, sessionID, text) })
	return string(raw), err
}

// Stop ends the cloud process for the session.
func (s *Sessions) Stop(address, sessionID string) (out0 string, err error) {
	defer func() { err = classify(err) }()
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
func (s *Sessions) Attach(address, sessionID string, cols, rows int, out TerminalOutput) (out0 *Terminal, err error) {
	defer func() { err = classify(err) }()
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
				message = classify(err).Error()
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
func (s *Sessions) Launch(agent string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	raw, err := s.read(`ls -t "$HOME/.ssh/fennec/" 2>/dev/null | sed -n 's/^\([a-f0-9]\{8,64\}\)\.conf$/\1/p' | head -1`)
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
