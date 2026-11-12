package localssh

import (
	"context"
	"fennec/internal/audit"
	"fennec/internal/guardian"
	"fennec/internal/sandbox"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

// directForward implements ssh -L and -D. Destination access is explicitly
// current-user network access, not a filesystem or network sandbox.
func directForward(parent context.Context, paired func(server.PublicKey) bool, broker *Broker, approval bool) server.ChannelHandler {
	slots := make(chan struct{}, 16)
	return func(_ *server.Server, _ *ssh.ServerConn, channel ssh.NewChannel, connection server.Context) {
		var target struct {
			Host       string
			Port       uint32
			Origin     string
			OriginPort uint32
		}
		key, _ := connection.Value(server.ContextKeyPublicKey).(server.PublicKey)
		if ssh.Unmarshal(channel.ExtraData(), &target) != nil || target.Host == "" || len(target.Host) > 253 || strings.ContainsAny(target.Host, "\x00\r\n") || target.Port < 1 || target.Port > 65535 || !paired(key) {
			channel.Reject(ssh.Prohibited, "invalid or unpaired forwarding request")
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			channel.Reject(ssh.ResourceShortage, "forwarding capacity reached")
			return
		}
		ctx, cancel := context.WithCancel(connection)
		defer cancel()
		watchPairing(ctx, cancel, func() bool { return paired(key) }, time.Second)
		stop := context.AfterFunc(parent, cancel)
		defer stop()
		dest := net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port)))
		desc := fmt.Sprintf("TCP forwarding to %s", dest)
		policy := broker.effectivePolicy()
		mode := sandbox.NormalizeMode(policy.Mode)
		decisionType := "auto_sandbox"
		// Codex parity: network access outside Full access is a reviewed
		// network request (user or auto-review); Full access never asks.
		if approval && mode != sandbox.ModeFull && !policy.Network {
			prompt, done := context.WithTimeout(ctx, 2*time.Minute)
			outcome := broker.authorize(prompt, approvalRequest{Display: desc, Kind: "tcp_forward", Fingerprint: ssh.FingerprintSHA256(key), Action: guardian.Action{Kind: "network", Host: target.Host, Port: int(target.Port), Command: desc}})
			allowed := outcome.Allowed
			decisionType = outcome.Decision
			done()
			if !allowed {
				broker.recordAudit(audit.Entry{
					Kind:           "tcp_forward",
					Command:        desc,
					SandboxMode:    mode,
					NetworkAllowed: policy.Network,
					Decision:       decisionType,
					Blocked:        true,
					ExitCode:       126,
					Detail:         "Forwarding denied",
				})
				<-slots
				channel.Reject(ssh.Prohibited, "forwarding denied")
				return
			}
		}
		if !paired(key) {
			<-slots
			channel.Reject(ssh.Prohibited, "pairing revoked")
			return
		}
		started := time.Now()
		remote, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", dest)
		if err != nil {
			broker.recordAudit(audit.Entry{
				Kind:           "tcp_forward",
				Command:        desc,
				SandboxMode:    mode,
				NetworkAllowed: policy.Network,
				Decision:       decisionType,
				ExitCode:       1,
				DurationMS:     time.Since(started).Milliseconds(),
				Detail:         "Destination unavailable",
			})
			<-slots
			channel.Reject(ssh.ConnectionFailed, "destination unavailable")
			return
		}
		stream, requests, err := channel.Accept()
		if err != nil {
			remote.Close()
			<-slots
			return
		}
		go ssh.DiscardRequests(requests)
		// Keep the handler alive so parent/connection cancellation remains wired.
		stopIO := context.AfterFunc(ctx, func() { stream.Close(); remote.Close() })
		defer stopIO()
		defer func() { stream.Close(); remote.Close(); <-slots }()
		var copies sync.WaitGroup
		copies.Add(2)
		go func() { defer copies.Done(); io.Copy(stream, remote); stream.CloseWrite() }()
		go func() {
			defer copies.Done()
			io.Copy(remote, stream)
			if tcp, ok := remote.(*net.TCPConn); ok {
				tcp.CloseWrite()
			}
		}()
		copies.Wait()
		broker.recordAudit(audit.Entry{
			Kind:           "tcp_forward",
			Command:        desc,
			SandboxMode:    mode,
			NetworkAllowed: true,
			Decision:       decisionType,
			ExitCode:       0,
			DurationMS:     time.Since(started).Milliseconds(),
		})
	}
}
