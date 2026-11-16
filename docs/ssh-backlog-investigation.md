# SSH backlog after application prioritization

Status: the 16 KiB window passed a WAN comparison with partial improvement.
TCP queue feedback is a new candidate; deployment acceptance remains pending.
The approximately 240 ms TUI P95 target is not met.

## Finding

Application queue priority does not protect a pong from file data already
accepted by SSH/TCP. On the exact diagnostic connection, a slow pong was handed
to sshd in less than 1 ms while TCP already held about 19 KiB of unsent data.
The socket accumulated up to 33,592 unsent bytes during the run and experienced
93 retransmitted TCP segments. These observations identify managed send backlog
as a concrete optimization opportunity; they do not attribute every slow ping
to a single cause or establish that Fennec caused the underlying packet loss.

The file/socket encoded output window is reduced from 32 KiB to 16 KiB as a
candidate fix. The root/bulk rates, per-packet priority, terminal window and
wire protocol remain the same. Existing per-stream credits continue to enforce
lossless delivery and reader backpressure. This is a per-stream bound; it is
not a new aggregate bound across all streams or a TCP delivery guarantee.

## Evidence from one connection

The staged command probe ran at 09:56:36–09:58:21 UTC on November 16, 2026,
using the previous 32 KiB output window. All 90 timing comparisons completed,
all client/server IDs matched, and 1,398,156 bytes were verified in each
direction over 52.797 seconds of load (25.86 KiB/s effective payload throughput
per direction). Other live traffic was uncontrolled.

| Phase, 30 samples each | Gateway P95 | Guest echo P95 | TUI P95 |
| --- | ---: | ---: | ---: |
| Before generated load | 190.882 ms | 335.512 ms | 404.599 ms |
| Paced command load | 219.898 ms | 1,107.129 ms | 1,343.875 ms |
| After generated load | 237.252 ms | 402.631 ms | 410.457 ms |

The [client report](benchmarks/2026-11-16-tcp-queue-client.json) and
[server report](benchmarks/2026-11-16-tcp-queue-server.json) preserve the exact
ping IDs. Server timestamps were added only to the diagnostic observer, without
changing wire messages. Monotonic durations remain authoritative. Wall times
are used only to associate socket observations on the same guest.

A read-only observer located the probe's SSH ancestor socket using its validated
`SSH_CONNECTION` tuple and sampled `TCP_INFO` plus `TIOCOUTQ` every 50 ms. It
inspected only that connection, retained the duplicated descriptor until probe
exit, and did not modify socket options. `TCP_NODELAY` was already enabled.
The [correlation summary](benchmarks/2026-11-16-tcp-queue-summary.json) contains
the surrounding observations for the slowest pings and these 2,021 samples:

| Socket observation | P95 | Maximum |
| --- | ---: | ---: |
| Queued bytes, including unacknowledged bytes | 32,156 | 39,272 |
| Bytes not yet transmitted | 26,560 | 33,592 |
| Guest-to-worker TCP RTT estimate | 202.023 ms | 211.281 ms |

The congestion window ranged from 1 to 26 segments, median 4. Counters increased
by 93 retransmitted segments and 127,356 retransmitted bytes. These are transport
counters, not a count of application retries or TUI reconnections.

For `measure-65`, TUI RTT was 1,468.483 ms; local send, server dispatch and
broker write took 0.123 / 0.104 / 0.339 ms respectively. A socket sample 2.794 ms
before broker completion already showed 25,248 queued bytes, including 19,456
not yet transmitted, with a congestion window of 3 segments. The following
sample showed 25,396 unsent bytes and a window of 2 segments. This places a
large TCP backlog next to the observed fast application write; it is not a
calculation of this particular pong's exact residence time in that backlog.

The worker capture connection ended early. Its 18 matched TAP-to-IFB packet
pairs had 130–199 ms residence times, but it captured only 2,480 payload bytes
from the diagnostic SSH connection and did not cover the command-load phase.
It must not be used to attribute the loaded ping tail. The other-traffic byte
counters in the summary likewise cover that partial capture, not the complete
benchmark. No host QoS or unrelated process traffic was changed.

## Candidate selection and outstanding validation

An 8 KiB candidate failed the unchanged propagation-delay throughput regression:
the bidirectional 128 KiB transfer could not finish within 4 seconds at 180 ms
RTT. The failure remains a rejection of that fixed window, not a reason to
relax the test. A 16 KiB candidate completed the same transfer in 2.338 seconds
and passed the existing ping bound. These are local simulated-delay results.

With the candidate applied after merging `88eabcb`, Linux race tests passed for
terminalstream, commandstream and the latency probe. Vet, source-language and
UI checks passed. The diagnostic probe's Mac race tests passed before the relay
outage; Mac validation of the 16 KiB candidate is still pending.

`profile-ssh-latency -baseline-paced` can now compare two paced resident
implementations on one authenticated connection, without confounding a window
change with the old ordinary-SSH load path. Both variants use identical producer
chunks, application flight limits, byte verification, EOF/drain checks and
balanced load blocks. Baseline capability validation still rejects accidental
path mismatches.

Matching staged Linux terminals were built from `88eabcb` plus the diagnostic
changes and the candidate window. An overlay restores only the 32 KiB constant
for the baseline. Their SHA-256 values are:

- 32 KiB: `48d754e25d15ae96029c8b342ba37989585fd4b86db5b429856cfd26ae10859a`
- 16 KiB: `c5037983a8fffa8beee9143dbbc361078a0b069d795eb775f842f07f367e97fd`

## Resumed WAN window comparison

The durable Mac job completed successfully on November 16 at 10:53:38 UTC.
Mac race tests passed for terminalstream, commandstream and the probe before
measurement. The measurement itself ran from 10:51:09 UTC for 148.847 seconds,
using the immutable binaries above and `-baseline-paced -samples 12`.
The [raw report](benchmarks/2026-11-16-window16-comparison.json) retains all
samples and verified byte counts. Both services stayed warm on one SSH
connection; eight BAAB ABBA blocks produced 48 loaded samples per variant.

| Phase | 32 KiB TUI P95 | 16 KiB TUI P95 | 32 KiB gateway P95 | 16 KiB gateway P95 |
| --- | ---: | ---: | ---: | ---: |
| Idle | 202.440 ms | 267.798 ms | 201.662 ms | 202.706 ms |
| Bulk | 677.652 ms | 479.759 ms | 193.208 ms | 217.000 ms |
| Recovery | 202.811 ms | 203.014 ms | 201.509 ms | 203.241 ms |

Effective aggregate payload throughput per direction fell from approximately
48.4 to 39.2 KiB/s (19%). The candidate's worst loaded ping was **1,918.099 ms**,
versus 785.675 ms for baseline. All bytes, drains and exits verified. Temporal
variation was substantial, and the final baseline block was faster than nearby
candidate blocks. This is partial improvement, not stable 240 ms achievement.

## TCP queue feedback candidate

The version-2 output broker can observe the exact Linux SSH TCP connection.
Discovery requires a validated SSH tuple, an SSH ancestor executable, and that
ancestor's ownership of the read end of the resident's actual output pipe.
`pidfd_getfd` duplicates the matching socket for observation; neither socket
options nor host QoS are modified. The descriptor is closed after queued broker
callbacks finish, including cancellation. Unsupported or denied discovery keeps
the existing paced behavior and does not advertise `tcp_output_guard_v1`.

Before each whole bulk packet, the serial broker callback checks TCP unsent
bytes plus unread output-pipe bytes. More than 1,024 bytes defers the packet
without spending tokens. Interactive packets and partial packet tails retain
their existing admission rules. Other destinations can still progress. The
check excludes unacknowledged flight, which must not impose an RTT-sized pause
on healthy transfers. An attached observer error closes the client rather than
silently disabling protection midstream.

These snapshots are not atomic, and sshd's internal buffers remain unobserved.
This is an output-side feedback mechanism, not proof of a hard aggregate bound
or protection in both directions. Existing brokers drain without interruption
in their version-1 directory. During coexistence, their budgets are independent.
The comparison report records actual guard capability for both variants.
Real TCP tests cover stalled receivers, interactive/peer progress, resumed bulk,
pipe backlog, cancellation and descriptor release. WAN validation, full signed
bundle startup, and the unchanged warm-operation gate remain required.
