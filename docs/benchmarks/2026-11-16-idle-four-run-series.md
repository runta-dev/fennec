# Four additional unloaded QoS comparisons

128/96 KiB/s had the lowest observed TUI ping P95 in three of four new runs
and in the pooled samples. The fourth run reversed the ranking. Exploratory
paired uncertainty intervals include zero, so the series favors 128/96 in
its observed pooled tail but does not establish a repeatable causal advantage.
No application installation or production-budget change was made.

## Provenance and method

- Client: Mac in **Singapore / Singtel**, user-reported. VPN/proxy routing was
  not captured. This is not a US-local measurement.
- Destination: `ssh.runta.xyz`, **GCP runta-integration / us-west1**. Earlier
  inventory at approximately 12:04 UTC identified cluster
  `runta-integration-31c80598`; previously identified worker
  `runta-integration-worker-xfs-04`, zone `us-west1-b`. Exact runtime placement
  was not re-resolved per sample.
- Measurement: **2026-11-16 13:22:43–13:40:06 UTC** (21:22:43–21:40:06 Singapore
  time). Four consecutive runs, each with a fresh authenticated connection.
  Within each run, three persistent resident channels shared that connection.
- Workload: no generated bulk transfer, scrolling or agent input. Existing
  live traffic and host load were uncontrolled. The Mac sender remained at
  **160/128 KiB/s**; only staged cloud budgets varied.
- Counts: **126 sample triplets per variant per run**, **504 per variant**,
  1,512 triplets total. Every triplet contains resident ping, guest echo and
  gateway keepalive RTT. All four runs completed; batch exit code was zero.
  Nine warm-up pings per run were excluded as specified by the probe.
- Order: the corrected 18-round design jointly balances resident position
  and probe position. Initial mappings were A/B/C, B/C/A, C/B/A and B/A/C,
  where A=160/128, B=128/128 and C=128/96 KiB/s (total/bulk).
- Boundary: exact resident protocol ping RTT, small JSON guest echo RTT and
  SSH gateway keepalive RTT, measured on the Mac using monotonic elapsed time.
  These have different endpoints. No UI rendering or perceived typing latency
  was measured; subtracting independent P95 values is not a component estimate.
- Source: cloud variants exported from
  `e53f4ba5c0228ba643e5e5269953c9e9dd7dc80b` with budget and isolated broker
  directory overrides. Corrected Mac diagnostic source is committed as
  `d6558529d39d5869b74a45421cece1a87be4d7ff`. Immutable binaries match the
  corrected prior repeat; exact source and binary SHA-256 values are retained
  in the raw artifact. No compilation ran during the sampling period.
- Settings: 2 KiB bursts, 16 KiB socket credit, adaptive 8–64 KiB terminal
  window, interactive priority, TCP output feedback default-off. Test broker
  token budgets were isolated; the host network remained shared.

## Results

Each table entry below is TUI protocol ping P95 in milliseconds. P95 uses the
nearest-rank estimator; pooled P95 is calculated from all 504 observations per
variant, not by averaging the four per-run percentiles.

| Run (UTC) | 160/128 | 128/128 | 128/96 |
| --- | ---: | ---: | ---: |
| 1: 13:22:43–13:26:35 | 362.321 | 365.029 | 359.891 |
| 2: 13:26:35–13:32:02 | 688.609 | 514.451 | 504.346 |
| 3: 13:32:02–13:36:18 | 405.023 | 461.426 | 367.252 |
| 4: 13:36:18–13:40:06 | 341.849 | 365.921 | 384.360 |
| Pooled, 504 samples each | 453.563 | 437.857 | 385.814 |

| Cloud total/bulk KiB/s | TUI median | TUI P95 | Guest echo P95 | Gateway P95 |
| --- | ---: | ---: | ---: | ---: |
| 160/128 | 199.068 ms | 453.563 ms | 420.851 ms | 201.713 ms |
| 128/128 | 199.846 ms | 437.857 ms | 436.941 ms | 201.671 ms |
| 128/96 | 199.977 ms | 385.814 ms | 420.180 ms | 202.879 ms |

All three configurations had substantially slower resident and guest-echo
medians in run 2 (about 339–350 ms) than run 1 (about 166–171 ms). Gateway
medians stayed around 162 ms. This demonstrates variation in the measured
guest paths that gateway RTT alone does not explain; it does not identify
whether the wait was in forwarding, scheduling, buffering or another stage.
We retained that entire slow run and every outlier.

During run 2, several new Mac-access attempts timed out, including a check
that bypassed the SSH pooling relay but still used the device bridge. An
existing multiplexed connection subsequently returned progress successfully.
The independently launched test completed. These access observations are
recorded as a concurrent condition, not a proven cause of the slow samples.

## Uncertainty and interpretation

The four-run plan was fixed before inspecting these outcomes. Primary
analysis uses only these four runs, excluding prior loaded experiments and
the earlier order-confounded comparison. A paired hierarchical bootstrap
resamples four connections, then seven non-overlapping 18-round cycles within
each selected connection. Each draw retains all three variants together.
There are 10,000 resamples, using Go `math/rand` seed 2026111604. A sensitivity
analysis keeps each connection fixed and resamples its cycles (seed 2026111605).
These exploratory percentile intervals are conditional on just four observed
connections; they do not eliminate environmental effects or prove equivalence.

| P95 difference: 128/96 minus alternative | Observed | Primary 95% interval | Sensitivity 95% interval |
| --- | ---: | ---: | ---: |
| Versus 160/128 | -67.749 ms | -223.856 to +73.477 ms | -141.610 to +61.029 ms |
| Versus 128/128 | -52.043 ms | -112.807 to +46.536 ms | -109.591 to +39.474 ms |

128/96 had lower individual pings in 269/504 paired rounds against 160/128
and 259/504 against 128/128. Its first-run P95 advantage was only 2–5 ms,
and it lost to both alternatives in run 4. The pooled medians are essentially
equal. The evidence supports keeping 128/96 as a candidate, not selecting it
as a proven latency improvement or changing installed budgets on this basis.
This unloaded experiment cannot establish transfer throughput, scrolling
responsiveness or latency under saturation. The approximately 240 ms target
has not been achieved by this series.

[Raw reports, launch log, source checksums and analysis](2026-11-16-idle-four-run-series.json)
retain all four runs. The artifact checksum and provenance are also recorded
in [the benchmark catalog](metadata.json). Repository English/localization
and whitespace checks passed; diagnostic implementation was unchanged.
