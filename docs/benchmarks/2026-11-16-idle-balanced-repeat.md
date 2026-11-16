# Balanced unloaded repeat

The user requested another run to check whether the earlier 128/96 KiB/s
ranking was repeatable rather than environmental error. Client origin remains
**Mac, Singapore / Singtel** (user-reported); destination is **GCP
runta-integration / us-west1**, `ssh.runta.xyz`. Region evidence is the earlier
12:04 UTC inventory; runtime placement and VPN/proxy routing were not
re-resolved per sample. This is not a US-local measurement.

## Measurement correction before the repeat

Review found that the previous six-round resident permutation combined with
`(round + residentPosition + turn) % 3` gave each variant a **4/1/1**, rather
than **2/2/2**, distribution of probe positions. Thus the earlier variant
ranking was confounded by whether ping, guest echo or gateway keepalive came
first. A regression test failed against that sequence, then passed after the
fix. This identifies a design defect, not proof that it caused the previous
latency difference.

The corrected probe jointly balances resident position and probe position
over an **18-round cycle**, with an offset after each six-round permutation.
Every variant sees every resident/probe position combination twice per cycle.
The sample count is therefore **126 per variant** (seven complete cycles).
The input terminal order changed from A/B/C to C/A/B. This is a corrected-method
repeat, not an identical-method replication. Existing raw observations remain
preserved with the order limitation documented.

## Conditions and results

The run lasted **13:07:08–13:10:51 UTC, November 16, 2026**. It used one
authenticated SSH connection, three persistent residents, one guest echo
channel and nine excluded warm-up pings. No file transfer, scrolling or agent
input was generated. Existing live traffic remained uncontrolled. The Mac
sender stayed at 160/128 KiB/s; only the staged cloud variants differed.
All **378 sample triplets** completed successfully, without reconnecting.

| Cloud total/bulk budget | TUI median | TUI P95 | TUI maximum | Guest echo P95 | Gateway P95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 160/128 KiB/s | 168.591 ms | 355.337 ms | 523.952 ms | 309.281 ms | 194.487 ms |
| 128/128 KiB/s | 168.438 ms | 351.090 ms | 524.765 ms | 352.259 ms | 196.276 ms |
| 128/96 KiB/s | 168.800 ms | 375.580 ms | 896.113 ms | 363.618 ms | 193.773 ms |

The earlier 128/96 advantage did not reproduce. Its median is essentially
the same as the other variants and its measured P95 is higher in this repeat.
This does not establish that 128/96 is intrinsically worse.

## Paired uncertainty check

Before inspecting the repeat's results, analysis was specified as a paired
block bootstrap with 10,000 resamples: primary block size 18 successive rounds
(one complete design cycle, seven blocks), and sensitivity size six rounds
(21 blocks). Each draw retains all three variants together. Nearest-rank P95
matches the probe's estimator. These are exploratory percentile intervals
conditional on observed traffic, not proof of causality or environmental
invariance; seven primary blocks provide limited information about long bursts.

| P95 difference (128/96 minus alternative) | Observed difference | Primary 95% interval |
| --- | ---: | ---: |
| Versus 160/128 | +20.243 ms | -8.726 to +109.692 ms |
| Versus 128/128 | +24.490 ms | -15.582 to +103.340 ms |

Both intervals include zero. Six-round sensitivity intervals also include
zero (-5.227 to +119.779 ms and -11.486 to +109.201 ms respectively). In paired
rounds, 128/96 had a lower individual ping in 60/126 comparisons against
160/128 and 50/126 against 128/128. These observations do not justify selecting
128/96 as the consistently faster unloaded configuration. They also do not
explain where the slow requests waited or predict scrolling/transfer behavior.

The diagnostic race suite passed on Linux and Mac; ordering regression tests,
vet and English/localization checks passed. Production budgets and the
installed application were not changed by this test.

[Raw samples, uncertainty calculations and provenance](2026-11-16-idle-balanced-repeat.json)
include exact source/binary checksums, timestamps, order design, fixed client
budget, cloud variants and sampling boundaries. Reproduction uses the corrected
diagnostic and the recorded staged artifacts:

```sh
profile-ssh-latency -load=false -samples 126 -timeout 8m \
  -remote /tmp/fennec-qos-tcp-correlated-probe \
  -idle-terminals /tmp/fennec-q128-c128-terminal,/tmp/fennec-q128-a160-terminal,/tmp/fennec-q128-b128-terminal
```
