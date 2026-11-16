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

Deployment validation will be appended after the signed build is installed.
