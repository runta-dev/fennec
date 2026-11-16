# Pre-QoS resident retest

The user requested a retest of the original version, after later loaded
measurements had been compared with the earlier no-generated-load baseline.
The historical no-generated-load observations were TUI P95 342.038 and
395.173 ms, with gateway P95 242.152 and 241.873 ms respectively. The later
333.319 ms result belongs to the paced 16 KiB loaded comparison, not that
original baseline.

At 11:59:20–12:00:34 UTC on November 16, two consecutive runs measured the
pre-application-pacing resident from commit
`184d080848c0dd7b80e4bae80021e36710da0b56`. Its source was exported directly
from Git and built with Go 1.26.11. The staged Linux binary SHA-256 was
`4f9315775e43f96ff6f584790d37969c6d3afce0a5472cecdacd9e4a5fb559a1`.

The existing Mac `profile-ssh-latency` diagnostic used `-load=false -samples 60`
and the staged original resident. Each run opened one authenticated SSH
connection and kept separate resident and equal-size guest echo channels warm.
It rotated exact matched protocol ping, guest echo, and gateway keepalive.
No bulk load or agent input was generated. Both reports confirm
`traffic_packets=false`; all 120 samples per path completed successfully.
The installed Mac app was not replaced: this tests the original resident
protocol round trip, not the complete original UI or typing/rendering path.
Existing live traffic was uncontrolled. Unlike the earlier fixed-order probe,
this diagnostic rotates the three measurement paths.

| Run | Samples per path | TUI median | TUI P95 | TUI maximum | Guest echo P95 | Gateway P95 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 60 | 162.670 ms | 298.468 ms | 371.305 ms | 246.346 ms | 204.767 ms |
| 2 | 60 | 165.079 ms | 351.094 ms | 376.437 ms | 317.666 ms | 162.124 ms |

Raw samples: [run 1](2026-11-16-original-retest-1.json) and
[run 2](2026-11-16-original-retest-2.json).

The original resident currently reproduces roughly 300–350 ms P95 without
generated load. This does not establish a before/after improvement or regression:
the gateway distribution changed since the historical measurement, and neither
current run compared old and new implementations under identical bulk load.
Loaded results of 480–600 ms must not be presented as improvements over the
earlier 342–395 ms no-generated-load baseline. Independent P95 values must not
be subtracted to infer a component's delay.
