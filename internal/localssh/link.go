package localssh

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fennec/internal/connectivity"
	"fennec/internal/launchprofile"
	"fennec/internal/runta"
	"golang.org/x/crypto/ssh"
)

type LinkConfig struct {
	RuntimeID    string                `json:"runtime_id"`
	LocalAddress string                `json:"local_address"`
	Name         string                `json:"name"`
	Directory    string                `json:"-"`
	Network      *connectivity.Monitor `json:"-"`
	// Only the account-scoped daemon may supply a previously verified owner
	// and borrow its connection. Neither can be supplied by serialized input.
	VerifiedOwner string                                                            `json:"-"`
	SharedSSH     func(context.Context) (*ssh.Client, error)                        `json:"-"`
	Bootstrap     func(context.Context, LinkBootstrap) (LinkBootstrapResult, error) `json:"-"`
	RelayDial     func(context.Context, string, int) (net.Conn, error)              `json:"-"`
}

// Link maintains one reverse connector. Runtime ID must already be provisioned;
// reconnect never creates a runtime or repeats a local command.
func Link(ctx context.Context, api *runta.Client, config LinkConfig, emit func(string, string)) error {
	host, _, err := net.SplitHostPort(config.LocalAddress)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("invalid local endpoint")
	}
	if config.RuntimeID == "" {
		return fmt.Errorf("runtime ID required")
	}
	if err = os.MkdirAll(config.Directory, 0700); err != nil {
		return err
	}
	if config.Network != nil {
		if err = config.Network.WaitOnline(ctx); err != nil {
			return nil
		}
	}
	user := config.VerifiedOwner
	if config.SharedSSH != nil {
		if user == "" {
			return fmt.Errorf("verified connection owner required")
		}
	} else {
		user, err = api.CurrentUser(ctx)
		if err != nil {
			return err
		}
		runtime, err := api.Get(ctx, config.RuntimeID)
		if err != nil {
			return err
		}
		if runtime.Owner != user || runtime.Environment["FENNEC_PURPOSE"] != "shared-v1" {
			return fmt.Errorf("runtime is not owned by the authenticated Fennec user")
		}
	}
	if err = bindLocalOwner(config.Directory, user); err != nil {
		return err
	}
	path := filepath.Join(config.Directory, "device_id")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		raw := make([]byte, 16)
		if _, err = rand.Read(raw); err != nil {
			return err
		}
		data = []byte(hex.EncodeToString(raw))
		if err = os.WriteFile(path, data, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	device := strings.TrimSpace(string(data))
	alias, err := DeviceAlias(config.Name, device)
	if err != nil {
		return err
	}
	hostData, err := os.ReadFile(filepath.Join(config.Directory, "host_key"))
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(hostData)
	if err != nil {
		return err
	}
	hostKey := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	delay := time.Second
	for ctx.Err() == nil {
		if config.Network != nil && !config.Network.Online() {
			emit("offline", alias)
			if config.Network.WaitOnline(ctx) != nil {
				return nil
			}
			delay = time.Second
		}
		emit("connecting", alias)
		connected := false
		report := func(stage, detail string) {
			if stage == "connected" {
				connected = true
			}
			emit(stage, detail)
		}
		err = linkOnce(ctx, api, config, device, alias, hostKey, user, report)
		if connected {
			delay = time.Second
		}
		if ctx.Err() != nil {
			return nil
		}
		if config.Network != nil && !config.Network.Refresh() {
			emit("offline", alias)
			if config.Network.WaitOnline(ctx) != nil {
				return nil
			}
			delay = time.Second
			continue
		}
		var netChanged <-chan struct{}
		if config.Network != nil {
			netChanged = config.Network.Changes()
		}
		emit("reconnecting", err.Error())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-netChanged:
			timer.Stop()
			if config.Network != nil && config.Network.Online() {
				delay = time.Second
			}
			continue
		case <-timer.C:
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
	return nil
}
func linkOnce(ctx context.Context, api *runta.Client, config LinkConfig, device, alias, hostKey, user string, emit func(string, string)) error {
	setupDone := launchprofile.Begin("connector.setup")
	setupCtx, cancelSetup := context.WithTimeout(ctx, 90*time.Second)
	defer cancelSetup()
	if config.Network != nil {
		changed := config.Network.Changes()
		go func() {
			select {
			case <-changed:
				cancelSetup()
			case <-setupCtx.Done():
			}
		}()
	}
	connectDone := launchprofile.Begin("connector.connect")
	var client *ssh.Client
	var err error
	if config.SharedSSH != nil {
		client, err = config.SharedSSH(setupCtx)
	} else {
		client, err = api.Connect(setupCtx, config.RuntimeID, filepath.Join(config.Directory, "cloud"))
	}
	connectDone()
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("shared cloud connection unavailable")
	}
	lease := newLinkSSHLease(ctx, setupCtx, client, config.SharedSSH == nil)
	defer lease.close()
	if err := setupCtx.Err(); err != nil {
		return err
	}
	var bridgePort, agentPort int
	prepared := false
	if config.Bootstrap != nil {
		phase := launchprofile.Begin("connector.resident")
		result, prepareErr := config.Bootstrap(setupCtx, LinkBootstrap{Device: device, Alias: alias, HostKey: hostKey})
		phase()
		if prepareErr != nil {
			return prepareErr
		}
		if result.Ready == result.NeedsSetup {
			return fmt.Errorf("connector outcome was not confirmed")
		}
		if result.Ready {
			if result.BridgePort < 1 || result.BridgePort > 65535 || result.AgentPort < 1 || result.AgentPort > 65535 {
				return fmt.Errorf("invalid relay ports")
			}
			key, _, options, rest, keyErr := ssh.ParseAuthorizedKey([]byte(result.PublicKey))
			if keyErr != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 || key.Type() != ssh.KeyAlgoED25519 {
				return fmt.Errorf("invalid pairing public key")
			}
			if err := addPairingKey(setupCtx, config.Directory, key, user); err != nil {
				return err
			}
			bridgePort, agentPort, prepared = result.BridgePort, result.AgentPort, true
		}
	}
	if !prepared {
		emit("pairing", alias)
		phase := launchprofile.Begin("connector.pair")
		err = PairRuntime(setupCtx, client, config.Directory, device, user)
		phase()
		if err != nil {
			return err
		}
		emit("opening_tunnel", alias)
		phase = launchprofile.Begin("connector.relay")
		bridgePort, agentPort, err = StartRelay(setupCtx, client, device)
		phase()
		if err != nil {
			return err
		}
		emit("configuring_alias", alias)
		phase = launchprofile.Begin("connector.alias")
		err = InstallAlias(setupCtx, client, alias, fmt.Sprintf("127.0.0.1:%d", agentPort), device, hostKey, true)
		phase()
		if err != nil {
			return err
		}
	}
	if !lease.finishSetup() || setupCtx.Err() != nil {
		return fmt.Errorf("connector setup deadline exceeded")
	}
	cancelSetup()
	setupDone()
	emit("connected", alias)
	// Standalone connectors own their connection and health checks. A daemon
	// connector relies on the shared resident health/recovery loop instead.
	keepaliveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Offline transitions invalidate this relay's idle bridges even when the
	// daemon retains SSH for a fast terminal resume. Online routing notifications
	// alone must not repeatedly tear down a healthy reverse link.
	if config.SharedSSH != nil && config.Network != nil {
		go cancelOfflineRelay(keepaliveCtx, config.Network, cancel)
	}
	if config.SharedSSH == nil {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				var changed <-chan struct{}
				if config.Network != nil {
					changed = config.Network.Changes()
					if !config.Network.Online() {
						client.Close()
						return
					}
				}
				select {
				case <-keepaliveCtx.Done():
					return
				case <-ticker.C:
				case <-changed:
				}
				reply := make(chan error, 1)
				go func() { _, _, e := client.SendRequest("keepalive@openssh.com", true, nil); reply <- e }()
				timeout := time.NewTimer(3 * time.Second)
				select {
				case e := <-reply:
					timeout.Stop()
					if e != nil {
						client.Close()
						return
					}
				case <-timeout.C:
					client.Close()
					return
				case <-keepaliveCtx.Done():
					timeout.Stop()
					return
				}
			}
		}()
	}
	if config.RelayDial != nil {
		err = serveResidentRelay(keepaliveCtx, func(ctx context.Context) (net.Conn, error) {
			return config.RelayDial(ctx, device, bridgePort)
		}, config.LocalAddress)
	} else {
		err = serveRelay(keepaliveCtx, client, bridgePort, config.LocalAddress, config.SharedSSH == nil)
	}
	if err == nil {
		return fmt.Errorf("reverse tunnel closed")
	}
	return err
}
