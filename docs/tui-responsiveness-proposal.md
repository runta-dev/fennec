# Proposal: responsive terminals under CPU and output load

Status: implementation in progress. Owner: Fennec runtime and terminal transport.

## Problem and evidence

During 2026-11-15 23:00 through 2026-11-16 02:00 China Standard Time,
the cloud journal recorded 122 successful SSH authentications, nine connection
timeouts, three broken pipes, and 26 inotify allocation failures. These are
server-wide observations, not an exact count of a user's TUI reconnections.
Missing per-attachment telemetry prevents attribution of every interruption.
The D-Bus leak was addressed separately by ee22ea7; this proposal does not
assume CPU saturation caused that incident.

The runtime currently has two vCPUs. SSH, the resident terminal service, Herdr,
and agents share a CPU cgroup with weight 100 and nice 0. Network IPQoS does not
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

Pending.
