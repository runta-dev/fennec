# Mac relay heartbeat false disconnects (November 17, 2026)

This is the original diagnosis and controlled experiment. The subsequent
[connection-health implementation](mac-relay-health.md) separates authenticated
health probes from the data stream without increasing the probe timeout.

## Finding

The reproduced disconnects were initiated by the cloud Mac relay's own
five-second keepalive deadline. A bulk transfer delayed the keepalive reply
behind bytes in the same ordered SSH connection. The relay closed the upstream
connection while the Mac was still processing that stream. This is not evidence
that the Mac was offline.

`macUpstream.watch` in `internal/localssh/mac_relay.go` sends
`keepalive@openssh.com` every five seconds and starts a five-second timer around
`client.SendRequest`. On timeout it calls `client.Close()` unconditionally.
The timer includes queuing, writing, and waiting for the reply, not just a
network round trip. SSH global requests are serialized by `globalSentMu` in
`golang.org/x/crypto/ssh`, and share the connection's ordered byte stream with
channel data. The resident device relay carries those encrypted bytes through a
flow-controlled socket stream; it cannot move an inner SSH keepalive ahead of
already queued inner SSH data.

Reverse forwarding makes the failure visible immediately: `invalidate` closes
the downstream SSH connection after loss of the upstream connection rather than
leaving a dead remote listener attached to a seemingly healthy ControlMaster.
That cleanup is intentional; the erroneous liveness decision happens earlier.
The keepalive deadline predates reverse-forwarding support and also affects exec
and file-transfer connections.

## Controlled reproduction

Both runs used separate diagnostic SSH connections through the normal paired
Mac relay, with `ControlPath=none`. They forwarded a cloud-loopback HTTP server
to Mac loopback and downloaded the same uncompressed `echarts.min.js` file
(1,034,102 bytes). The file's SHA-256 is:

```text
bf4a223524e40b77c304bec67e1222cf551f14880cf42c69dc046558e11c07b1
```

- **Five-second production deadline:** the diagnostic relay logged
  `closing upstream: keepalive timed out after 5.000372228s`, followed by
  `upstream connection ended: ... use of closed network connection`.
  Curl received HTTP 200 but only 688,640 bytes, then exited 18 with
  345,462 bytes missing. During the timed-out probe the raw SSH socket still
  received 72 bytes and wrote 52 bytes. A preceding independent diagnostic
  connection had also logged the five-second timeout and local close.
- **Thirty-second diagnostic deadline:** the heartbeat reply arrived after
  **21.518349223 seconds with no error**. Curl received all 1,034,102 bytes,
  exited zero, and matched the original SHA-256. Subsequent heartbeat waits
  returned to sub-second values. The only behavioral change in this diagnostic
  connection was its keepalive wait limit.

The delayed successful reply demonstrates a false liveness failure under this
workload. It does not establish the cause of every historical reconnect or a
universally safe replacement timeout. In particular, increasing the timeout to
30 seconds can still fail with a larger backlog.

## Provenance and limits

- Source: `2bcf83084a7703c9c17a31300c22ce1207f0a27f`, plus an uncommitted,
  diagnostic-only patch recording keepalive outcomes and socket byte counters.
  The production source was restored after the experiment.
- Diagnostic executable SHA-256:
  `721438efa0e0669687670d1bb0064c1dc4e67be9292b70c887abecf565b96f65`.
- Diagnostic patch SHA-256:
  `767c4c220c372be3d076b65e64c71d5ca2e773fe706baa40e891aad3bea7ff80`.
- Client: the paired Apple Silicon Mac. Its physical location, access network,
  and network provider were not verified.
- Server: the user's Runta cloud computer, Linux 6.12.8, x86_64. Infrastructure
  provider, region, and zone were not verified.
- Cloud log UTC labels: baseline timeout at 2026-11-17T11:51:43Z; delayed
  successful control reply at 2026-11-17T11:53:24Z. Mac wall-clock labels were
  ahead of the cloud clock; do not directly subtract timestamps across hosts.
- Measurement boundary: Go's monotonic elapsed time around the inner SSH
  `SendRequest` call, including all queuing and I/O waits. These are not
  client-to-gateway latency, raw network RTT, or UI latency measurements.
- Samples: two logged baseline timeout incidents; one baseline download with
  complete curl evidence; one matched-file control download with hash validation.
  No statistical latency or reliability claim is made from these counts.
- The Mac daemon remained PID 98944 during the inspected window. Its uptime
  was eight days. No cloud `ssh.service` restart was recorded in the queried
  11:30-11:42 UTC window. These observations rule out those specific restart
  explanations for that window, not all possible physical network disturbances.

Raw evidence and the diagnostic patch are retained in the cloud task directory
`/root/.fen/river-lone/relay-investigation/`: `baseline-5s.log`,
`control-30s.log`, `mac-baseline-download.log`, `mac-control-download.log`, and
`diagnostic-only.patch`. The diagnostic services and listeners were stopped.
The existing preview service retains its original production timeout and
explicit reconnect policy.

## Repair direction

Replace the fixed wall-clock deadline around a potentially queued global request
with a liveness policy that accounts for connection progress and pending
requests. Separate time spent waiting to send from time waiting for a response,
and use the existing authenticated resident transport's health state where
appropriate. Keep a bounded no-progress deadline and immediate handling of real
connection errors. Do not replay a command or recreate listeners after an
uncertain request outcome.

Regression coverage should include a large ordered transfer delaying an inner
keepalive beyond five seconds while the connection remains healthy, an approval
holding the global request lock, and a genuinely stalled peer that still closes
within the specified no-progress bound. This investigation does not deploy that
behavior change.
