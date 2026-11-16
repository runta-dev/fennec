# SSH backlog after application prioritization

Status: candidate backlog reduction implemented; WAN comparison and deployment
acceptance remain pending. The approximately 240 ms TUI P95 target is not met.

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

The Mac relay became unreachable before the WAN comparison launch was confirmed.
Check its task job state before launching another attempt; an observation
failure is not proof that a job never started. The candidate must still pass
the real comparison, throughput review, matching development-bundle startup
checks and the existing warm-operation gate. A fixed per-stream window also
does not establish the goal's aggregate backlog requirement. The goal remains
active, with unrelated traffic and host-wide shaping outside implementation scope.
