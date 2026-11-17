package localssh

import (
	"errors"

	"golang.org/x/crypto/ssh"
)

// Global requests are ordered and never replayed: a lost reply may already
// have allocated a listener. A transport failure retires this local connection
// so OpenSSH notices that its remote forwards are gone instead of keeping a
// misleadingly healthy ControlMaster. Ordinary exec-only relays still recover.
func (u *macUpstream) relayRequests(requests <-chan *ssh.Request) {
	for request := range requests {
		switch request.Type {
		case "keepalive@openssh.com":
			if request.WantReply {
				request.Reply(true, nil)
			}
		case "tcpip-forward", "cancel-tcpip-forward":
			// A port-zero allocation cannot be used without a success reply.
			if request.Type == "tcpip-forward" && !request.WantReply {
				continue
			}
			client, err := u.connect()
			if err != nil {
				if request.WantReply {
					request.Reply(false, nil)
				}
				continue
			}
			u.mu.Lock()
			u.forwardUsed = true
			u.mu.Unlock()
			ok, payload, err := client.SendRequest(request.Type, request.WantReply, request.Payload)
			if err != nil {
				if request.WantReply {
					request.Reply(false, nil)
				}
				u.downstream.Close()
				return
			}
			if request.WantReply {
				request.Reply(ok, payload)
			}
		default:
			if request.WantReply {
				request.Reply(false, nil)
			}
		}
	}
}

// Remote listeners belong to this relay's authenticated Mac connection. Copy
// the RFC 4254 destination/origin payload unchanged to the originating client.
func (u *macUpstream) relayReverseChannel(client *ssh.Client, incoming ssh.NewChannel) {
	u.mu.Lock()
	allowed := !u.closed && u.client == client && u.forwardUsed
	u.mu.Unlock()
	if !allowed || u.downstream == nil {
		incoming.Reject(ssh.Prohibited, "no remote forwarding connection")
		return
	}
	local, localRequests, err := u.downstream.OpenChannel("forwarded-tcpip", incoming.ExtraData())
	if err != nil {
		var rejected *ssh.OpenChannelError
		if errors.As(err, &rejected) {
			incoming.Reject(rejected.Reason, rejected.Message)
		} else {
			incoming.Reject(ssh.ConnectionFailed, "forwarding client disconnected")
		}
		return
	}
	defer local.Close()
	remote, remoteRequests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer remote.Close()
	relayStreams(local, localRequests, remote, remoteRequests)
}
