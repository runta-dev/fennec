# Installed-path performance acceptance, 2026-11-11

Status: **FAIL; goal remains active**. Command: `make test-e2e-performance`.
This target serializes real cold/warm PTY launches and live session operations,
including under `make -j`. Cold usable-screen budget is 5 seconds; warm usable
screen and operations retain the 500ms per-sample gate. Nonzero exit is required
for missing output, missing spinner, operation failure, or an exceeded budget.

Latest log: `/tmp/fennec-unified-performance.log`.

| Boundary | Samples (ms) |
| --- | --- |
| Cold invocation to usable Codex composer | 4071.361, 3802.566, 3751.388 |
| Warm invocation to usable Codex composer | 335.001, 369.642, 348.761 |
| Mature-session send, complete CLI round trip | 552.576, 586.291, 563.507 |
| Same sends, remote Herdr execution | 202.064, 202.896, 202.677 |

All six launch samples observed the text-free spinner and exited successfully.
Cold launches create a fresh SSH master at invocation, not a hidden warmed
connection. Launch results are usable-screen observations, not receipt times.

Send measurements now include remote identity-check and Herdr execution timings;
they do not include user input text in the diagnostics. The identity check takes
about 1–2ms. Herdr Core 0.1.18 uses a 200ms paste-to-submit delay, and during its
first ten seconds also waits for output to settle. Fresh and repeated sends are
still tested; the mature-terminal case is additional coverage, not a replacement
that hides first-send failures. Mature cases wait eleven seconds **after** fixture
creation returns, outside their operation measurement.

The full run also detected refresh 503ms, stop 647ms, and launch receipt 593ms.
Do not classify these as acceptable or remove them based on faster samples.
The next optimization must distinguish transport tail latency from Herdr's
intentional input protection. Do not bypass dialog/human-input protection,
shorten timeouts, or return a queued acknowledgement as completed input.

An earlier measurement was invalidated by a concurrent daemon replacement.
The live ops suite now records the initial daemon PID/build ID and fails if they
change during the run. It cleans up only its own labeled fixture sessions.

Signing check: canonical app authority is `Fennec Local Development`; the
credential helper SHA256 remains
`b0ee08622cd866bf1e1bfd7c5f7f76ba7eca51944364e329a6d47a33e3714d6e`,
identical to the pre-profile bundle. This hash check is not by itself graphical
proof of absence of a Keychain authorization dialog.

## Removing completed-operation close frames

Installed a change to `terminalstream` that releases the local mailbox after a
final operation result/error without sending an extra remote `close`. Timeout or
unknown outcome still sends cancellation. This removes a redundant packet and
avoids making an already confirmed operation wait on an unnecessary stream write.
`go test -race ./internal/terminalstream` passes, including success, error and
timeout wire-behavior coverage. The existing development certificate and helper
hash were preserved during the staged bundle replacement.

Post-change full log: `/tmp/fennec-no-redundant-close-e2e.log` (still **FAIL**).
Cold usable screens: 4865.561 / 4028.500 / 3832.947ms. Warm usable screens:
358.598 / 318.855 / 330.870ms. All observed the spinner. Mature send:
477.079 / 488.040 / 559.952ms; remote send remained approximately 203ms.
This is not a controlled proof that all observed latency differences came from
the patch: network tail latency remains. Fresh/repeated send still failed;
stop had 502/565ms samples and launch receipt was 524ms. No gate was relaxed.

The shared machine was not CPU/memory saturated during diagnosis: load average
0.33, roughly 3GB memory available. Existing user agent processes were preserved.

## Stop phase breakdown

The daemon previously discarded `LocalRuntime.Stop` timing fields. They now reach
the internal control response and live test log, without adding another remote
request. Installed-path focused results (`/tmp/fennec-stop-detailed-e2e.log`):

| Stop | CLI wall ms | Remote lookup ms | Kill ms | Exit confirmation ms |
| --- | --- | --- | --- | --- |
| 1 | 287.011 | 1.632 | 4.499 | 0.986 |
| 2 | 310.448 | 1.290 | 8.086 | 0.818 |
| 3 | 280.600 | 1.612 | 4.726 | 0.967 |

Focused stop acceptance passed; runtime ownership and stale-refresh regression
tests passed with `-race`. This does not supersede the prior full-suite failures.
The stop handler updates its cache immediately after confirmation and does not
wait for a new list operation. These samples place remote stop work at 7–10ms;
the rest includes transport and client/daemon overhead, not yet individually
attributed. Keep the original long-tail failures as evidence.

## Final terminal frames

Extended completed-stream cleanup to terminal `exit` as well as operation
`result`/`error`: `Session.Next` drains queued output first, returns the original
exit code, and then releases local resources without a redundant close packet.
Active detach and timeout cancellation still close remotely. Race regressions
cover final output, nonzero exit status, mailbox release and cancellation.

Installed-path validation after the change:
- New usable Codex screens: 334.051 / 332.599 / 397.296ms, exit code 0 and spinner
  observed in all samples (`/tmp/fennec-final-frame-warm.jsonl`).
- Reattach to the same session after detach: 340.894 / 337.517 / 446.991ms, spinner
  observed; profiler completed cleanup (`/tmp/fennec-final-frame-resume.jsonl`).

The credential helper hash remained unchanged across the staged replacement.
These focused checks do not erase earlier send/full-suite failures.

## Cold measurement boundary audit

`cmd/profile-launch --mode cold` sets `FENNEC_COLD_START=1`: the CLI opens a fresh
per-invocation SSH master, while the broker independently checks runtime ownership
and supplies the authorized launch. This genuinely measures a fresh connection
to usable TUI, but it is **not** a full daemon-restart/normal-first-channel test.
The normal path uses `SharedConnection` and `terminalChannel.get`; it queues its
first request without waiting for remote readiness. The dedicated cold path uses
`serve-initial`. Do not claim these distinct lifecycle paths are interchangeable
or that ordinary daemon startup has been fully accepted from this test alone.

The startup profiler now rejects missing daemon identity or a PID/build-ID change
between samples. A fresh run with this guard produced cold usable screens of
4370.912 / 3888.557 / 3948.170ms with spinner visible and successful cleanup
(`/tmp/fennec-cold-identity-verified.jsonl`). The remaining normal first-channel
lifecycle acceptance must preserve existing user attachments and sessions.

## Normal daemon-cold acceptance: failing at 11 seconds

Added explicit `--mode daemon-cold` to the Go startup profiler. It stops the
local daemon, verifies its socket is unavailable, then times ordinary `run-agent`
with `FENNEC_COLD_START=0`; this includes auto-start, account restoration, runtime
preparation and the normal shared-channel path. UI must be closed to avoid its
poller starting the daemon before timing begins. The 5-second gate remains.

This exposed a stop/start race: explicit `fen daemon stop` returned after
launchctl bootout but before the registration disappeared. A following start
could kickstart the retiring registration and wait forever after it vanished.
Stop now uses the existing `waitServiceRemoval` barrier; focused lifecycle tests
passed and the signed app was replaced safely.

Afterward a normal cold invocation reached usable Codex at **11038.935ms** with
spinner, exit 0 and owned-session cleanup, correctly failing the 5s gate.
Log: `/tmp/fennec-normal-daemon-cold-diagnostic.jsonl`. Broker request alone took
3452ms. An earlier attempt exited before first screen and remains unresolved;
failed profiler terminal output is now stored privately for diagnosis.

Source audit identifies two separate connections on this normal path:
`prepare_runtime.go` calls `client.Connect` for setup/checkpoint health, then
`serveLaunchBroker` constructs a new `SharedConnection`. Consolidating these is
the next critical task. The previous 3.8–4.9s dedicated cold-SSH numbers do not
prove the full normal daemon-cold objective. UI was reopened after the test;
existing user sessions were not stopped.

## Preparation connection handoff: transport support

Added `terminalstream.StartSSH` to create the existing resident protocol over an
already authenticated Go SSH client. The native-process constructor and SSH
constructor share the same mailbox, heartbeat, flow control and completion code.
Transport shutdown closes only its SSH channel, not the shared connection.
A real loopback SSH regression opens the protocol channel, closes it, then runs
a command successfully on a second channel of the same client; it passes with
`-race`. Existing transport regressions also pass.

This is source-only infrastructure for the handoff. It is not yet wired from
runtime preparation into broker lifetime and has not been deployed as a startup
optimization. Do not claim the second handshake has been removed until that
integration and the ordinary daemon-cold measurement pass.

## Preparation handoff installed

Wired the authenticated preparation SSH client into the broker configuration,
bound to its runtime ID and account epoch. The daemon owns and closes it on
account lifecycle cancellation; standalone preparation retains its prior close
behavior. The broker uses `StartSSHContext` on that client instead of proactively
starting another OpenSSH master. Failed borrowed connections are closed before
the existing single-flight, ownership-checked fallback reconnect. Setup retries
while a broker is already running still use the existing setup path; connector
reverse-link connection consolidation also remains outstanding.

Focused race tests passed, the signed staged bundle was installed, and helper
hash equality was verified. Live channel status showed generation 1, ready true,
and fallback master start time 0. Normal `daemon-cold` usable screen improved from
11038.935ms to **8536.598ms**, with exit 0, spinner and owned-session cleanup.
The hard gate correctly failed (`/tmp/fennec-handoff-daemon-cold.jsonl`).

Daemon phase record for this sample: local route reads 716ms (parallel), SSH
provisioning 1309ms, TCP 397ms, SSH handshake 2971ms, checkpoint health 1056ms,
total preparation 6799ms. The CLI broker request was 505ms rather than 3452ms.
Remaining work includes overlapping safe cached-identity handshake with API
validation and reducing serial checkpoint/terminal-channel round trips, while
retaining fail-closed ownership and host-key checks. UI was reopened afterward.

## Inline checkpoint and agent-readiness race

Checkpoint health now uses `ExecPublicCheck`, placing its non-secret script in
the SSH exec request instead of waiting to send it over stdin. Existing `Exec`
still sends credential-bearing scripts only via stdin. Real loopback SSH tests
verify quoting/multiline behavior, preserved remote exit 17 and diagnostics, and
that ordinary private scripts retain `sh -s`. Preparation checkpoint regressions
also pass.

A daemon-cold failure captured in private terminal diagnostics showed the CLI
sent its first launch while the broker was listening but Codex route restoration
was incomplete. New launches now wait for the requested agent in `services.active`;
existing-session resume keeps its route-independent readiness behavior. A final
ready state without the requested agent returns an error rather than waiting
indefinitely. Regression/race tests passed and the signed bundle was installed.

After waiting for installation preparation to finish, the normal daemon-cold
sample reached usable screen in **8036.436ms**, exit 0, spinner observed and owned
session cleaned up. Checkpoint health measured 920.849ms (previous sample
1055.839ms). These single-run differences are not a controlled attribution of all
speedup to the patch. The 5s gate still fails. Evidence:
`/tmp/fennec-inline-health-agent-ready.jsonl`. UI reopened afterward.

## Overlap SSH handshake with configuration API

`Client.Connect` now speculates only an SSH authentication handshake using an
exact, previously pinned host entry and existing device key. It exposes no SSH
session until the API succeeds and address, runtime username, gateway key and
client public key all match. Rejection closes the speculative connection;
changed identities close it before connecting with freshly verified config.
Unrecognized/missing cache uses the original API-first path. Cancellation closes
an in-flight socket. Race tests cover overlap without early release, rejection,
each identity change and exact-host cache validation. Signed bundle installed.

The first normal cold sample was **27218.788ms**, correctly failing: preparation
cache missed and full reconciliation ran. SSH provisioning plus connection took
3319.636ms, demonstrating overlap, but this does not excuse total startup time.
After that completed synchronization, a new ordinary daemon-cold sample with a
cache hit took **6525.565ms**, exit 0, spinner and owned-session cleanup confirmed.
Both remain failures against 5s. Logs:
`/tmp/fennec-parallel-ssh-daemon-cold.jsonl` and
`/tmp/fennec-parallel-ssh-daemon-cold-cache-hit.jsonl`.

Next critical work: overlap the safe no-command handshake with runtime resolution
as well, and avoid repeating unrelated installation work on credential-only
cache invalidation. Neither cache misses nor previous long tails may be discarded
from acceptance. UI was reopened after profiling.

## Overlap runtime resolution as well

Preparation now calls `ConnectResolved`: the existing runtime preference is only
a speculative handshake hint. `EnsureShared` still checks authenticated ownership,
shared-purpose selection and uniqueness, starts/resumes if needed, and waits for
readiness. Fresh SSH API validation then runs before the connection can escape.
A different resolved runtime discards the hinted connection. Related race and
preparation tests passed; signed bundle installed.

The cache-hit ordinary daemon-cold sample was **6258.376ms**, exit 0, spinner and
owned-session cleanup confirmed (`/tmp/fennec-resolve-overlap-daemon-cold.jsonl`).
Measured resolve+connect 3566.525ms, handshake 3329.315ms, checkpoint health
971.082ms, preparation 4583.358ms. This still fails 5s, and the small change versus
the prior sample is not a controlled attribution because network timing varied.

`make test-e2e-startup` now begins with three ordinary daemon-cold samples, before
the independent cold-SSH and warm samples. The profiler requires an absolute
binary path and rejects daemon-cold measurement while its native UI is running,
preventing the UI poller from pre-starting the daemon. UI reopened after testing.

## Checked resident channel and one verified owner lookup

For checkpoint hits, preparation now starts one SSH channel whose shell first
executes the unchanged health checks and only then execs the resident protocol.
The ready transport is retained by the account-scoped daemon and adopted by the
broker, so no second session-channel establishment follows the check. Failed
checks never start the protocol. Standalone preparation retains its finite check.
Related shell-gating, SSH lifetime and race tests passed. Installed first result:
5493.576ms, still over budget (`/tmp/fennec-checked-channel-daemon-cold.jsonl`).

Then removed the separate serial `CurrentUser` in service initialization: the
owner comes from `EnsureShared`, which already freshly validates identity and
runtime ownership. Initialization rejects empty owners or owner changes within
an account epoch; pre-validation progress cannot initialize services. Full
`go test -race ./internal/cli ./internal/runta ./internal/terminalstream` passed.
The signed bundle was installed with the unchanged credential helper hash.

Three consecutive ordinary daemon-cold samples:
**4435.165 / 4714.965 / 88795.549ms**. All reached usable TUI, observed spinner,
exited 0 and cleaned up their owned sessions, but the third makes acceptance
**FAIL** (`/tmp/fennec-single-identity-daemon-cold.jsonl`). Do not drop it.
The third was a cache hit: preparation itself took 4200.513ms, so roughly 84s
preceded preparation. The cause is not yet proven. Added opt-in phase timers for
daemon start-lock acquisition, launchctl, control readiness, broker readiness and
credential restoration to distinguish these cases. These extra timers are still
source-only; the latest installed bundle contains the channel/owner changes.
UI reopened after the failed run.

## Startup diagnostics installed; cache-hit batch passes

Installed the phase timers. A complete three-sample ordinary daemon-cold batch
passed at **4518.360 / 4316.166 / 4508.417ms**
(`/tmp/fennec-startup-phases-daemon-cold.jsonl`). Start-lock wait was approximately
0ms; launchctl plus control readiness was approximately 60ms. This batch does not
erase the earlier 88.8s failure.

Historical macOS logs confirm the old CLI process existed at 03:24:05.929 and its
daemon was not spawned until 03:25:30.035. At 03:24:57.457 the kernel reported that
the profiler woke the CPU about 714 times/second. Causation is **not established**.
The profiler unnecessarily rescanned all accumulated output even after finding
the composer. It now stops that scan after detection, bounds the detection tail
to 64KiB and the diagnostic tail to about 1MiB, and remembers spinner observation
independently. Added spawn time, first-output time, total output bytes and profiler
PID to future records to localize pre-daemon delays. Profiler unit tests pass.

The subsequent bounded-profiler run measured **27025.049ms** with a preparation
cache miss (`/tmp/fennec-bounded-profiler-daemon-cold.jsonl`); it correctly failed
and cleaned up. Local `magpie/logins.json` had changed since the successful batch.
This is evidence to investigate credential-driven invalidation, not permission
to exclude the failure. Splitting credential reconciliation from unrelated
installation work remains necessary. UI reopened afterward.

## Installation/configuration separation and startup-lock root cause

Added independent installation fingerprints/checkpoints, bound to account token,
runtime, desired agents, packaged artifacts and daemon binary. Credentials remain
part of the configuration fingerprint only. An installation hit must also pass
the remote installation marker and software health checks; only then are unrelated
Node/Herdr/terminal/Magpie installation steps skipped. Credential migration,
plugins, account restoration and route reconciliation/readback still run. Race
tests verify credential rotation does not skip configuration, and account/runtime/
artifact changes cannot reuse installation state. Installed signed bundle.

`--invalidate-configuration` backs up and changes only derived cache metadata,
not user credentials. It retains installation state to exercise this edge case.
Confirmed `installation_hit_configuration_changed`, usable screen **13188.537ms**,
exit 0 and spinner (`/tmp/fennec-configuration-only-cold.jsonl`). This remains FAIL.
The edge case is now available as `make test-e2e-configuration` and included in
the aggregate performance target. Its 5-second gate was not relaxed.

A later cold batch exposed **92192.434ms**, including **87732.614ms waiting for
control-start.lock** (`/tmp/fennec-split-cache-hit-cold.jsonl`). Private timing
records identified another active CLI, PID 49849, acquiring the lock while its
session reconnected during daemon stop. It had kicked a retiring launchd job and
then held the lock waiting for readiness after that job disappeared. This gives
a concrete cause for the long startup-lock stall; the earlier profiler CPU warning
was not proof of its cause.

Fixed both sides: start re-registers a job that disappears while waiting; explicit
stop holds the same startup lock through bootout and registration removal, so
even older reconnecting clients cannot kick the retiring job. Deterministic race
tests cover disappearance, registration failure and stop/reconnect exclusion.
Full `go test -race ./internal/cli` passed. Signed bundle installed; credential
helper hash unchanged.

An initial post-fix configuration-change sample still took **12776.523ms** but
lock wait was 0.021ms (`/tmp/fennec-start-stop-serialized-cold.jsonl`). After that
sync completed, three ordinary cold samples passed at
**4430.464 / 4620.174 / 4391.451ms**, with lock waits 0.026 / 0.031 / 0.034ms
(`/tmp/fennec-serialized-stable-cold.jsonl`). The profiler additionally proves
SSH startup began 106 / 99 / 104ms *after* command invocation, rejecting hidden
background pre-starts. UI reopened afterward.

Normal cached cold startup is now repeatedly under five seconds, but the
configuration-change path and previous warm-op failures remain outstanding.

## Go configuration helpers

Replaced the embedded Python plugin reconciliation and Codex identity restoration
with Go implementations in the remote terminal helper. Plugin checks require
both matching configured spec and actual package metadata, skip verified installs,
reject unsupported/duplicate plugins and verify installation output. Empty plugin
sets avoid an SSH request entirely. Identity restoration preserves existing auth,
selects only a single enabled/non-hidden/non-local-home migrated account, and
verifies that Magpie created the guest identity. CLI output is not forwarded.

Focused setup race tests cover installation reuse, missing artifacts, allowlists,
existing identities, ambiguous/hidden/disabled accounts and missing restore
output. CLI and terminal-helper race suites passed. Packaged/signed helper and
daemon were deployed together; real preparation reached ready.

Forced configuration reconciliation still measured **13084.436ms**, exit 0 and
spinner observed, with `installation_hit_configuration_changed`
(`/tmp/fennec-go-config-reconcile-cold.jsonl`). This is not a material demonstrated
speedup over 13.19s. These local Go operations enable the next change: execute
configuration reconciliation remotely as one request while preserving per-agent
results/readback, rather than separate WAN requests for every step. UI reopened.

## Account synchronization in one remote request

Credential migration, plugin reconciliation, identity restoration and optional
catalog read now execute in a single remote Go request. Credential bytes remain
in SSH stdin; argv contains only the command name. The versioned envelope rejects
unknown fields, invalid plugins and trailing data before mutations. Existing
credential merge rules and local install/identity verification are reused.
Catalog failures remain non-blocking for readiness; no inference probe is added.
Agent configuration and readback are still separate and remain required.

Race tests verify operation order, no secrets in the reply, validation before
credential writes, and exactly one real loopback SSH exec with private stdin.
CLI and terminal-helper suites passed. Signed client/remote artifacts were
deployed together and preparation reached ready.

Forced configuration-change cold startup: **10194.165ms**, exit 0, spinner and
owned-session cleanup confirmed (`/tmp/fennec-account-batch-config-cold.jsonl`).
Confirmed installation cache hit/configuration change. The account request took
1501.635ms locally; remote durations were credentials 2.527ms, plugins 0.112ms,
identity 0.002ms and catalog 5.418ms. Remote durations are observed at receipt,
not clock-aligned cross-host spans. This remains FAIL against 5s. Next bottlenecks
are new exec-channel overhead and agent install/config/readback round trips.
UI reopened afterward.

## Magpie subprocess diagnosis

Read-only guest profiling of the documented `magpie codex model` command showed
that the CLI starts a `magpie-bun` child; no `--version` child was observed.
Three isolated reads took 106.093 / 110.773 / 105.525ms. Concurrent reads for
Claude, Codex and Pi took 287.119 / 298.404 / 327.981ms, with reported process CPU
times 128.010 / 138.803 / 125.682ms. This explains much of the 303–332ms remote
route work under concurrent preparation; it is not solely WAN latency.

Diagnostic source: `/tmp/fennec-profile-magpie.go`, cross-compiled and executed
on the owned runtime. It discards model command output and reports timing and
process names, not configuration or credentials. `magpie ls` is documented but
its displayed settings are not yet proven an authoritative replacement for the
per-field readback contract, so no production validation was weakened. This turn
does not claim a startup speedup or a passing full performance gate.

## Optional catalog moved after verified agent readiness

Added capability-negotiated catalog deferral. Account synchronization can now
return credentials/plugins/identity/inventory confirmation without querying or
returning the model catalog. After route results have been verified and published,
the catalog is fetched over the resident protocol and recorded for the UI/cache.
Legacy/large-stdin paths retain the previous behavior. A trace-based regression
checks that deferred account processing makes no catalog HTTP connection.
CLI, terminalstream and helper race suites passed; signed artifacts deployed.

The actual configuration-cold sample was **11106.295ms** and failed the gate
(`/tmp/fennec-deferred-catalog-config-cold.jsonl`). SSH resolve/connect took
4639.794ms; account RPC 3065.699ms versus remote work 4.456ms; route RPCs about
1227ms versus remote work 191–290ms. The later catalog RPC took 532ms versus
7.742ms remotely. No speedup is claimed from this sample: transport latency
dominated despite removing an optional dependency from pre-route readiness.
UI reopened after the run.

## SSH gateway transport candidate

After user reauthentication, `forge sync flush spark` succeeded. Remote Forge
build completed in 50.17s; package tests passed **17/17, none skipped**. Logs:
`/tmp/fennec-spark-gateway-build.log`, `/tmp/fennec-spark-gateway-test.log`.
The existing stack still reports gateway PID 13050, so these build/test results
do not prove a running-service replacement or latency improvement.

Live production provenance is now verified: project `runta-prod`, GKE cluster
`runta-production-e32de500`, namespace `runta-system`, service
`runta-ssh-gateway` external IP `104.196.44.188` matches the Fennec SSH endpoint.
Deployment has three ready replicas, source annotation
`ea6c2104f50aefe25ad83e188008e8c67fef62ac`, image digest
`sha256:102a63ae86b119d7f505b58e6a8dd6c32251942499fab77142df7da327cb3dce`.
That source also uses generic `run_stream` without setting TCP_NODELAY on the
accepted socket. No production deployment was changed.

A bounded 15-minute gateway-log sample contained 95 connection failures:
71 early EOF, 17 disconnected, 3 invalid version strings, 2 resets and 2 keepalive
timeouts. These aggregate public-endpoint failures are not attributed to Fennec
and are not evidence of the measured authentication delay. Production read-only
inspection used a separate `/tmp/fennec-production-kubeconfig`.

Handshake follow-up: two direct pinned SSH diagnostics (no agent launch) measured
2880.705ms and 3021.800ms from SSH identification exchange through authenticated
connection establishment, excluding TCP dial. The last client write to final
authentication response took approximately 1138.8ms and 1349.0ms, respectively.
Other request/response gaps were approximately 230–290ms. This localizes a
repeatable long tail to the authentication completion path, but does not split
network transit, gateway authorization, worker tunnel setup and upstream SSH.
Only byte counts and monotonic timings were recorded, never packet contents:
`/tmp/fennec-handshake-wire.jsonl`, `/tmp/fennec-handshake-wire-2.jsonl`.

Added child tracing spans `ssh.authorize_key`, `ssh.prepare_session`, and
`ssh.open_worker_tunnel` in the active spark gateway route. They preserve checks
and carry no keys/tokens. Diff checks pass; these additional spans have not been
built or deployed. A fresh IAP connection still fails with GCP reauthentication
required, so Forge remote validation remains blocked. Goal remains active.

Follow-up: CI run 42927043624 completed successfully. The same narrow socket
change is now present in the verified active Forge slot `spark`; its pre-existing
`.cargo/config.runta-forge.toml` change was preserved. `git diff --check` passed.
Remote synchronization failed because Mutagen's IAP SSH transport requires GCP
reauthentication. The running spark gateway has NOT received this patch. No
local Cargo build was used for this follow-up.

Endpoint provenance: spark's live access status serves `api.forge` on
35.240.209.92, while Fennec uses `ssh.runta.com`. The active source's
`deploy/config/environments/production/api.toml` names `ssh.runta.com`; this
identifies the configured production environment, not its currently deployed
commit. Updating spark alone must not be reported as a production speedup.

A size-only credential diagnostic measured two stores at 21638 JSON/base64
bytes, versus 14588 bytes with gzip/base64 (299us encoding). Same-connection
echo samples were 560.652/498.192ms for small/large payloads, then
619.369/345.935ms for large/small payloads. Baseline 32-byte echoes were
341.629/380.477ms. These noisy results do not establish a compression win;
compression was not added. No credential contents were logged. Evidence:
`/tmp/fennec-ssh-size-ab.jsonl` and `/tmp/fennec-payload-size.go`.

A diagnostic using one persistent authenticated SSH connection and `/bin/cat`
measured 32-byte echoes at 273.976 / 275.653ms and 32KiB echoes at
1018.085 / 984.805ms; local writes were below 0.1ms. Output contains only byte
counts/timing, not credentials (`/tmp/fennec-ssh-echo-profile.jsonl`). This supports
investigating size-dependent transport delay, not a claim that Nagle is the only
cause.

Source audit: the gateway accepts its own TcpStream and invokes generic russh
`run_stream`. russh 0.62.3 defaults `nodelay` to false and its listener-level socket
option path is bypassed. Prepared a narrow fix that enables TCP_NODELAY after
connection admission, with a metric and warning on per-connection setup failure.
It leaves authentication, ownership checks and host keys unchanged.

Isolated worktree: `/Users/shiqimei/repos/runta-dev/runta-ssh-latency`.
Commit: `f264cf84e8d9f47e3674fd72697afa61f4a08850`.
Draft PR: https://github.com/runta-dev/runta/pull/1815
`cargo check -p runta-ssh-gateway`, its 5 tests, and diff checks passed.
Full required local verification was attempted; it encountered platform/tooling
issues on macOS (including Linux-only netlink-sys in workspace lint and a flaky
build-profile shell test). CI run 42927043624 is the pending Linux validation.
No gateway production deployment or measured production benefit yet.

## Route write/readback and binary inventory

Route configuration and verification now run inside one remote Go invocation,
with typed results checked against requested agent/model identities. A matching
initial readback skips the no-op write and duplicate read. Changed routes still
require successful write and matching readback. Read-only checks use a shared
lock that excludes the existing exclusive configuration writer. Known local
blockers (for example no selected model) do not open an SSH channel.

The account synchronization reply now includes executable availability. Managed
binaries must have a working alias; missing/stale aliases retain the repair path.
Other executable installations can be reused without running version probes.
Confirmed inventory entries skip the old separate install-if-missing request;
missing entries still use it. Tests cover reuse without executing binaries,
missing files/aliases, readback mismatch, failed writes, shared-lock exclusion,
and no inference probes. Relevant race suites passed; signed artifacts installed.

Forced configuration-cold samples were **7110.652ms** and, after removing an
unnecessary remote call for unconfigured OpenCode, **7449.304ms**. Both are FAIL,
not a claim that every subsequent change reduces every network sample. Logs:
`/tmp/fennec-route-batch-config-cold.jsonl` and
`/tmp/fennec-route-final-config-cold.jsonl`. All four binaries were reused;
OpenCode's local blocked result took 0.001ms. Other route requests still took
841–1322ms, motivating reuse of the resident protocol rather than opening exec
channels for setup operations. UI reopened after profiling.

## Resident configuration operations

Added advertised `reconcile_routes` and `model_accounts` capabilities to the
resident protocol. Installation health checks now retain the protocol channel
for configuration changes as well as ordinary cache hits. Routes use that channel;
account envelopes up to a conservative 200KiB bound do too. Larger stores select
authenticated stdin before sending any mutation, preserving the existing frame
limit. Missing capabilities select compatibility paths before mutation; an
uncertain resident result is never replayed on another path. Already-cancelled
operations send nothing. Setup/account validation and model readback remain.

Race tests cover repeated route operations on one transport, peer survival after
an invalid credential request, absent capabilities, no cross-path replay and
large-store selection without truncation. CLI, terminalstream and terminal-helper
suites passed. Signed artifacts deployed together.

Configuration-cold samples remain over budget: **5931.195ms** and **6652.363ms**
(`/tmp/fennec-resident-configuration-cold.jsonl`,
`/tmp/fennec-resident-config-timings.jsonl`). The latter measured account RPC
876.458ms versus remote execution 13.254ms (inventory only 0.051ms). Three route
RPCs took about 698ms, with remote work 303–332ms. These distinguish actual remote
CLI work from remaining transport latency; neither is excused from the 5s gate.
Remote duration fields are observations at receipt, not synchronized spans.
UI reopened afterward.
