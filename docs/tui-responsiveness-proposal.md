# Proposal: responsive terminals under CPU and output load

Status: implemented and deployed; sustained-load gate passed; the separate
500 ms warm-operation gate remains unmet.
Owner: Fennec runtime and terminal transport.

The application bandwidth follow-up below is in progress. The implementation
and acceptance status of the original four items does not imply that the new
network latency target has been met.

Latest WAN check (11:22 UTC, November 16): the TCP output feedback candidate
had loaded ping P95 424 ms versus 333 ms without feedback. Feedback remains
experimental and default-off. The 240 ms target remains unmet; see the
[paired reports and limitations](ssh-backlog-investigation.md).

The terminal encoded output window is now **32 KiB**, retaining the user's
scrolling experiment. The previous 4 KiB window limited one terminal to about
16 KiB/s at 250 ms RTT. This was an interaction regression even when a small
ping could pass quickly. A future 64 KiB trial must measure scrolling,
full-screen repaint, typing and concurrent-terminal fairness alongside exact
ping and file throughput. The 16 KiB file/socket window is a separate setting.
Do not accept QoS changes using ping alone or restore the 4 KiB terminal cap.

The subsequent [SSH backlog investigation](ssh-backlog-investigation.md)
observed up to 33,592 TCP bytes not yet transmitted on the exact diagnostic
connection. A 16 KiB per-socket encoded window is now a candidate reduction
from 32 KiB. Its real WAN comparison showed partial improvement with lower
throughput and a 1.9 second outlier; it does not establish the 240 ms target.

## Follow-up: application bandwidth scheduling (2026-11-16)

Goal: bring exact TUI ping P95 close to 240 ms under conditions comparable to
the measured gateway keepalive P95 of 242 ms. This is an optimization target,
not a proven physical minimum. Also measure the extra interactive latency
caused by saturated file transfer; target no more than 20–30 ms at P95.

Host packet capture identified the same runtime reply entering the TAP and
leaving its IFB 169.472 ms and 178.950 ms later. Guest turnaround for those
requests was about 0.5 ms. The host currently sends all guest egress through
one 1048576 bit/s token bucket; its priority map does not separate SSH traffic.
The user's chosen scope is Fennec-managed SSH traffic, with application-level
scheduling and no required host QoS change. Traffic from unrelated guest
processes and public-network loss remain outside this application's guarantee.

The user explicitly reaffirmed this boundary after the background-egress
observation: delays caused by traffic outside Fennec do not count as a failure
of this design. Do not expand the implementation into shaping Claude/API HTTPS
or host-wide traffic. Acceptance must distinguish Fennec-generated congestion
from that outside traffic; retain noisy observations with their limitations
rather than presenting them as controlled before/after evidence.

A four-minute background observation recorded eth0 transmit-rate median
128,005 B/s and P95 152,169 B/s, with substantial Claude HTTPS traffic as well
as SSH. The first command-mode probe attempt failed argument validation and
produced no test load in that observation window; that diagnostic validation
bug was subsequently fixed. Guest counters do not establish the queue delay
of an individual ping. See the bounded
[background-egress summary](benchmarks/2026-11-16-unmanaged-egress.json).

### Required coverage

| Path | Existing behavior | Required integration |
| --- | --- | --- |
| Resident TUI and socket streams | Priority frame writer, per-stream 4 KiB output credit | Shared directional rate budget, fair streams, bounded aggregate pending writes |
| Setup, archive upload and other exec channels | Independent SSH channel writes | Small paced writes and controlled remote output, sharing the same budget |
| Mutagen SSH wrappers | Independent OpenSSH connections | Route through the managed scheduler; never claim coverage from TUI-only tests |
| Mac reverse connector and SSH relay | Opaque forwarding and multiple bridge sockets | Include both directions in the aggregate budget without changing authentication or approvals |
| Large protocol replies | Whole JSON frames | Bound serialization head-of-line blocking without interleaving invalid JSON |

Scheduling must happen before SSH/TCP buffering on the sending endpoint. A
Mac receive-side limiter cannot remove bytes already queued on the cloud.
Do not wrap a potentially window-blocked SSH channel in a connection-wide
serial callback without isolating that blockage: a stalled file reader must
not stop a different terminal. Similarly, independently limiting each channel
or process does not enforce an aggregate runtime budget.

### Scheduling foundation

`internal/traffic` provides a bounded scheduler with interactive and bulk
classes, a shared root token bucket, a separate bulk bucket, per-flow FIFO and
fair turns. Waiting bulk requests reserve neither root tokens nor every queue
slot, so a heartbeat can enter and preempt a bulk token wait. A bounded run of
interactive packets is followed by a bulk opportunity; tiny interactive
packets cannot perpetually consume the tokens needed by a larger bulk packet.

Current tuning values are 112 KiB/s total, 96 KiB/s bulk, a 2 KiB burst/atomic
packet limit, and 1 KiB data chunks with per-packet transport overhead charged
by the adapter. These values leave headroom below the host cap and must be
calibrated against measured wire traffic. Installed Mac and running cloud
binaries were subsequently verified at revision `c5864f1`, which includes these
settings; earlier notes below describe the deployment state at each measurement.
Cancellation never returns a caller's buffer while a running write still
references it. Oversized atomic writes are refused rather than waiting forever
for a bucket that cannot hold them.

The resident protocol now negotiates `traffic_packets_v1`. Both directions
then pace bounded packets through the scheduler. Ordinary small JSON frames
keep their existing encoding; large frames are split into independently
scheduled fragments and reconstructed before application dispatch. Incomplete
large messages do not block a pong from another stream. Receipt and first
paint still share one write when they fit, and same-stream messages retain
their order. Reassembly rejects out-of-order/oversized fragments and reserves
an aggregate 8 MiB budget. Terminal output chunks are 1 KiB; application
output retains priority over bulk socket data. Older peers remain on the
legacy wire format and do not gain the new rate/fragmentation guarantees.

Current status: the foundation and resident integration have passed Linux
race tests, terminal-service and CLI tests, vet and source-language checks.
Regression coverage includes a fresh ping completing while a 96 KiB operation
reply is still transferring, lossless reconstruction and image paste across
many fragments. No achieved WAN latency improvement is claimed yet.

### Shared cloud output broker

The cloud resident service now obtains its stdout writer from
`internal/trafficbroker`. A single per-user helper owns the root/bulk buckets
for all registered process outputs. Clients register a pipe/socket descriptor
over a private Unix socket and submit bounded packets with independent request
IDs, allowing a pong to enter while a bulk request waits for quota. Negotiated
resident packets go directly to this shared queue; wrapping the broker with a
second serial rate-limiter would restore head-of-line blocking.

The broker performs nonblocking writes only. An unwritable destination refunds
its unused token reservation and yields for 5 ms; writable peers continue.
Partial socket writes retain exclusive ownership of that destination until the
packet tail completes, while other destinations continue. Disk descriptors are
rejected. Cancellation closes IPC, drains queued callbacks, then closes passed
descriptors, preventing descriptor-reuse races. A canceled submitted packet is
not replayed because its write outcome is uncertain.

Admission is bounded to 128 registered clients, 64 outstanding packets per
client (56 bulk), and the scheduler's aggregate 64 slots (56 bulk). Packets carry
at most 1,792 data bytes and reserve 96 bytes of transport overhead. The helper
uses a versioned private directory, an exclusive owner lock and serialized
startup; duplicate starters never unlink a live owner's socket. It exits after
30 seconds without clients. A required scheduling compatibility change must use
a new broker directory version so old active sessions can drain normally.

Tests cover the aggregate rate across independent clients, a full destination
pipe with a responsive peer, interactive admission during a same-client bulk
quota wait, whole socket packets after backpressure, duplicate process startup,
resident fragmentation/ping through the broker, and inherited-pipe cancellation.

Coverage remains incomplete: this stage shares **cloud resident outputs**.
Mutagen and independent Mac senders do not yet share this budget. The command
and reverse connector integrations described below add more daemon traffic to
the resident's directional budgets. The reverse connector integration adds
reverse streams to the resident's directional budgets. Even registered stdout pipes can feed data into sshd's own
buffers when the far reader stalls. Receiver credits and a resume-burst test
are still required; a userspace broker alone does not establish a bound on
already-buffered network bytes. Development installation and full WAN acceptance
remain outstanding.

### Reverse connector on the resident channel

The daemon now uses a negotiated `device_relay_v1` socket stream for each reverse
bridge. Both directions share the resident's packet scheduler; socket uploads
are explicitly bulk, rather than inheriting keyboard input priority. The cloud
accepts a device ID from the fixed socket target, verifies its recorded relay
process and listener, and chooses the loopback port itself. Inner SSH identity,
host-key verification, command approvals and sandbox behavior remain at their
existing endpoints. The retained SSH transport worker remains opaque and keeps
its current compatibility epoch.

The net.Conn adapter processes input credits independently of application reads.
A stopped output consumer cannot prevent a concurrent upload from receiving its
credits, nor block the main protocol reader or another stream's ping. Payload
buffering is bounded by the existing byte window; timeouts use net.Conn deadline
semantics, and final output is drained before EOF. Closing the adapter closes
only its stream. Account/runtime scope is rechecked after an in-flight open.

Reverse streams have separate admission capacity: 64 active connections, eight
parked bridges, and eight server-side slots for retiring streams racing their
replacement. The usual terminal/control stream capacity remains available.
Legacy remote services use the previous direct-tcpip connector until upgraded;
the standalone legacy link path also remains outside the new coverage. Neither
fallback should be counted as successful aggregate scheduling.

For negotiated, paced socket streams, the encoded output credit allowance is
32 KiB. Terminal streams and unpaced legacy peers retain 4 KiB. The larger bulk
allowance covers the bandwidth-delay product without bypassing packet pacing.
An asynchronous 90 ms propagation delay in each direction demonstrated the
reason: a 128 KiB echo transfer exceeded the 4 s deadline with the 4 KiB window;
with 32 KiB it verified 128 KiB in each direction in 2.157 s (about 59.4 KiB/s
per direction), while six fresh pings stayed below the 260 ms test bound. This
is an emulated link result, not a WAN performance claim or the final 20–30 ms
added-P95 acceptance.

Additional tests cover stalled readers with simultaneous uploads, payload and
EOF integrity, deadlines, 80 occupied reverse stream slots with normal control
still available, account changes during opening, and replenishing parked
bridges with 24 active connections. `profile-ssh-latency -load-mode duplex` now
uses the same socket adapter with a dedicated synthetic echo endpoint and
verifies every byte in both directions. It does not claim to test real Mac SSH
authorization or Mutagen.

During validation, an existing Linux credential-watch failure was reproduced
with an immediate atomic replacement: the inotify watches were armed only after
the API returned. Initial registration is now synchronous; the regression test
fails before that fix and passes after it. The CLI race suite then passed.
Mac terminalstream/localssh/CLI/terminal-command race tests passed again after
the final window tuning was synchronized. The earlier detached WAN attempt
failed DNS resolution and supplied no valid measurements. A fresh 135-sample
run completed on 2026-11-16 at 07:14 UTC, verifying 2,097,234 load bytes per direction
over 43.335 seconds, but failed the latency target:

| Phase | Gateway P95 | Guest echo P95 | Exact TUI ping P95 |
| --- | ---: | ---: | ---: |
| Idle | 192.869 ms | 350.980 ms | 407.774 ms |
| Duplex socket load | 219.657 ms | 901.892 ms | 846.332 ms |
| Recovery | 161.783 ms | 353.560 ms | 342.535 ms |

The probe's idle and recovery phases also show substantial guest-path delay;
these were not isolated from other active traffic. This run does not identify
which sender caused the queueing, and independent percentile differences are
not component latency. The earlier 243.750 ms resident-reply result is not a
stable acceptance result. Raw samples are in
[duplex WAN results](benchmarks/2026-11-16-duplex-socket-ping.json).
No development installation has been performed for this stage.

### Non-PTY command streams

The negotiated `command_stream_v1` socket target now carries ordinary setup,
configuration synchronization and terminal installer stdin/stdout/stderr through
the same resident scheduler in both directions. `internal/commandstream` is a
small transport protocol independent of runtime/setup application packages;
the retained SSH carrier remains opaque. It transmits explicit stdin EOF and
exit status, bounds records, keeps command payloads in the bulk class, and
reserves separate admission for at most 16 commands so long-running transfers
cannot consume terminal/control slots. Commands run as the authenticated
resident owner, with the existing workload CPU boundary and a cancellable
process group. Closing the stream cancels the command even when stdin blocks.

An existing SSH client is associated with its resident dialer. Registrations
are retired when that SSH connection closes without deleting a newer replacement;
a failed resident cannot silently reopen an unpaced path during recovery.
Only cold bootstrap and explicitly unsupported old peers use a legacy exec
channel; neither is counted as scheduled coverage. Once a resident command is
sent, an uncertain outcome is never replayed on a fresh SSH channel. Tests
verify binary stream integrity, separate stderr, EOF, nonzero status, early
exit, blocked-input cancellation, registration replacement, bounded admission
and responsive exact pings during an actual streamed `cat` transfer.

Project archive upload now uses the same command session. Its receiver is a Go
`fennec-terminal receive-project` command, replacing the embedded Python path.
It verifies bounded manifests, member names/types/sizes, SHA-256, executable
modes and complete archive end markers before an atomic rename. Identity locks
serialize concurrent publishers, and existing snapshots are never replaced.
Regression tests reject traversal, symlinks, digest mismatch, duplicate and
unlisted members, truncation and competing publications. An integration test
sends a binary executable file through a resident command to the real receiver
entrypoint, then verifies that re-uploading cannot overwrite cloud edits.

Mutagen still needs integration. Cold bootstrap, older peers and standalone
senders also remain outside full aggregate coverage. This stage does not
resolve the separate warm-operation gate.

Linux and Mac race suites passed for command streams, setup, terminal transport,
CLI and terminal commands. The Go project receiver and its production-entrypoint
upload test also passed race tests on both platforms. These are source-level
checks; no replacement of the running application has occurred for this stage.
`profile-ssh-latency -load-mode command -terminal <staged-terminal>` can now
measure real `cat` commands through the resident instead of a synthetic echo
endpoint. It requires the negotiated capability, verifies bytes and exit status,
and records uploads separately from verified received bytes. Historical duplex
reports' `verified_load_bytes` is a per-direction payload count, not their sum.

The first valid command-load WAN run completed 90 timing comparisons but failed
before any full load payload was verified, with exit status 255. Its samples
are retained in [failed command-drain run](benchmarks/2026-11-16-command-drain-failure.json)
and are not acceptance evidence. It exposed a real output-lifetime bug:
`exec.Cmd.WaitDelay` started after a successful child exit while paced stdout
was still draining, truncating valid output. Output pipes are now drained
independently of process waiting; cancellation still closes both pipes and
terminates the process group. A regression with a 1.5 s paused output reader
fails against the previous server and passes with the fix.

The fixed server completed a fresh 135-comparison WAN run on 2026-11-16 at
07:48 UTC, using the staged production terminal binary and real `cat` command
streams. It verified 2,097,234 bytes in each direction over 46.483 seconds of
load, with successful exit statuses and no measurement errors. Mac command
and terminal-command race suites also passed with the fix. Timing remained
noisy:

| Phase | Gateway P95 | Guest echo P95 | Exact TUI ping P95 |
| --- | ---: | ---: | ---: |
| Idle | 192.515 ms | 402.302 ms | 398.017 ms |
| Command load | 204.835 ms | 403.097 ms | 580.341 ms |
| Recovery | 178.931 ms | 362.149 ms | 471.621 ms |

Raw [command-load samples](benchmarks/2026-11-16-command-stream-ping.json) prove
the transfer/drain behavior, not the latency target. Other traffic was not
isolated. The installed daemon used an earlier development build at this
measurement; subsequent verification found both installed Mac and running cloud
binaries at `c5864f1`. The current user experience therefore cannot be explained
by claiming that pacing has not been deployed.
The background-egress observation above was a separate time window; it cannot
attribute every slow sample in this run to external traffic. Do not treat
independent percentile differences as a Fennec queueing measurement.

The remaining Mutagen path includes both its long-running agent command stream
and the scp upload used for agent installation; see the
[upstream SSH transport implementation](https://github.com/mutagen-io/mutagen/blob/master/pkg/agent/transport/ssh/transport.go).
Both need daemon IPC into the existing authenticated resident, with pinned
account/runtime scope and bounded one-shot local stream offers. Independent
OpenSSH wrappers or one rate limiter per wrapper would not meet the aggregate
budget. Preserve command status and cancellation, and reject unsupported target
or forwarding options instead of silently changing their meaning.

`Transport.Ping` now measures an independent, uniquely identified round trip
through the production writer, recording the matching pong's receive time.
It never substitutes unrelated traffic or a coalesced health probe for a
measurement. Concurrent measurements and timed-out pending writes are bounded
to eight; timeout leaves the shared connection alive. The CPU/PTY load probe
also uses this exact measurement instead of timing `Probe`.

The new `cmd/profile-ssh-latency` runs rotated, sequential resident ping, guest
echo and gateway keepalive samples over one authenticated connection. Its
optional dedicated server generates deterministic large operation replies;
the client verifies each complete reply's SHA-256. It records every successful
sample and an error on failure, including idle, bulk and recovery phases. It
uses the paired identity and pinned host keys without modifying the installed
application. Example (stage the Linux build at the remote path first):

```sh
go run ./cmd/profile-ssh-latency -remote /tmp/fennec-qos-latency -samples 60
```

A first Mac-to-cloud run on 2026-11-16 at 06:02–06:04 UTC used 45 samples per
phase and the staged current protocol. No installed application was replaced.

| Phase | Gateway P95 | Guest echo P95 | Exact TUI ping P95 |
| --- | ---: | ---: | ---: |
| Idle | 159.565 ms | 262.465 ms | 231.667 ms |
| Resident bulk replies | 191.952 ms | 266.724 ms | 318.736 ms |
| Recovery | 159.172 ms | 228.406 ms | 210.547 ms |

All 135 comparisons completed, with 1,747,695 verified JSON reply bytes over
28.061 s of load (about 60.8 KiB/s of logical reply content; wire framing is
additional). The maximum loaded ping was 927.383 ms. This does **not** pass the
latency goal: the contemporaneous network baseline is substantially below the
earlier 242 ms observation, and resident pacing alone leaves tail latency.
These are separate empirical distributions, not additive component costs.
This test covers resident replies only, not full SSH file-transfer coverage.
The remaining SSH adapters, deployment and full acceptance are still pending.

A second staged run at 06:21–06:23 UTC enabled the shared cloud output broker,
again with 45 samples per phase:

| Phase | Gateway P95 | Guest echo P95 | Exact TUI ping P95 |
| --- | ---: | ---: | ---: |
| Idle | 221.517 ms | 187.402 ms | 223.443 ms |
| Resident bulk replies | 225.351 ms | 239.015 ms | 243.750 ms |
| Recovery | 184.341 ms | 221.755 ms | 224.160 ms |

All 135 comparisons completed. The client verified 2,097,234 JSON reply bytes
over 34.216 s of load (about 59.9 KiB/s of logical content), and the maximum
loaded ping was 272.044 ms. This is promising evidence near the requested
240 ms target for resident traffic. It is not a controlled attribution of the
change from the first run: the network distributions changed, and ordinary
guest echo also changed. Independent phase percentiles describe observed
distributions, not per-packet queue delay. Broader SSH coverage, long-duration
tests and an installed development build are still required before completion.
The raw per-sample records are preserved in
[resident pacing](benchmarks/2026-11-16-resident-pacing-ping.json) and
[shared output](benchmarks/2026-11-16-shared-output-ping.json).

Linux and Mac race suites passed for the scheduler, broker, resident protocol
and terminal command. The cancellation command test needed a short temporary
home on macOS to fit its Unix socket path limit; the test executable also
dispatches the real broker entrypoint when launched by the resident. Vet,
source-language and UI checks passed. These checks do not replace the unchanged
500 ms installed warm-operation gate, whose earlier failures remain unresolved.

### Same-connection comparison after the reported regression

The user reported that approximately 300 ms had become more than 500 ms.
The earlier 395 ms observation and the later 580 ms command-load P95 used
different workloads and time windows. They establish neither a regression nor
an improvement. Checking actual build metadata also corrected a stale deployment
assumption: the installed Mac application (`0.3.11161602-dev`) and running cloud
resident both contained clean revision `c5864f1` by 08:06 UTC. Pacing was deployed.

`profile-ssh-latency -baseline` now compares an old resident built from `184d080`
(before application pacing) with the staged candidate built from `1af2ac6`
(the same transport implementation as deployed `c5864f1`). Both Linux services
were built with Go 1.26.11, stay warm on one authenticated SSH connection, and
use fresh exact ping IDs. Baseline load uses an ordinary SSH `cat` channel;
candidate load uses the paced resident command stream. Both send the same
deterministic bytes in 4 KiB producer writes, with at most 64 KiB of unconfirmed
application data. Every completed block drains stdin/stdout and verifies every
byte and successful command exit. Eight blocks alternate in BAAB ABBA order
(B is candidate), with no overlapping test transfers. Gateway and guest echo
measurements rotate sequentially with ping. Other live traffic is uncontrolled.

Two earlier attempts are retained, not discarded:

- [Unbounded producer attempt](benchmarks/2026-11-16-ab-unbounded-failure.json):
  idle ping P95 was 204.479 ms old / 209.081 ms new, with 24 samples each.
  Baseline load then ended with EOF before a completed bulk comparison.
- [64 KiB window attempt](benchmarks/2026-11-16-ab-window-failure.json):
  baseline load yielded four timing comparisons, ping 1,465–2,977 ms, then
  guest echo EOF. Candidate load was never reached. The disconnect cause
  was not established; a heartbeat timeout is not proven by EOF alone.

The first complete [balanced comparison](benchmarks/2026-11-16-ab-balanced.json)
ran at 08:23:52–08:27:12 UTC with probe source `e4d4100`. All 192 timing
comparisons and all eight transfer blocks completed without error:

| Phase | Samples per variant | Old TUI P95 | New TUI P95 | Old gateway P95 | New gateway P95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Before test load | 24 | 468.479 ms | 406.692 ms | 205.084 ms | 203.425 ms |
| File-transfer load | 48 | 1,019.848 ms | 596.551 ms | 211.008 ms | 210.393 ms |
| After test load | 24 | 362.214 ms | 269.950 ms | 285.696 ms | 206.857 ms |

During load the old/new ping medians were 609.016 / 239.712 ms. The new median
is not a passing P95. Guest echo P95 was 973.944 / 560.085 ms, showing that
slow loaded samples also affected the ordinary guest path. Sequential
percentiles cannot identify the delay of a particular packet or establish
that remaining latency comes from unmanaged traffic.

The old transfer verified 7,086,080 bytes per direction in 77.806 s (88.94 KiB/s
aggregate effective payload rate); the candidate verified 2,080,768 bytes per
direction in 47.688 s (42.61 KiB/s). Rates include startup and final draining.
This approximately halves payload throughput while reducing observed loaded
ping P95 by about 42%. This comparison tests the whole pacing/transport change;
it does not isolate the priority scheduler's benefit from the rate reduction.
All four candidate block maxima (514–680 ms) remained below all four old block
maxima (868–1,137 ms), but the changing background load limits causal precision.

Decision: the complete comparison supports a partial improvement under file
load, not a stable 240 ms result. It supplies no evidence for a fixed 200 ms
idle regression from the change. Do not roll back useful pacing solely on the
unmatched historical comparison, and do not expand transfer coverage as a
substitute for resolving the remaining tail latency. Further diagnosis must
separate application waiting, already-buffered managed bytes and guest-path
delay; unmanaged traffic remains outside the requested implementation scope.
The goal, full transfer coverage and installed warm-operation gate remain open.

The probe's EOF/drain tests passed with race detection on Linux and macOS.
Source-language/localization and UI checks passed. This A/B run started only
dedicated diagnostic services; it did not restart the production app or TUI.

### Matched application-queue timing and command packet overhead

The next diagnostic adds no wire messages and changes no rate or priority.
`Transport.MeasurePing` reports the matched ID, exact RTT and elapsed local
send duration; `Ping` retains its existing API and admission/cancellation
semantics. A dedicated probe-server observer records each measured ping's
interval from complete input-line receipt to broker submission, and from
submission to broker completion. It preserves the broker writer interface
and wire bytes. Only IDs and durations are retained in a bounded private file;
payloads and credentials are never logged. Existing files are not overwritten.

All durations use their own host's monotonic clock. Broker completion means
writing to sshd's pipe, not network delivery. A fast reply can arrive before
the local write returns, so these intervals must not be treated as disjoint
components or subtracted from independently calculated percentile values.

The staged probe at source `bac4102` ran at 08:40:41–08:42:44 UTC. All 135
client IDs matched server records, without dropped, pending or failed trace
entries. It verified 1,747,695 command bytes per direction over 45.593 s.
The [client records](benchmarks/2026-11-16-ping-queue-client.json) and
[server records](benchmarks/2026-11-16-ping-queue-server.json) show:

| Phase | Exact TUI P95 | Local send P95 | Server dispatch P95 | Broker-write P95 |
| --- | ---: | ---: | ---: | ---: |
| Before load | 581.119 ms | 0.280 ms | 0.264 ms | 1.190 ms |
| Command load | 778.953 ms | 0.295 ms | 0.299 ms | 3.308 ms |
| After load | 357.030 ms | 0.226 ms | 0.297 ms | 2.288 ms |

For the exact slowest ping (`measure-74`), RTT was 835.530 ms, local send
0.297 ms, server dispatch 0.129 ms and broker completion 1.715 ms. The maximum
loaded broker interval was 4.229 ms. These observed application intervals do
not explain the hundreds of milliseconds of tail delay. Already-buffered SSH
bytes, scheduling outside the observed intervals and network/host queues are
not measured by these callbacks. The loaded gateway P95 also changed to
346.823 ms, so this run is not a before/after comparison with the earlier
210 ms gateway run. No attribution of each slow sample to unmanaged traffic
is justified.

A subsequent read-only worker check still showed the 1048576 bit/s guest TBF
and a 23,410-byte, 21-packet backlog. This is a separate queue snapshot, not
the residence time of a traced ping. Host configuration was not changed.
The current guest process check found no running Mutagen agent; integrating
that missing path remains necessary for coverage but would not by itself
explain this particular test's tail latency.

Inspection also found avoidable command framing overhead. Each binary record
previously wrote its five-byte header separately from the payload. The resident
socket's net.Pipe preserved these write boundaries, producing tiny data frames
and credit messages for every content chunk. Headers and content now share
one bounded write, with up to 1,019 content bytes plus the five-byte header in
a 1 KiB resident chunk. Receivers continue accepting the old 1,024-byte payload
limit, so either endpoint can be upgraded first. Larger initial command records
retain their existing bound and socket fragmentation.

A real resident `cat` regression verifies the complete 64 KiB binary upload
and counts actual input frames: the old implementation produced 131 frames,
67 tiny; the packed implementation produces 67 frames, one tiny. The test was
run against the previous protocol using a Go source overlay and failed there,
then passed with the fix. Compatibility tests exercise old-style full-size
input and output records. Binary/stderr/exit, cancellation, slow-output drain,
capacity and loaded-ping tests passed with race detection on Linux and Mac.
This packet reduction is not itself evidence that the WAN latency target passes.

The packed staged probe (`a670b97`) completed another 135 comparisons at
08:50:55–08:52:25 UTC. All client/server IDs matched and all 1,747,695 bytes per
direction were verified, with 37.776 s of command load (45.18 KiB/s effective
payload throughput per direction). Raw
[client results](benchmarks/2026-11-16-packed-command-client.json) and
[server timings](benchmarks/2026-11-16-packed-command-server.json) are retained:

| Phase | Gateway P95 | Guest echo P95 | Exact TUI P95 |
| --- | ---: | ---: | ---: |
| Before load | 161.279 ms | 196.191 ms | 176.347 ms |
| Command load | 215.237 ms | 616.529 ms | 411.418 ms |
| After load | 208.895 ms | 230.855 ms | 214.250 ms |

Loaded local-send/server-dispatch/broker-write P95 values were
0.275 / 0.304 / 2.298 ms; the largest loaded broker interval was 2.450 ms.
The slowest ping was 735.843 ms, with matching local intervals
0.197 / 0.055 / 0.395 ms. The observed intervals again fail to explain the
remaining tail. Different network conditions prevent attributing the change
from 778.953 ms to 411.418 ms to record packing; even the unloaded gateway and
guest distributions changed. The 240 ms WAN target and the 20–30 ms added-P95
gate still have no passing full-coverage result.

At the subsequent build check, the installed Mac binary and running cloud
resident both had clean revision `c40492b`, which includes the prior pacing
work but predates record packing. The packed change is committed and tested
with staged processes; this task has not installed or restarted the app to
apply it. Source synchronization is separate from installed-build acceptance.

### Managed project synchronization and migration

Revisions `0ba1dca` through `ab59885` route scoped Mutagen SSH commands through
the existing daemon resident. The adapter also covers the first agent upload:
the private scp wrapper selects the managed SSH wrapper, whose SFTP subsystem
request uses the production Go `sftp-stdio` entrypoint. The wrapper accepts only
the fixed cloud target and supported noninteractive options. The control plane
checks the authenticated owner, runtime and lifecycle again after recovery;
uncertain command outcomes are never replayed. Remote exit status is retained.
`syncProject` now uses the same managed command path.

Existing registered sessions are migrated only in the matching owner/runtime's
private Mutagen daemon and wrapper directory. Active sessions pause/resume to
release their previous connection, while paused sessions remain paused. Session
identifiers and synchronization history are preserved. Persistent transition
intent and the shared registry lock allow interrupted migration to recover and
explicit user pause intent to prevail. Migration runs in the background after
resident readiness, not on the interactive launch path.

Linux/macOS focused race tests passed in the preceding implementation work.
The Mac migration acceptance at 09:30 UTC passed its package race suites but
failed the real integration at the first post-flush readiness observation;
`Flush` returned no error, but the session did not satisfy `Ready`. That attempt
did not log the observed session state. Its
[failure log](benchmarks/2026-11-16-managed-migration-failure.txt) is retained.
The test now records the complete isolated-fixture state on such a failure;
readiness criteria and timeouts have not been relaxed.

A fresh isolated run at 09:37:39–09:39:47 UTC passed cold agent installation,
two-way binary transfer, executable mode, credential exclusion, deletion,
active/paused migration with unchanged session IDs, and post-migration file
verification. It completed 2,442 exact resident pings, maximum 2.619209 ms,
over 124.545 seconds of integration work. The
[passing log](benchmarks/2026-11-16-managed-migration-pass.txt) does not explain
the earlier intermittent readiness failure. Both endpoints were local fixtures;
this is neither WAN acceptance nor proof of installed-daemon migration.

### Current command measurement after migration work

At 09:40:22–09:42:20 UTC, a dedicated probe built from `ab59885` completed all
135 exact comparisons using the production packet scheduler, shared output
broker and command adapter. All 1,747,695 bytes in each direction were verified;
the load lasted 37.687 seconds, about 45.29 KiB/s effective payload throughput
per direction. All client/server ping IDs matched, with no failed, pending or
dropped trace entries. The [client report](benchmarks/2026-11-16-current-command-client.json)
and [server report](benchmarks/2026-11-16-current-command-server.json) show:

| Phase (45 samples each) | Gateway P95 | Guest echo P95 | Exact TUI P95 |
| --- | ---: | ---: | ---: |
| Before generated load | 193.738 ms | 397.902 ms | 450.025 ms |
| Paced command load | 223.195 ms | 753.402 ms | 637.152 ms |
| After generated load | 187.186 ms | 363.128 ms | 407.844 ms |

Loaded local-send/server-dispatch/broker-write P95 values were
0.227 / 0.159 / 1.888 ms. The slowest exact ping (`measure-120`) took
1,769.591 ms, while its respective observed local intervals were
0.219 / 0.080 / 0.525 ms. These measured application intervals do not account
for the large tail. Bytes already handed to SSH, TCP loss/retransmission,
unobserved client scheduling and host/network queues remain outside these
trace intervals. No individual slow sample has yet been attributed among them.

A read-only worker check during this run still found the unchanged 1048576
bit/s shaper, with a 6,364-byte / 12-packet backlog. This snapshot is not a
packet residence measurement. No host QoS or unrelated traffic was changed.
The installed Mac binary was still clean `cfb56da`, which contains pacing and
record packing but predates managed Mutagen integration. The WAN probe used
dedicated staged services, not an application install or a TUI restart.

This repeat confirms that the remaining WAN tail is real. It does not establish
a before/after regression from the previous 411 ms or 580 ms observations,
whose background/network conditions differed. The earlier balanced A/B remains
the comparable evidence: partial loaded latency improvement with a substantial
throughput tradeoff, not achievement of the target. Next diagnosis must examine
the managed bytes already handed to SSH; faster application queue callbacks
alone cannot establish the required end-to-end improvement.

### Acceptance

Use unique matched ping IDs; background heartbeat coalescing is unsuitable for
exact RTT measurements. Interleave protocol, ordinary echo and gateway
keepalive on one authenticated connection, report their sample distributions,
and never subtract independent percentiles as a component cost. Test idle,
bulk upload/download and sustained terminal output, including a stalled reader
and multiple sessions. Record throughput as well as latency, errors, ordering,
reconnections and aggregate backlog. Keep the existing warm-operation and
installed startup checks visible, including any remaining failures.

## Problem and evidence

During 2026-11-15 23:00 through 2026-11-16 02:00 China Standard Time,
the cloud journal recorded 122 successful SSH authentications, nine connection
timeouts, three broken pipes, and 26 inotify allocation failures. These are
server-wide observations, not an exact count of a user's TUI reconnections.
Missing per-attachment telemetry prevents attribution of every interruption.
The D-Bus leak was addressed separately by ee22ea7; this proposal does not
assume CPU saturation caused that incident.

The incident runtime had two vCPUs. SSH, the resident terminal service, Herdr,
and agents shared a CPU cgroup with weight 100 and nice 0. Network IPQoS does not
reserve CPU time. An unpredicted key also waits for the cloud render round trip
(historically about 200–300 ms). The old sessions.send benchmark measures a
different RPC path and must not be presented as per-keystroke latency.

## 1. Protect interactive CPU scheduling

Create sibling cgroup-v2 control and workload groups in a Fennec-owned subtree.
Use CPU weights 1000 and 100 respectively, with no CPU quota or dedicated core.
Set interactive terminal processes to nice -5 where supported; agents start
at nice 0. Reserve no idle capacity: workloads can use spare CPU. Do not change
the entire ssh.service, whose descendants include agents and unrelated work.

An exec wrapper enters the workload group before starting an agent, so all
descendants inherit the correct group. Integrate actual resident launches and
app-server launches, not just a shell-only legacy path. Protect Herdr and the
resident's SSH ancestors only after verifying their identities. Reconcile
existing Fennec agent descendants without killing or restarting sessions.
Treat unsupported or denied scheduling controls as a reported degraded mode,
not a launch failure. Do not use realtime scheduling or modify unrelated PIDs.

## 2. Prioritize control traffic and bound output contention

Keep one shared SSH connection and resident channel. Schedule ping/pong,
input credits, and keystrokes ahead of queued bulk traffic, while preserving
ordering within each session and receipt/output/exit ordering. Bound queued
work, keep terminal data lossless, and use small output chunks and fair turns
between producers. Cancellation must release waiters and blocked IO.

Priority cannot preempt bytes already written to SSH/TCP or unlock a blocked
writer. Tests must cover congested output, cancellation, ordering and progress
of another session; do not merely assert queue implementation details. Never
hide a broken connection by increasing the heartbeat deadline.

## 3. Preserve immediate local input feedback

Retain adaptive composer-aware predictive echo and its safe fallback for
unsupported keys/layouts. Validate real recorded Claude/Codex typing while
streaming and with long transcripts. Measure local prediction time separately
from authoritative cloud echo confirmation, and expose aggregate timings only
in private diagnostics. Avoid logging prompt text or credentials. A local
prediction is not proof that an agent received or executed input.

## 4. Verify under sustained contention

Provide a reproducible Go load probe using labeled, task-owned processes.
Compare idle and busy samples while saturating both vCPUs and streaming output
on multiple sessions. Verify actual input/output and heartbeat round trips,
process scheduling readback, and child inheritance. Always clean up only probe
processes, including on timeout. Keep a deadline independent of the transport.

Acceptance targets (not current guarantees):

- Ordinary supported local prediction P95 below 50 ms.
- Busy minus idle authoritative echo P95 below 50 ms on the same transport;
  report cloud-local and real Mac-to-cloud results separately.
- No unexpected disconnects or lost/misordered input over a 30-minute run.
- Existing package/race checks, source/UI checks, and the unchanged 500 ms
  warm-operation suite. Failed gates remain visible and incomplete.

A successful synthetic echo probe does not prove a busy agent's event loop is
responsive. Retain recorded-agent tests and real installed startup checks.
Network outages, host scheduling pauses, memory exhaustion and agent-internal
blocking remain outside any CPU-weight guarantee.

## Rollout and rollback

Implement and validate scheduling, transport fairness, prediction measurement,
then sustained-load acceptance. Build a complete development bundle from one
snapshot using the stable signer and credential helper. Preserve existing
sessions and verify the installed startup path. Do not publish a release as
part of this task. Record observed settings, measured outcomes and limitations
below; do not label unrun or failed acceptance as complete.

Rollback restores default CPU weights/nice for the exact managed processes and
uses the prior application bundle. Existing agent sessions must survive.

## Implementation and results

### Implemented behavior

- `internal/cpusched` creates control/workload sibling cgroups with weights
  1000/100 and applies nice -5/0 to all existing threads. The resident reconciles
  verified Herdr, terminal helpers, authenticated SSH ancestors and managed
  agent descendants. The SSH listener remains in `ssh.service`, nice 0.
  `workload-exec` fences actual Herdr child and Codex app-server launches.
  Missing host delegation remains a diagnostic degraded mode.
- The shared frame writer has a bounded queue and prioritizes input/credits/
  heartbeat traffic across streams. Same-stream FIFO is preserved, and bulk
  traffic gets a turn after at most eight urgent writes. Output chunks are
  2 KiB while the accepted legacy wire maximum remains 16 KiB.
- New consumers request `wire_output_window_v1`, promising per-frame credit
  return. Each stream then has a 4 KiB encoded JSON/base64 output allowance in
  addition to its existing 64 KiB original-byte window. Partial/coalesced
  credits release the corresponding frame reservations. Old consumers retain
  their original window. Compressible screen paints can still transmit tens
  of KiB without waiting for another round trip. This limits per-stream SSH
  backlog; it does not impose a connection-wide cap for arbitrary stream counts.
- Echo acknowledgement records input/output association before transport
  waiting, and cannot report successful echo while output is waiting for
  credit. Local prediction paint P50/P95 is recorded separately from cloud
  confirmation, without prompt contents.
- `cmd/profile-tui-load` runs a PTY echo child, two CPU burners, two output
  producers, input equality checks and heartbeat probes. It checks every child
  thread's workload placement, and continuously checks the server's control
  placement. Its Mac mode uses the production Go SSH implementation and
  TCP_NODELAY policy with the existing identity and pinned host keys.

### Measurements on 2026-11-16

| Boundary | Idle echo P95 | Loaded echo P95 | Added P95 | Result |
| --- | ---: | ---: | ---: | --- |
| Mac-to-cloud, final transport, 60 s / 120 inputs | 223.913 ms | 266.377 ms | 42.464 ms | Pass; zero disconnects |
| Cloud-local, independent 30 min / 17,618 inputs | 6.143 ms | 3.523 ms | -2.620 ms | Pass; zero disconnects |

The complete cloud run lasted 1,800.09 s and consumed 117,932,032 output bytes.
Every echo was checked for exact content/order. Heartbeat P95 was 0.435 ms.
Forty host-CPU samples at 30 s intervals over the final 20 minutes all reported
0% idle. The server checked its control cgroup and every thread's nice -5 every
five seconds; load children checked workload/nice 0 at startup. All owned load
processes exited and the independent systemd job reported success. The negative
P95 increment reflects baseline/environment variation, not evidence that adding
CPU load speeds up terminals. User workloads remained running throughout.

The WAN short run used two CPU workers and two pseudo-random printable output
streams, each producing 32 KiB/s before backpressure. It consumed 1,249,280
output bytes and measured heartbeat P95 257.619 ms. Before encoded output
limits, a Go-SSH diagnostic exceeded its 3 s echo deadline; a separate plain
OpenSSH diagnostic also exceeded the 50 ms increment gate. Neither is treated
as an equivalent before/after baseline, and no diagnostic timeout is hidden.
A 30-minute WAN run was not completed; the real-network result is explicitly
short-duration evidence and does not establish long-term WAN reliability.

Mac recorded Claude/Codex typing tests, including streaming and long transcripts,
passed the 50 ms local-paint gate. Observed local paint P95 ranged from 0.383 to
0.625 ms. These are replay wall times, not an end-to-end measurement through a
busy live agent. Recorded cloud confirmation latency remains separately logged.

Focused package tests, terminal transport race tests, Go vet, source-language
and UI checks passed. New regressions cover encoded backlog, compressed paints,
partial credits, slow-peer isolation, cancellation and echo acknowledgement
ordering. The unchanged installed 500 ms warm-operation gate failed under
the independent synthetic load: six `sessions.send` samples took 770–965 ms,
and usable TUI launch took 551.29 ms. Attach/resume (285–329 ms), model changes
(285–304 ms), reads and stops passed. Most slow-send time was inside Herdr's
send operation, not the transport writer. With all probe load removed, seven
send samples still exceeded the gate (670–986 ms), as did one read (530.98 ms).
Usable TUI launch then passed all three samples (389–437 ms). The daemon identity
remained unchanged through each suite. This separate RPC/startup acceptance
remains incomplete; the gate is not waived and the implementation does not
claim to resolve these failures. See
[warm-operation results](benchmarks/2026-11-16-tui-warm-operations.json).

The development bundle uses the existing `Fennec Local Development` signer.
The credential-helper SHA-256 remains
`f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`.
The installed broker successfully launched cloud `codex-cli 0.169.0`; its owned
test session exited normally. Three sessions live immediately before the
development install remained live afterwards. The merged development build
`0.3.11161052-dev` (application source `a1822d4`) is installed. Its cloud terminal
SHA-256 `311ef698c729451d274c7ec441bb8c829db28da5e10bb5481229f4a2116dcd62`
matches the running cloud installation. Connector and broker are ready;
Claude/Codex routes are configured. Cursor's existing missing-credential status
is unchanged.

The Mac relay investigation independently confirmed successful direct Mac SSH
while an older multiplexed route failed. Retiring the old ControlMaster restored
the alias. A later interruption also affected fresh/direct connections and
recovered afterwards, so stale multiplexing is not a proven explanation for
every outage. Do not count 122 server authentications as 122 TUI reconnections.

### Reproduction and measurement boundaries

Build the probe and terminal service from the same source. Run the cloud probe
outside an agent's descendant tree: a resident restart correctly reclassifies
agent descendants as workload, which would invalidate a temporary probe placed
under that tree. A task-owned transient systemd service provides an independent
parent and deadline:

```sh
go build -o /tmp/fennec-tui-probe ./cmd/profile-tui-load
go build -o /tmp/fennec-tui-terminal ./cmd/fennec-terminal
systemd-run --unit=fennec-tui-acceptance --property=RuntimeMaxSec=33m \
  --property=Nice=-5 \
  --property=StandardOutput=append:/tmp/fennec-tui-result.json \
  --property=StandardError=append:/tmp/fennec-tui-progress.log \
  /tmp/fennec-tui-probe -terminal /tmp/fennec-tui-terminal -duration 30m
```

Use a fresh unit/output path for each run. For WAN measurements, build the same
probe on the Mac and run it through an independent `cmd/run-macos-check` job:

```sh
/tmp/fennec-tui-probe-mac -remote /tmp/fennec-tui-probe \
  -terminal /tmp/fennec-tui-terminal -duration 30m
```

Do not combine overlapping probe baselines, count interrupted runs as 30-minute
acceptance, or equate process priority with a hard latency guarantee. Cgroups
cannot remove WAN latency or make an internally blocked agent render input.
