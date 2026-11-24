package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/byoc"
	"fennec/internal/byoc/byoctest"
	"fennec/internal/control"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type tokenCredentials struct{ token string }

func (c *tokenCredentials) Read(context.Context) (string, error) {
	if c.token == "" {
		return "", account.ErrNotFound
	}
	return c.token, nil
}
func (c *tokenCredentials) Write(_ context.Context, token string) error { c.token = token; return nil }
func (c *tokenCredentials) Delete(context.Context) error                { c.token = ""; return nil }

func byocCall(t *testing.T, handler control.Handler, params any) (byocStatus, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	result, err := handler(context.Background(), raw)
	if err != nil {
		return byocStatus{}, err
	}
	status, ok := result.(byocStatus)
	if !ok {
		t.Fatalf("unexpected result %T", result)
	}
	return status, nil
}

func faultCode(err error) string {
	var fault *control.Fault
	if errors.As(err, &fault) {
		return fault.Code
	}
	return ""
}

func TestPairAndUnpairSelfHostedComputer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	home := t.TempDir()
	credentials := &tokenCredentials{token: "runta-token"}
	manager := account.New(ctx, credentials, "http://127.0.0.1:1")
	if err := manager.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, runtaEpoch, _ := manager.Session()
	c := newBYOCControl(home, manager)
	var restored atomic.Int64
	c.restore = func() { restored.Add(1); _ = manager.Restore(ctx) }
	handlers := c.handlers()

	// The device key is created on request and is what the computer accepts.
	raw, err := handlers["byoc.key"](ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	deviceKey := raw.(map[string]string)["device_key"]
	computer := byoctest.New(t)
	computer.Authorize(t, home)
	endpoint := computer.Endpoint()

	// A fingerprint that is not the computer's never pairs.
	other := byoctest.New(t)
	_, err = byocCall(t, handlers["byoc.pair"], byocPairRequest{Host: endpoint.Host, Port: endpoint.Port, Fingerprint: other.Fingerprint()})
	if faultCode(err) != "pairing_failed" {
		t.Fatalf("paired with the wrong fingerprint: %v", err)
	}
	if _, err := byoc.Load(home); !errors.Is(err, byoc.ErrNotPaired) {
		t.Fatal("a refused pairing left a record")
	}
	if _, principal, current, _ := manager.Session(); principal.Kind != account.PrincipalRunta || current != runtaEpoch {
		t.Fatal("a refused pairing changed the principal")
	}

	status, err := byocCall(t, handlers["byoc.pair"], byocPairRequest{Host: endpoint.Host, Port: endpoint.Port, Fingerprint: computer.Fingerprint()})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Paired || !status.Active || status.MachineID != byoctest.Machine || status.User != byoc.DefaultUser || status.HostKeyFingerprint != computer.Fingerprint() || status.DeviceKey != deviceKey {
		t.Fatalf("unexpected status %+v", status)
	}
	_, principal, pairedEpoch, ok := manager.Session()
	if !ok || !principal.IsBYOC() || principal.Machine != byoctest.Machine || pairedEpoch <= runtaEpoch {
		t.Fatal("pairing did not switch the principal under a new epoch")
	}
	if credentials.token != "runta-token" {
		t.Fatal("pairing erased the Runta credential")
	}
	if _, err := manager.LoginMethod(""); !errors.Is(err, account.ErrSelfHostedPaired) {
		t.Fatal("Runta sign-in allowed while paired")
	}

	// A different machine cannot replace the pairing implicitly.
	other.Authorize(t, home)
	otherEndpoint := other.Endpoint()
	other.SetIdentity(`{"machine_id":"1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"}`)
	_, err = byocCall(t, handlers["byoc.pair"], byocPairRequest{Host: otherEndpoint.Host, Port: otherEndpoint.Port, Fingerprint: other.Fingerprint()})
	if faultCode(err) != "already_paired" {
		t.Fatalf("replaced the paired machine: %v", err)
	}
	if p, err := byoc.Load(home); err != nil || p.MachineID != byoctest.Machine || p.Port != endpoint.Port {
		t.Fatal("a refused replacement changed the record")
	}

	// Unpairing requires the paired machine ID.
	if _, err := byocCall(t, handlers["byoc.unpair"], byocUnpairRequest{MachineID: "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"}); faultCode(err) != "confirmation_mismatch" {
		t.Fatalf("unpaired without confirmation: %v", err)
	}
	status, err = byocCall(t, handlers["byoc.unpair"], byocUnpairRequest{MachineID: byoctest.Machine})
	if err != nil {
		t.Fatal(err)
	}
	if status.Paired || status.DeviceKey != "" {
		t.Fatalf("unpair left pairing material: %+v", status)
	}
	if _, err := os.Stat(byoc.Dir(home)); !os.IsNotExist(err) {
		t.Fatal("unpair left the pairing directory")
	}
	for restored.Load() == 0 || manager.Snapshot().Status != account.StatusAuthorized {
		select {
		case <-ctx.Done():
			t.Fatalf("Runta account not restored after unpairing: %q", manager.Snapshot().Status)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, principal, _, _ := manager.Session(); principal.Kind != account.PrincipalRunta {
		t.Fatal("unpairing did not return to the Runta account")
	}
}

func TestUnreadablePairingIsOnlyRemovedExplicitly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	if err := os.MkdirAll(byoc.Dir(home), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(byoc.Dir(home), "pairing.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := account.New(ctx, &tokenCredentials{token: "runta-token"}, "http://127.0.0.1:1")
	restorePrincipal(ctx, home, manager)
	if manager.Snapshot().Status != "signed_out" {
		t.Fatal("an unreadable pairing restored a principal")
	}
	c := newBYOCControl(home, manager)
	c.restore = nil
	status, _ := byocCall(t, c.handlers()["byoc.status"], nil)
	if !status.Unreadable || status.Paired {
		t.Fatalf("unexpected status %+v", status)
	}
	if _, err := byocCall(t, c.handlers()["byoc.pair"], byocPairRequest{Host: "127.0.0.1", Fingerprint: "SHA256:x"}); faultCode(err) != "pairing_unreadable" {
		t.Fatalf("paired over an unreadable record: %v", err)
	}
	if _, err := byocCall(t, c.handlers()["byoc.unpair"], byocUnpairRequest{MachineID: byoctest.Machine}); faultCode(err) != "pairing_unreadable" {
		t.Fatalf("unexpected unpair result: %v", err)
	}
	if _, err := byocCall(t, c.handlers()["byoc.unpair"], byocUnpairRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(byoc.Dir(home)); !os.IsNotExist(err) {
		t.Fatal("unreadable pairing was not removed")
	}
}

func TestRestorePrincipalPrefersPairing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtaHome := t.TempDir()
	runta := account.New(ctx, &tokenCredentials{token: "runta-token"}, "http://127.0.0.1:1")
	restorePrincipal(ctx, runtaHome, runta)
	if _, principal, _, ok := runta.Session(); !ok || principal.Kind != account.PrincipalRunta {
		t.Fatal("unpaired Mac did not restore the Runta account")
	}

	pairedHome := t.TempDir()
	byoctest.New(t).Pair(t, pairedHome)
	paired := account.New(ctx, &tokenCredentials{token: "runta-token"}, "http://127.0.0.1:1")
	restorePrincipal(ctx, pairedHome, paired)
	if _, principal, _, ok := paired.Session(); !ok || !principal.IsBYOC() || principal.Machine != byoctest.Machine {
		t.Fatal("paired Mac did not restore the self-hosted computer")
	}
}

func TestParseBYOCPair(t *testing.T) {
	for _, tc := range []struct {
		args []string
		host string
		port int
		user string
	}{
		{[]string{"vps.example.com", "--fingerprint", "SHA256:a"}, "vps.example.com", 22, ""},
		{[]string{"10.0.0.5:2222", "--fingerprint", "SHA256:a", "--user", "dev"}, "10.0.0.5", 2222, "dev"},
		{[]string{"[2001:db8::1]:2222", "--fingerprint", "SHA256:a"}, "2001:db8::1", 2222, ""},
		{[]string{"2001:db8::1", "--fingerprint", "SHA256:a"}, "2001:db8::1", 22, ""},
	} {
		request, err := parseBYOCPair(tc.args)
		if err != nil || request.Host != tc.host || request.Port != tc.port || request.User != tc.user || request.Fingerprint != "SHA256:a" {
			t.Fatalf("%q parsed as %+v (%v)", tc.args, request, err)
		}
	}
	for _, args := range [][]string{{}, {"host"}, {"host", "--fingerprint"}, {"host:0", "--fingerprint", "SHA256:a"}, {"host:x", "--fingerprint", "SHA256:a"}, {"--fingerprint", "SHA256:a"}, {"host", "--fingerprint", "SHA256:a", "--port", "22"}} {
		if _, err := parseBYOCPair(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

// The fen CLI pairs and unpairs through a real control socket.
func TestFenCloudBYOCOverControlSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	home := t.TempDir()
	manager := account.New(ctx, &tokenCredentials{}, "http://127.0.0.1:1")
	c := newBYOCControl(home, manager)
	c.restore = nil
	// Unix socket paths are limited to 104 bytes on macOS; keep it short.
	dir, err := os.MkdirTemp("", "fb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "c.sock")
	ready := make(chan struct{})
	served := make(chan error, 1)
	go func() { served <- control.Serve(ctx, socket, c.handlers(), func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-served:
		t.Fatalf("control server did not start: %v", err)
	case <-ctx.Done():
		t.Fatal("control server did not start")
	}
	client := control.Client{Socket: socket}
	run := func(args ...string) map[string]any {
		t.Helper()
		var output bytes.Buffer
		if err := cloudCommand(ctx, client, append([]string{"byoc"}, args...), nil, &output, false); err != nil {
			t.Fatalf("fen cloud byoc %q: %v", args, err)
		}
		var result map[string]any
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatalf("fen cloud byoc %q printed %q", args, output.String())
		}
		return result
	}
	if run("status")["paired"] != false {
		t.Fatal("fresh Mac reported a pairing")
	}
	key := run("key")["device_key"].(string)
	if !strings.HasPrefix(key, "ssh-ed25519 ") {
		t.Fatalf("unexpected device key %q", key)
	}
	computer := byoctest.New(t)
	computer.Authorize(t, home)
	endpoint := computer.Endpoint()
	paired := run("pair", net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port)), "--fingerprint", computer.Fingerprint())
	if paired["paired"] != true || paired["active"] != true || paired["machine_id"] != byoctest.Machine || paired["device_key"] != key {
		t.Fatalf("unexpected pairing output %v", paired)
	}
	if run("unpair", byoctest.Machine)["paired"] != false {
		t.Fatal("unpair did not clear the pairing")
	}
	if manager.Snapshot().Status != "signed_out" {
		t.Fatal("unpair left a principal")
	}
}
