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

## Live acceptance, 2026-11-17

Both components were built from `16d4724338f01d5fb98796a8f05bd9f7d87a8360`.
The installed Mac development package was `0.3.11172013-dev`, signed by
`Fennec Local Development`; its running daemon and packaged control build ID
both matched `b18eb6fb6d935f0578ae3d41f77fc1fc7a813fb7c03ab1b660c3997891c34074`.
The active cloud terminal SHA-256 was
`1b45185691f82fd0b97be0b5f9ee554fbac3a199f8ba3864f47fd5551cd98bea`
(the package's embedded Linux artifact). Alias registration selected `relay-v5`.
The credentials helper's hash was unchanged across installation.

Provenance: the HTTP client ran on the paired Mac, Darwin 25.6.0 arm64;
its physical location, access network and provider were not established.
The HTTP server ran on the user's Runta cloud computer, Linux 6.12.8 x86_64;
underlying infrastructure provider, region and zone were not established.
Traffic crossed the existing resident Mac/cloud link through one SSH reverse
forwarding process, with independent data and health streams. These are loaded
end-to-end HTTP transfer measurements, not local rendering, raw RTT or
unloaded gateway latency. UTC starts below are from the Mac clock; elapsed
times use its monotonic clock. Host clocks were not synchronized, so no
cross-host timestamp subtraction was used.

The client disabled HTTP compression and made three sequential GETs of the
original ECharts asset, followed by one upload with a server-side hash response.
There were no application retries. The SSH service had `Restart=no`,
`ControlPath=none`, PID `2162398` before and after, and `NRestarts=0`.
Each GET measurement covers request start through complete response body;
the upload covers request start through receipt and decoding of the server's
hash response. The 90-second HTTP client limit is a test harness bound, not a
change to the relay's five-second health deadline.

| Operation | Mac UTC start | Bytes | Elapsed | Validation |
| --- | --- | ---: | ---: | --- |
| Uncompressed GET 1 | 2026-11-17T12:18:50.027857Z | 1,034,102 | 22.893 s | SHA-256 matched |
| Uncompressed GET 2 | 2026-11-17T12:19:12.922172Z | 1,034,102 | 20.517 s | SHA-256 matched |
| Uncompressed GET 3 | 2026-11-17T12:19:33.443591Z | 1,034,102 | 20.696 s | SHA-256 matched |
| Upload and hash response | 2026-11-17T12:19:54.143937Z | 1,179,648 | 18.842 s | SHA-256 matched |

All three downloads matched
`bf4a223524e40b77c304bec67e1222cf551f14880cf42c69dc046558e11c07b1`;
the upload matched
`25312360b1fe3c58bb4488e99073c2023f8128906c854ac9f5cbd5eb2d1ae13d`.
The probe exited zero and the same SSH process remained active. This reproduces
the original uncompressed workload across multiple health intervals without
using compression, timeout enlargement or tunnel restart to hide failure.
Four successful operations are a targeted regression check, not a guarantee
against all future network outages. Silent blackholes, approval blocking and
control-only recovery are additionally covered by the race-tested regressions.

Linux and macOS race tests passed for `internal/localssh` and
`cmd/fennec-terminal`; UI, source-language and localization checks passed.
After installation the connector reported connected, takeover reported
`broker_ready=true`, and the existing cloud sessions remained live.
Raw transfer results and connection snapshots are retained in the task's
cloud workspace at `/root/.fen/river-lone/relay-health-fix/`.
