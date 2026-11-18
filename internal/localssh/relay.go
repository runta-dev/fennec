package localssh

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// StartRelay uses supported exec and local forwarding channels rather than
// requesting gateway-level remote forwarding. Both guest listeners are loopback.
func StartRelay(ctx context.Context, client *ssh.Client, device string) (int, int, error) {
	if _, err := DeviceAlias("machine", device); err != nil {
		return 0, 0, err
	}
	script := relaySetupScript(device)
	session, err := connectorSession(ctx, client)
	if err != nil {
		return 0, 0, err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(script)
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	output, err := session.Output("sh -s")
	if err != nil {
		return 0, 0, err
	}
	var state struct {
		Bridge int `json:"bridge_port"`
		Agent  int `json:"agent_port"`
	}
	if json.Unmarshal(output, &state) != nil || state.Bridge < 1 || state.Bridge > 65535 || state.Agent < 1 || state.Agent > 65535 {
		return 0, 0, fmt.Errorf("invalid relay state")
	}
	return state.Bridge, state.Agent, nil
}

func relaySetupScript(device string) string {
	return `exec "$HOME/.local/fennec/bin/fennec-terminal" relay-ensure ` + device
}

// Relay capacity: parkedBridges idle device sockets wait for agents at all
// times, and a claimed socket is replaced immediately rather than when its
// connection ends. Long-lived connections (such as desktop MCP servers) can
// therefore never starve new commands; maxRelayConnections bounds the total.
const (
	parkedBridges       = 8
	maxRelayConnections = 64
)

func serveRelay(ctx context.Context, client *ssh.Client, port int, local string, owned ...bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	active := make(chan struct{}, maxRelayConnections)
	forward := func(remote net.Conn) {
		defer workers.Done()
		defer func() { <-active }()
		defer remote.Close()
		target, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", local)
		if err != nil {
			log.Printf("device relay: local SSH service unreachable: %v", err)
			return
		}
		stopLocal := context.AfterFunc(ctx, func() { target.Close(); remote.Close() })
		defer stopLocal()
		done := make(chan struct{})
		go func() { io.Copy(remote, target); remote.Close(); close(done) }()
		io.Copy(target, remote)
		target.Close()
		<-done
	}
	for i := 0; i < parkedBridges; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				remote, err := client.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("device relay stopping: parking a bridge failed: %v", err)
					}
					cancel()
					return
				}
				stop := context.AfterFunc(ctx, func() { remote.Close() })
				var marker [1]byte
				_, err = io.ReadFull(remote, marker[:])
				stop()
				if err != nil {
					remote.Close()
					if ctx.Err() == nil {
						log.Printf("device relay stopping: parked bridge closed before use: %v", err)
					}
					cancel()
					return
				}
				if marker[0] != 'F' {
					remote.Close()
					continue
				}
				select {
				case active <- struct{}{}:
					workers.Add(1)
					go forward(remote)
				default:
					log.Printf("device relay: refused a connection at capacity (%d)", maxRelayConnections)
					remote.Close() // over capacity: refuse rather than queue
				}
			}
		}()
	}
	<-ctx.Done()
	// A borrowed connection also carries the resident terminal protocol. Only
	// close this relay's channels; its owner decides whether SSH needs replacing.
	if len(owned) == 0 || owned[0] {
		client.Close()
	}
	workers.Wait()
	return fmt.Errorf("device relay disconnected")
}
