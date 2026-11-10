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

	"fennec/internal/runta"
	"golang.org/x/crypto/ssh"
)

type LinkConfig struct {
	RuntimeID    string `json:"runtime_id"`
	LocalAddress string `json:"local_address"`
	Name         string `json:"name"`
	Directory    string `json:"-"`
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
	user, err := api.CurrentUser(ctx)
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
		emit("reconnecting", err.Error())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
	return nil
}
func linkOnce(ctx context.Context, api *runta.Client, config LinkConfig, device, alias, hostKey, user string, emit func(string, string)) error {
	setupCtx, cancelSetup := context.WithTimeout(ctx, 90*time.Second)
	defer cancelSetup()
	client, err := api.Connect(setupCtx, config.RuntimeID, filepath.Join(config.Directory, "cloud"))
	if err != nil {
		return err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	setupStop := context.AfterFunc(setupCtx, func() { client.Close() })
	defer setupStop()
	emit("pairing", alias)
	if err = PairRuntime(setupCtx, client, config.Directory, device, user); err != nil {
		return err
	}
	emit("opening_tunnel", alias)
	bridgePort, agentPort, err := StartRelay(setupCtx, client, device)
	if err != nil {
		return err
	}
	emit("configuring_alias", alias)
	if err = InstallAlias(setupCtx, client, alias, fmt.Sprintf("127.0.0.1:%d", agentPort), device, hostKey, true); err != nil {
		return err
	}
	if !setupStop() || setupCtx.Err() != nil {
		return fmt.Errorf("connector setup deadline exceeded")
	}
	cancelSetup()
	emit("connected", alias)
	// A stalled SSH connection is closed, allowing the outer loop to reconnect.
	keepaliveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-keepaliveCtx.Done():
				return
			case <-ticker.C:
				reply := make(chan error, 1)
				go func() { _, _, e := client.SendRequest("keepalive@openssh.com", true, nil); reply <- e }()
				select {
				case e := <-reply:
					if e != nil {
						client.Close()
						return
					}
				case <-time.After(10 * time.Second):
					client.Close()
					return
				case <-keepaliveCtx.Done():
					return
				}
			}
		}
	}()
	err = serveRelay(ctx, client, bridgePort, config.LocalAddress)
	if err == nil {
		return fmt.Errorf("reverse tunnel closed")
	}
	return err
}
