# Proposal: responsive terminals under CPU and output load

Status: implemented and deployed; sustained-load gate passed; the separate
500 ms warm-operation gate remains unmet.
Owner: Fennec runtime and terminal transport.

The application bandwidth follow-up below is in progress. The implementation
and acceptance status of the original four items does not imply that the new
network latency target has been met.

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

Initial tuning values are 112 KiB/s total, 96 KiB/s bulk, a 2 KiB burst/atomic
packet limit, and 1 KiB data chunks with per-packet transport overhead charged
by the adapter. These values leave headroom below the host cap and must be
calibrated against measured wire traffic. They are not deployed settings.
Cancellation never returns a caller's buffer while a running write still
references it. Oversized atomic writes are refused rather than waiting forever
for a bucket that cannot hold them.

Current status: the foundation has focused race tests for rate envelopes,
priority under saturation, FIFO/fairness, cancellation and lossless chunking.
Production adapters, shared cross-process ownership, deployment and WAN
acceptance remain outstanding. No achieved latency improvement is claimed yet.

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
