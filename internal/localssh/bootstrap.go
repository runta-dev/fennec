package localssh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
)

// LinkBootstrap reuses an installed relay over one resident request. It returns
// Ready=false before any mutation when cold installation is needed. Errors after
// alias registration must not be replayed through a second transport.
type LinkBootstrap struct {
	Device  string `json:"device"`
	Alias   string `json:"alias"`
	HostKey string `json:"host_key"`
}

type LinkBootstrapResult struct {
	NeedsSetup bool   `json:"needs_setup,omitempty"`
	Ready      bool   `json:"ready"`
	BridgePort int    `json:"bridge_port,omitempty"`
	AgentPort  int    `json:"agent_port,omitempty"`
	PublicKey  string `json:"public_key,omitempty"`
}

func (r LinkBootstrap) Validate() error {
	_, _, err := AliasConfig(r.Alias, "127.0.0.1:1", r.Device, r.HostKey, true)
	return err
}

func BootstrapExistingLink(ctx context.Context, home string, r LinkBootstrap) (LinkBootstrapResult, error) {
	if err := r.Validate(); err != nil {
		return LinkBootstrapResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return LinkBootstrapResult{}, err
	}
	result, err := inspectExistingRelay(ctx, home, r.Device, os.ReadFile)
	if err != nil || !result.Ready {
		return result, err
	}
	keyPath := filepath.Join(home, ".ssh/fennec", r.Device)
	info, err := os.Lstat(keyPath)
	if os.IsNotExist(err) {
		return LinkBootstrapResult{NeedsSetup: true}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return LinkBootstrapResult{}, fmt.Errorf("invalid runtime pairing key")
	}
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return LinkBootstrapResult{}, fmt.Errorf("runtime pairing key unavailable")
	}
	key, err := ssh.ParsePrivateKey(data)
	if err != nil || key.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return LinkBootstrapResult{}, fmt.Errorf("invalid runtime pairing key")
	}
	result.PublicKey = string(ssh.MarshalAuthorizedKey(key.PublicKey()))
	if err := ctx.Err(); err != nil {
		return LinkBootstrapResult{}, err
	}
	_, err = RegisterAlias(home, AliasRegistration{Alias: r.Alias, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(result.AgentPort)), Device: r.Device, HostKey: r.HostKey, Default: true})
	return result, err
}

func inspectExistingRelay(ctx context.Context, home, device string, readProc func(string) ([]byte, error)) (LinkBootstrapResult, error) {
	base := filepath.Join(home, ".local/fennec/relay-"+device)
	path := filepath.Join(base, "state.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return LinkBootstrapResult{NeedsSetup: true}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return LinkBootstrapResult{}, fmt.Errorf("invalid relay state")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return LinkBootstrapResult{}, err
	}
	var state struct {
		Bridge   int    `json:"bridge_port"`
		Agent    int    `json:"agent_port"`
		PID      int    `json:"pid"`
		Revision string `json:"revision"`
	}
	if json.Unmarshal(data, &state) != nil || state.PID <= 1 || state.Bridge < 1 || state.Bridge > 65535 || state.Agent < 1 || state.Agent > 65535 {
		return LinkBootstrapResult{}, fmt.Errorf("invalid relay state")
	}
	if state.Revision != fmt.Sprintf("%x", sha256.Sum256([]byte(relaySource))) {
		return LinkBootstrapResult{NeedsSetup: true}, nil
	}
	cmdline, err := readProc(filepath.Join("/proc", strconv.Itoa(state.PID), "cmdline"))
	if os.IsNotExist(err) {
		return LinkBootstrapResult{NeedsSetup: true}, nil
	}
	if err != nil {
		return LinkBootstrapResult{}, err
	}
	args := bytes.Split(cmdline, []byte{0})
	if !containsArg(args, filepath.Join(base, "relay.py")) || !containsArg(args, "--state") || !containsArg(args, path) {
		return LinkBootstrapResult{}, fmt.Errorf("relay process identity mismatch")
	}
	probe, err := (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Bridge)))
	if err != nil {
		if ctx.Err() != nil {
			return LinkBootstrapResult{}, ctx.Err()
		}
		return LinkBootstrapResult{NeedsSetup: true}, nil
	}
	probe.Close()
	return LinkBootstrapResult{Ready: true, BridgePort: state.Bridge, AgentPort: state.Agent}, nil
}

func containsArg(args [][]byte, value string) bool {
	for _, arg := range args {
		if string(arg) == value {
			return true
		}
	}
	return false
}
