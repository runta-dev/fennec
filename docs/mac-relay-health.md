# Mac relay connection health

The cloud Mac relay keeps bulk data and health probes on separate inner SSH
connections. Both use the existing resident Mac-to-cloud link; this does not
create another gateway connection or daemon. Both authenticate the paired cloud
identity and verify the paired Mac host key.

## Why separation is necessary

A keepalive in a data connection shares its encrypted, ordered byte stream with
bulk transfers and its global-request lock with forwarding approvals. Giving
that keepalive a fixed wall-clock deadline can kill a healthy connection.
[The original investigation](mac-relay-heartbeat-investigation.md) reproduced a
successful reply delayed by more than 21 seconds behind an uncompressed file.

The dedicated health connection carries only bounded probes. It has its own
resident byte stream and SSH request lock, so data already queued in the target
connection cannot delay its probe. Normal commands continue to use preopened
session channels on the data connection.

## Connection-specific verification

Before publishing a new data connection, the relay registers its authenticated
SSH session ID with the Mac connector. The connector keeps that ID only while
the connection's context is alive. A probe on the control connection asks about
that exact ID. It succeeds only if the target is alive, both connections belong
to the same paired public key, and pairing remains valid.

The health handlers do not execute commands, open arbitrary sockets, or bypass
approval. Queries cannot inspect another device's connections. Registry entries
are removed on disconnect and bounded at 256. Registration happens before any
user operation, so approval cannot block it.

## Failure and shutdown behavior

The probe interval and per-attempt deadline both remain **five seconds**.
A lost control connection is replaced once and the original data session is
rechecked. No command, listener, or data stream is replayed or replaced during
that control recovery. An explicit negative session/pairing response closes the
data connection immediately. If two control attempts cannot confirm liveness,
the data connection closes. A completely unresponsive peer is therefore retired
within one interval plus two attempt budgets (15 seconds, excluding scheduler
stalls), while direct data-connection errors still retire it immediately.

Cancellation closes both connections, including handshakes and pending probes.
Late control recovery cannot publish a new connection after shutdown. A failed
old data generation cannot invalidate a recovered generation. Each relay uses
one additional authenticated resident byte stream; the existing relay connection
capacity bounds both streams.

Aliases use `relay-v5` ControlMaster sockets so newly started commands do not
reuse an older in-band watchdog. Older Mac connectors that do not implement
registration are compatible: probes still use an independent authenticated
connection, but can verify only the peer, not the exact data session. Installing
both current components enables the complete session-specific check.

## Verification

`go test -race ./internal/localssh ./cmd/fennec-terminal` includes:

- A paced megabyte-scale transfer queued longer than the old watchdog's budget,
  without control redial or data loss.
- A forwarding approval that holds the data connection's global-request lock
  across more than two probe intervals.
- A silently blackholed control connection and failed reconnect, bounded by the
  interval plus two five-second attempts.
- An isolated control failure recovered without replacing the data connection.
- Host-key verification on the separate control connection; same-device,
  wrong-device, malformed, unknown and expired session queries.
- Existing reverse-forwarding, pairing revocation, cancellation, generation
  fencing, command output, half-close and real OpenSSH regressions.

Live acceptance must use the uncompressed file from the original investigation
and compare its hash. Compression is an optional HTTP optimization, not the
liveness repair. Record installed versions and both successful transfer and
connection continuity; merely seeing an open listener is insufficient.
