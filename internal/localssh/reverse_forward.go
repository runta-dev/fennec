package localssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"fennec/internal/audit"
	"fennec/internal/guardian"
	"fennec/internal/sandbox"
	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

type reverseBind struct {
	Host string
	Port uint32
}

type reverseOwner struct {
	conn *ssh.ServerConn
	bind reverseBind
}

// reverseForward implements RFC 4254 remote TCP listeners (ssh -R). The
// library's stock handler shares a map across users and does not enforce
// pairing revocation, so listener ownership and lifetime are managed here.
func reverseForward(parent context.Context, paired func(server.PublicKey) bool, broker *Broker, approval bool) server.RequestHandler {
	var mu sync.Mutex
	listeners := make(map[reverseOwner]net.Listener)
	listenerSlots := make(chan struct{}, 16)
	streamSlots := make(chan struct{}, 64)
	return func(connection server.Context, _ *server.Server, request *ssh.Request) (bool, []byte) {
		var bind reverseBind
		if (request.Type == "tcpip-forward" && !request.WantReply) || ssh.Unmarshal(request.Payload, &bind) != nil || bind.Port > 65535 {
			return false, nil
		}
		conn, ok := connection.Value(server.ContextKeyConn).(*ssh.ServerConn)
		if !ok {
			return false, nil
		}
		owner := reverseOwner{conn, bind}
		if request.Type == "cancel-tcpip-forward" {
			mu.Lock()
			listener := listeners[owner]
			if listener != nil {
				delete(listeners, owner)
				listener.Close()
			}
			mu.Unlock()
			return listener != nil, nil
		}
		if request.Type != "tcpip-forward" {
			return false, nil
		}
		host := bind.Host
		switch host {
		case "localhost":
			host = "127.0.0.1"
		case "", "*":
			host = "0.0.0.0"
		default:
			if net.ParseIP(host) == nil {
				return false, nil
			}
		}
		key, _ := connection.Value(server.ContextKeyPublicKey).(server.PublicKey)
		if parent.Err() != nil || connection.Err() != nil || !paired(key) {
			return false, nil
		}
		select {
		case listenerSlots <- struct{}{}:
		default:
			return false, nil
		}
		ctx, cancel := context.WithCancel(connection)
		watchPairing(ctx, cancel, func() bool { return paired(key) }, time.Second)
		stopParent := context.AfterFunc(parent, cancel)
		retained := false
		defer func() {
			if !retained {
				stopParent()
				cancel()
				<-listenerSlots
			}
		}()
		policy := broker.effectivePolicy()
		mode := sandbox.NormalizeMode(policy.Mode)
		decision := "auto_sandbox"
		desc := fmt.Sprintf("Reverse TCP forwarding listening on %s", net.JoinHostPort(host, strconv.Itoa(int(bind.Port))))
		record := func(blocked bool, code int, detail string) {
			broker.recordAudit(audit.Entry{Kind: "tcp_forward", Command: desc, SandboxMode: mode, NetworkAllowed: !blocked, Decision: decision, Blocked: blocked, ExitCode: code, Detail: detail})
		}
		full := policy.Approval == "never"
		onRequest := policy.Approval == sandbox.PresetAsk || policy.Approval == sandbox.PresetAuto
		if approval && !full && (!onRequest || (mode != sandbox.ModeFull && !policy.Network)) {
			prompt, done := context.WithTimeout(ctx, 2*time.Minute)
			outcome := broker.authorize(prompt, approvalRequest{Display: desc, Kind: "tcp_forward", Fingerprint: ssh.FingerprintSHA256(key), Action: guardian.Action{Kind: "network", Host: host, Port: int(bind.Port), Command: desc}})
			done()
			decision = outcome.Decision
			if !outcome.Allowed {
				record(true, 126, "Reverse forwarding denied")
				return false, nil
			}
		}
		if ctx.Err() != nil || !paired(key) {
			return false, nil
		}
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(bind.Port))))
		if err != nil {
			record(false, 1, "Listen address unavailable")
			return false, nil
		}
		actualPort := uint32(listener.Addr().(*net.TCPAddr).Port)
		owner.bind.Port = actualPort
		mu.Lock()
		if listeners[owner] != nil || ctx.Err() != nil || !paired(key) {
			mu.Unlock()
			listener.Close()
			return false, nil
		}
		listeners[owner] = listener
		mu.Unlock()
		retained = true
		// A revoked pairing also closes pending channel opens, for which SSH has
		// no per-request cancellation. Disconnecting a revoked peer is intentional.
		stopIO := context.AfterFunc(ctx, func() { listener.Close(); conn.Close() })
		record(false, 0, fmt.Sprintf("Listening on %s", listener.Addr()))
		go func() {
			var streams sync.WaitGroup
			defer func() {
				listener.Close()
				mu.Lock()
				if listeners[owner] == listener {
					delete(listeners, owner)
				}
				mu.Unlock()
				<-listenerSlots
				// Canceling the listener stops new accepts but preserves established
				// streams, as OpenSSH does. Pairing/connection cancellation still kills all.
				streams.Wait()
				stopIO()
				stopParent()
				cancel()
			}()
			for {
				tcp, err := listener.Accept()
				if err != nil {
					return
				}
				if ctx.Err() != nil || !paired(key) {
					tcp.Close()
					cancel()
					return
				}
				select {
				case streamSlots <- struct{}{}:
				default:
					tcp.Close()
					continue
				}
				streams.Add(1)
				go func() {
					defer streams.Done()
					defer func() { <-streamSlots }()
					defer tcp.Close()
					stopTCP := context.AfterFunc(ctx, func() { tcp.Close() })
					defer stopTCP()
					origin := tcp.RemoteAddr().(*net.TCPAddr)
					payload := ssh.Marshal(struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}{bind.Host, actualPort, origin.IP.String(), uint32(origin.Port)})
					stream, requests, err := conn.OpenChannel("forwarded-tcpip", payload)
					if err != nil {
						return
					}
					defer stream.Close()
					stopStream := context.AfterFunc(ctx, func() { stream.Close() })
					defer stopStream()
					go ssh.DiscardRequests(requests)
					done := make(chan struct{})
					go func() { io.Copy(stream, tcp); stream.CloseWrite(); close(done) }()
					io.Copy(tcp, stream)
					tcp.(*net.TCPConn).CloseWrite()
					<-done
				}()
			}
		}()
		if bind.Port == 0 {
			return true, ssh.Marshal(struct{ Port uint32 }{actualPort})
		}
		return true, nil
	}
}
