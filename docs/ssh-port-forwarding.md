# SSH port forwarding through the Mac relay

The cloud computer's paired-device SSH alias supports local and dynamic
forwarding (`-L`, `-D`) and remote TCP forwarding (`-R`). Both the cloud terminal
binary and Mac connector must include remote-forwarding support. Alias
registration uses `relay-v4` ControlMaster sockets so older masters are not reused.

To preview a service running on cloud loopback port 8765 on the Mac, run on the
cloud computer:

```sh
ssh -NT -o ExitOnForwardFailure=yes \
  -R 127.0.0.1:8765:127.0.0.1:8765 machine
```

Keep this SSH process running and open `http://localhost:8765` on the Mac.
The first address is the Mac listener; the destination is resolved and connected
by the cloud SSH client. Use an unused Mac port when an existing service already
owns 8765. Port zero requests are supported and return the allocated port.

## Transport and policy

- The relay forwards `tcpip-forward` and `cancel-tcpip-forward` global requests,
  including success payloads and cancellation without a requested reply.
- Mac-initiated `forwarded-tcpip` channels return to the originating SSH client.
  Destination and origin metadata, channel rejection, data and half-close pass
  through unchanged. Unknown global requests and channel types remain rejected.
- Each listener belongs to its authenticated SSH connection, including the
  assigned port for dynamic allocations. Another connection cannot cancel it.
- The Mac applies the same network approval policy and audit category as `-L`.
  Full access does not prompt; reviewed access uses the existing broker. Pairing
  is rechecked after approval, on accept, and while forwarding remains active.
- `localhost` binds IPv4 loopback; explicit IPv4 and IPv6 addresses are supported.
  Empty or `*` binds IPv4 wildcard. Arbitrary DNS names are not accepted as listen
  addresses. Use an explicit loopback address for local-only previews.
- Cancellation stops new accepts while allowing existing streams to finish.
  Disconnect, connector shutdown, or pairing revocation closes listeners and
  active streams. Revocation also closes the affected SSH connection, including
  pending channel opens. Capacity is bounded at 16 listeners and 64 active
  reverse streams across the connector.
- Once a relay connection has requested reverse forwarding, losing its Mac
  transport closes the local SSH connection too. Listeners are never silently
  recreated or requests replayed after uncertain replies. Start a new SSH command
  to restore the forward. Exec-only relay connections retain their recovery path.

## Validation

`go test -race ./internal/localssh ./cmd/fennec-terminal` covers real connector
registration, dynamic port allocation and cancellation, connection ownership,
large payloads, half-close responses, approval and denial, malformed requests,
capacity limits, pairing revocation and disconnect cleanup. The OpenSSH regression
uses a real `ssh -NT -R` process and ControlMaster `-O cancel` against the relay;
it skips only when the OpenSSH executable is unavailable.

These local tests validate protocol behavior, not deployment. Verify an actual
Mac HTTP request through the paired alias after installing matching development
artifacts before declaring the installed feature ready.
