# Benchmark provenance

Every reported performance number needs its measurement origin and boundary.
Record client location, network/provider, destination environment and cloud
region/zone, UTC time, versions, workload, sample count, and whether the path
ends at the SSH gateway, guest, resident protocol, or rendered UI. Record the
source of each attribution: user report, live inventory, captured configuration,
or inference. Missing fields must say unknown; a filename date is not an exact
measurement timestamp. Do not publish credentials or private client addresses.

The November 16 original-version retest originates on a Mac on **Singapore /
Singtel**, as reported by the user, and targets **GCP runta-integration /
us-west1**. GCP inventory confirmed that region after the test. These numbers
do not measure US-local connectivity. `runta-test` / beta is a separate
environment in `us-east1`. Network geography is not inferred from latency.

`metadata.json` indexes the retained raw artifacts with checksums, available
original metadata, and explicit gaps. The two original-retest reports also
embed their provenance. Historical artifacts retain their raw bytes; missing
historical location, carrier, version, and timestamp details are not filled
with current settings. A cloud-local test or local paint replay must never
inherit the Mac-to-US network label merely because it shares a directory.

Comparisons require matching workload and measurement boundaries. Gateway RTT
terminates before the guest; it is not the same endpoint as a resident pong.
Independent P95 values cannot be subtracted to identify component delay. Keep
failed and interrupted runs with their errors, and distinguish observational
retests from controlled old/new comparisons.
