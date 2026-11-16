# Selected 128/96 KiB/s deployment

The user selected 128/96 KiB/s total/bulk on November 16, 2026 and requested
documentation, a push to main and installation on the Mac. This is a development
installation, not a public release publication.

The default counts 131,072 total bytes/s and 98,304 bulk bytes/s with existing
wire-overhead accounting, a 2 KiB burst and interactive priority within the
total budget. Both Mac and cloud senders use this default. The cloud broker
directory advances to `traffic-v4` so new connections cannot inherit the
previous rate from an existing broker. Old brokers drain without killing
user agent sessions. Adaptive terminal windows remain 8–64 KiB and now derive
from 128 KiB/s; socket credit remains 16 KiB and TCP feedback remains off.
SSH relay recovery fixes are retained.

The selection follows the
[four additional unloaded comparisons](2026-11-16-idle-four-run-series.md):
Singapore/Singtel (user-reported) to GCP runta-integration/us-west1,
2026-11-16 13:22:43–13:40:06 UTC, 504 samples per cloud variant. The raw report
records exact binaries, source checksums, endpoint boundaries and placement
evidence. Pooled resident ping P95 was 454/438/386 ms for 160/128, 128/128 and
128/96. The last configuration won three of four runs, but uncertainty
intervals include zero and the fourth run reversed the ranking. No stable
causal advantage or approximately 240 ms P95 achievement is claimed.

Those experiments held the Mac sender at 160/128. They do not establish the
performance of the new two-ended default or its scrolling/transfer behavior.
Linux race tests passed for `internal/traffic`, `internal/trafficbroker`, and
the terminalstream window/socket/packet/priority/ping/SSH selection. Go vet,
`make check-ui`, English/localization and whitespace checks passed. An initial
full terminalstream race run was manually interrupted after 205 seconds while
executing `TestConflatorRecordedSessions`; it is not recorded as a pass. The
scoped transport tests subsequently completed in 5.6 seconds. No conflator
implementation was changed for this budget selection.

## Installed build and acceptance

Development build **0.3.11162158-dev**, source
`e56fc2172570959b508eabb0e589922e2d486136`, is installed at
`/Applications/Fennec.app`. Main was pushed before building. A detached local
build snapshot kept the UI and Go sources fixed while unrelated website work
continued on main. A cache symlink initially marked the snapshot untracked;
after excluding that build cache, the package was rebuilt and reports
`vcs.modified=false`. Both signing runs completed without an additional
authorization exchange, using the existing `Fennec Local Development` signer.
Complete signature, package permission/component checks, control contract and
bundled Chromium smoke checks passed. Mac race tests passed for the scheduler,
broker and focused terminal/socket-window tests.

The supported installer verified staging, stopped the old UI, replaced the
complete bundle and restarted it. Installed `fen start` activated the matching
backend. The installation/acceptance wrapper ran from **13:59:13.980 to
14:00:36.077 UTC on November 16, 2026**; this interval encloses the live
operations suite, whose log does not timestamp each individual sample.

The installed daemon was authorized, the connector connected and the launch
broker ready. Claude, Codex, Kimi, OpenCode, Grok and Pi routes were configured;
Cursor's pre-existing missing-credential state remained unchanged. All four
sessions live immediately before installation were still live afterwards.
The normal cloud `run-agent codex --version` path returned `codex-cli 0.169.0`
and its task-owned session exited normally. No user agent session was stopped.

Verified SHA-256 values:

- Installed Mac control binary and running daemon build ID:
  `5a081d7d28a5186daeb708813070f0e4b936ec7d0e2ce87a3783217b642392ae`.
- Bundled Linux terminal, installed cloud terminal, running resident and new
  `traffic-v4` broker:
  `51a2634ea46c61418bf45aba55f0bca83fe9df75afbb015eac287cd7272b0770`.
- Unchanged credential helper:
  `f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`.

The installed-path `ope2e` suite took 60.635 seconds and **failed the unchanged
500 ms per-sample gate**. Its 69 recorded operation samples include separately
labeled cold SSH, local/cache calls and remote operations. The table below
shows the remote-operation groups, not resident ping or rendered typing RTT:

| Operation | Samples | Observed range | Above limit |
| --- | ---: | ---: | ---: |
| Cold SSH (5,000 ms limit) | 1 | 4,754.710 ms | 0 |
| Remote refresh | 3 | 237.670–613.541 ms | 1 |
| Send | 9 | 426.928–1,020.171 ms | 7 |
| Read | 9 | 205.712–521.087 ms | 1 |
| Model change / restore | 2 | 202.046–310.297 ms | 0 |
| Attach, first usable TUI | 3 | 266.632–277.658 ms | 0 |
| Resume, first usable TUI | 3 | 309.954–321.185 ms | 0 |
| Stop confirmation | 3 | 249.972–258.598 ms | 0 |
| Launch, first usable TUI | 3 | 347.946–429.733 ms | 0 |

The live test retained the same daemon identity throughout. Cold connection
time is separate from warm operations; subtest durations include setup and
waits and are not substitutes for the logged per-operation samples. Existing
user traffic and host load were uncontrolled; brief status reads may overlap.
The separately opt-in image-paste test was skipped, not passed. No manual
scrolling/typing acceptance or new post-installation ping comparison was run.
The budget was installed as requested, but the broader latency target remains
incomplete and this record does not claim a measured improvement.

[Raw acceptance log, counts and provenance](2026-11-16-budget128-96-deployment.json)
are retained in the benchmark catalog.
