# 128 KiB/s budget experiment

Client: **Mac, Singapore / Singtel**, reported by the user. Destination:
**GCP runta-integration / us-west1**, via `ssh.runta.xyz`. GCP inventory was
checked around 12:04 UTC; the previously identified worker is
`runta-integration-worker-xfs-04` in `us-west1-b`. Exact runtime placement and
VPN/proxy routing were not re-resolved for each block. These are cross-region
measurements, not US-local latency.

The user requested an experiment at 128 KiB/s after the 160 KiB/s experiment
failed to show a stable benefit. All three variants use source
`e53f4ba5c0228ba643e5e5269953c9e9dd7dc80b` exported from Git. Only the scheduler
defaults and diagnostic broker directory differ. Mac and cloud budgets match.
The installed application was not replaced and remains on `ad8c138` with
160/128 KiB/s budgets, the dynamic terminal window and SSH relay fixes.

| Variant | Total budget | Bulk budget | Application budget difference |
| --- | ---: | ---: | ---: |
| A | 160 KiB/s | 128 KiB/s | 32 KiB/s |
| B | 128 KiB/s | 128 KiB/s | 0 KiB/s |
| C | 128 KiB/s | 96 KiB/s | 32 KiB/s |

A versus B changes only the total-rate parameter. B versus C changes only
the bulk-rate parameter. All retain interactive priority, 2 KiB bursts, the
16 KiB socket window, and default-off TCP feedback. The dynamic terminal
window still derives from the variant's total rate and stays within 8–64 KiB.
Each variant has an isolated broker directory so production traffic cannot
spend its application tokens. The host link and background traffic remain shared.

The run lasted **12:29:48–12:35:07 UTC on November 16, 2026**, ordered
**A B C C B A C A B B A C**. Every block opens a fresh authenticated SSH
connection, keeps its resident and guest echo channels warm, and measures
10 idle, 10 loaded and 10 recovery sample triplets. Protocol ping, equal-size
guest echo and gateway keepalive rotate sequentially. Load is repeated
resident `cat` with fixed payloads, exact SHA-256 verification and successful
command exit. All 12 blocks, 360 sample triplets and transfer validations
completed. Each aggregate below contains **40 samples per variant per phase**.

| Aggregate metric | A: 160/128 | B: 128/128 | C: 128/96 |
| --- | ---: | ---: | ---: |
| Loaded TUI P95 | 375.403 ms | 701.584 ms | 685.429 ms |
| Loaded TUI median | 185.212 ms | 200.897 ms | 245.379 ms |
| Loaded TUI maximum | 437.549 ms | 802.399 ms | 771.993 ms |
| Loaded gateway P95 | 232.370 ms | 219.323 ms | 204.875 ms |
| Effective payload per direction | 37.956 KiB/s | 33.675 KiB/s | 29.926 KiB/s |
| Idle TUI P95 | 396.345 ms | 367.855 ms | 393.697 ms |
| Recovery TUI P95 | 347.336 ms | 352.733 ms | 348.163 ms |

Neither 128 KiB/s setting improved measured latency or throughput in this run.
This is not proof of an isolated causal regression: connections and time windows
differ, and individual blocks vary substantially. Retain the slow blocks. Do
not combine these results with the earlier 260/304 ms comparison or select only
the best block. The per-block maxima below each summarize **10 loaded samples**;
the aggregate P95 above is calculated from all 40, not averaged from block P95s.

| Block | Variant | Loaded TUI maximum | Effective payload per direction |
| --- | --- | ---: | ---: |
| 1 | A | 213.317 ms | 48.419 KiB/s |
| 2 | B | 241.341 ms | 46.942 KiB/s |
| 3 | C | 249.241 ms | 46.444 KiB/s |
| 4 | C | 730.587 ms | 29.628 KiB/s |
| 5 | B | 212.794 ms | 47.577 KiB/s |
| 6 | A | 437.549 ms | 39.665 KiB/s |
| 7 | C | 771.993 ms | 18.759 KiB/s |
| 8 | A | 395.473 ms | 27.117 KiB/s |
| 9 | B | 802.399 ms | 17.905 KiB/s |
| 10 | B | 228.823 ms | 48.519 KiB/s |
| 11 | A | 309.444 ms | 44.165 KiB/s |
| 12 | C | 418.223 ms | 39.879 KiB/s |

Throughput counts completed application payload and includes command/drain time.
This single command stream does not establish saturation of every configured
budget or measure raw link capacity. The experiment contains no UI scroll or
typing input. It does not locate time spent in application, SSH/TCP or host
queues, and gateway P95 must not be subtracted from TUI P95 to infer that split.
Uncontrolled background traffic is a limitation, not an established explanation.

Both 128 KiB/s variants passed the existing traffic and trafficbroker race
tests before measurement. No production default or installed application was
changed. [Raw data and complete provenance](2026-11-16-budget128-comparison.json)
include all blocks, timestamps, source version, Linux/Mac binary checksums,
configuration, boundaries and sample counts. The benchmark catalog indexes
the artifact by SHA-256.
