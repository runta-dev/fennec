# Recovery performance

Goal: minimize downloaded-update installation, daemon cloud startup, and TUI
reattachment latency without weakening identity verification, signature checks,
credential isolation, or session continuity. Measure until the original TUI can
accept input and authoritative cloud readback confirms it, not just a receipt.

## Baseline

Installed version: `0.3.11150238-dev`, build
`9a5a2e4dfa5027596564a661d8dd9041d74c7cb91617027ec94652d22eaee13e`.
The real installed-shim restart verifier retained session `expert-stone` at
`t3qp`; daemon PID 13762 changed to 21195 while CLI PID 21018 survived.
Recovery took 21,725 ms. The preceding acceptance took 24,542 ms.

Opt-in stage timings during the measured restart:

| Stage | Milliseconds |
| --- | ---: |
| Account restoration | 65.9 |
| TCP connect | 337.4 |
| SSH handshake | 1865.9 |
| Combined fresh identity checks and SSH | 2208.1 |
| Local model routes | 20010.5 |
| Local native account gather | 136.2 |
| Prepared-runtime health and resident handoff | 424.3 |
| Total preparation | 20655.5 |

Local routes invoke a plugin CLI for unknown models with sequential 5-second and
15-second deadlines, before applying saved user choices. This gates even an
unchanged prepared runtime. Startup now reads supported configuration formats
directly and applies authenticated user preferences; absent configurations need
selection rather than running a plugin host or launching an agent to discover it.

Standalone installed-bundle measurements on the Mac: deep/strict codesign
verification 2.79 seconds; ditto copy 1.03 seconds; APFS clone 0.26 seconds.
The old installer scans source, private copy, and final published bundle. The
new path clones, validates the private copy before executing it or stopping the
old UI, then atomically publishes that verified directory. Rollback separately
verifies the old application before reopening it.

The previous protocol heartbeat tolerated 45 seconds without a pong. A dead
prepared SSH connection could then spend another 35 seconds attempting channel
open, followed by an OpenSSH master whose local process check does not prove
remote health. Recovery now detects failure on the resident protocol, aborts the
stale underlying connection, wakes immediately, and coalesces fresh ownership-
verified in-process SSH establishment. Network events initiate a bounded probe;
active inbound transfer prevents a delayed pong from retiring a healthy link.

## Implementation and regression coverage

The resident transport probes every two seconds, with a three-second deadline;
network topology changes trigger a 1.5-second probe immediately. Confirmed offline
state closes the stale SSH connection before channel shutdown, avoiding an EOF
write blocked behind a dead socket. One pending reconnection serves all sessions.
Fresh account/runtime ownership, provisioned keys, and host keys remain required.
The connector also probes on topology changes, cancels setup tied to an obsolete
network path, and caps retry delays at five seconds.

Recovery readiness now requires both the protocol and the launch broker to be
ready. Previously an early protocol-ready event could wake the shim too soon;
the failed attachment then slept through another backoff interval. A watcher
whose first snapshot is already ready also wakes immediately, once. Regression
tests cover these transitions, probe coalescing, dead sockets, bounded abort,
active large transfers, route selection without CLI execution, configuration
and credential invalidation, and transactional installer rollback.

After a verified service starts, existing sessions can reattach while unrelated
agent configuration continues. New launches still require configured routes.
Mac connector setup starts as soon as the authenticated cloud SSH link exists.
Initial account synchronization now negotiates `model_accounts_gzip` separately
from the existing runtime-configuration compression capability. Old peers keep
the original encoding. Decompression retains the 256 KiB limit, rejects other
operation kinds, corrupt checksums, and ambiguous encodings. Actual Mac Magpie
configuration files required 131,984 JSON/base64 wire bytes; compression reduced
that to 36,012 bytes (72.7% smaller), before accounting for native-agent files.
This reduces configuration bytes sharing the connection with recovering TUIs;
the measured byte reduction alone does not prove an end-to-end timing gain.
Preparation fingerprints retain account/runtime/configuration/artifact isolation,
hash setup scripts, and use an explicit orchestration schema instead of the
entire local daemon executable. Bump `preparationSchema` for semantic changes
to local preparation that must invalidate an existing checkpoint.

## Measurements and validation

Linux setup, installation, terminalstream, CLI, and Runta tests pass. Mac race
suites for terminalstream, installation, CLI, Runta and targeted startup route
tests pass. Mac `make check`, `make check-ui`, and the complete signed package
verifier passed for candidates `0.3.11150308-dev`, `0.3.11150331-dev`, and
`0.3.11150340-dev`. The final CLI and connector changes passed Mac race tests.
Linux race execution is unavailable because the cloud lacks a C compiler;
race validation ran on the Mac instead.

One initial probe test exposed counting the initial ready frame as post-probe
traffic; timing now starts after readiness. A PTY cancellation test failed only
under nohup's inherited ignored SIGHUP; running the tests through ordinary SSH
passed, including three repeats of the probe and PTY cancellation tests. Build
packaging remains detached, but signal-sensitive tests run without nohup.

The first optimized installed restart samples still failed the 8-second gate
(12.91 and 11.04 seconds). These failures exposed configuration work on recovery
and the premature readiness notification described above; they were not counted
as successful acceptance. Update verification was tightened to require composer
output captured after the new attachment receipt, avoiding an old redraw that
could be mistaken for restored input.

Candidate `0.3.11150331-dev` passed three installed-shim restart samples:
7,839 / 6,965 / 7,477 ms through rendered input, followed by authoritative cloud
readback. Cloud preparation itself took 4,561 / 3,025 / 2,903 ms, versus the
20,656 ms baseline. Local model routing fell from 20,011 ms to approximately
1–6 ms. The same CLI process, cloud terminal address, creation timestamp, and
draft survived every sample; Ctrl+C detached in 1–3 ms.

A real Wi-Fi off/on cycle on `en0` restored local routing after 6,725 ms (including
the deliberate three-second outage and Wi-Fi reassociation). The original Codex
TUI accepted input 3,920 ms after routing returned, 10,646 ms from the beginning
of the disruption. This is not a simulated connectivity flag.

The actual downloaded-update path from `0.3.11150308-dev` to
`0.3.11150331-dev` restored the original terminal in 14,891 ms. Installation
stages: APFS copy 6.4 ms, deep signature validation 3,044 ms, native UI stop
3,849 ms, atomic renames below 1 ms, and UI reopen 170 ms. The new remote helper
installation added 2,343 ms during cloud preparation. Separately, stopping the
already running installed UI took 297 ms. The isolated first-execution experiment
below explains the larger update-path native-adapter cost. Do not remove signing
or force-kill the UI to hide it.

Observed successful remote operations take roughly 0.2–0.4 seconds, while a
fresh authenticated SSH connection takes about 2.1 seconds on this network
(some samples 3.7 seconds). Thus a complete daemon restart/update cannot equal a
single warm network RTT. Signature verification, graceful draining of in-flight
ChatGPT requests (up to three seconds), process startup, and changed remote
artifacts remain real costs. The optimization removes avoidable timeout chains
and polling gaps without claiming the absolute physical minimum is proven.

Mac evidence: `/tmp/fennec-recovery-performance/` contains baseline profiles and
owned terminal transcripts. `/tmp/fennec-recovery-build/` contains build/test logs.
The update fixture serves an already signed development bundle on loopback with
checksummed metadata. Test-only launchd environment overrides are restored on
exit; saved automatic-update preferences are never changed. The installed
`update.install` handler and surviving installed shim are the acceptance path.

The intermediate `0.3.11150340-dev` update also passed at 14,462 ms, with the
new matching daemon responding after 8,057 ms. A subsequent configuration-miss
restart took 8,269 ms and failed the 8-second gate. Its SSH setup took 3,185 ms
and resident operations that executed remotely in milliseconds took over a
second end to end. This motivated compressing initial account synchronization;
the failed sample remains recorded under `final/daemon-1.log` rather than being
counted as successful acceptance.

## First-goal installed baseline (superseded acceptance target)

Installed development version: `0.3.11150353-dev`, control build
`e809e2f249c2fa6671ced17e3f0027d69c260a0c9db59452bd549c925f210992`.

| Actual operation | Completed result |
| --- | ---: |
| Downloaded update through `update.install`, original TUI input-ready | 15,026 ms |
| Matching new daemon responding during that update | 8,122 ms |
| Installed-shim daemon restart, sample 1 | 7,754 ms |
| Installed-shim daemon restart, sample 2 | 7,825 ms |
| Installed-shim daemon restart, sample 3 | 4,869 ms |
| Corresponding cloud preparation | 4,263 / 3,069 / 2,802 ms |
| Real Wi-Fi cycle, local routing restored | 6,988 ms after disruption |
| Original TUI input-ready after routing restored | 4,367 ms |
| Ctrl+C detach after recovery | 1 ms in all four recovery samples |

All three final restart samples and the Wi-Fi sample passed the unchanged
8-second recovery gate, with original CLI/session identity, retained draft,
rendered new input, authoritative cloud input readback, and confirmed cleanup.
The Wi-Fi test took 11,355 ms overall, including the deliberately disabled radio
and reassociation. The final update's private-copy signature check took 2,891 ms,
UI-stop adapter 3,994 ms, copy 14 ms, reopen 370 ms, and changed remote helper
installation 2,618 ms. The update still takes about 15 seconds; it is not a
single-RTT operation.

An isolated APFS clone of the installed bundle, with a different bundle build
number and a new signature from the existing development identity, passed full
signature verification. Executing its native adapter against a nonexistent
target (no application to stop) took 4,032 ms the first time, versus 14 ms the
second time. Starting the processes took 43 ms and 1 ms respectively. This
isolates the approximately four-second cost to first execution of a newly signed
bundle, independently of terminating the old UI. The temporary clone was removed;
the installed bundle and running UI were not modified by this experiment.
Evidence: `compression/native-first-exec.log`. This is a development-signing
measurement, not a claim about notarized production releases or the precise
internal macOS subsystem responsible for that first-execution delay.

The reverse Mac connector is distinct from terminal readiness. Its measured
setup phases took 3,973–6,696 ms after owner validation in the final restart
batch; after Wi-Fi restoration its setup took 4,525 ms. It is verified connected
at completion. These figures must not be relabeled as cloud preparation or
hidden behind a process-start receipt.

One update fixture attempt failed before disruption because its shim produced
no receipt. The authoritative cloud listing showed only the preexisting sessions,
so no uncertain launch was replayed. The verifier now captures failed launch
output before closing its PTY. A subsequent complete update attempt succeeded;
the failed attempt is retained in `compression/update-launch-failed.log`.

Final Mac checks: account authorized, runtime ready, broker ready, connector
connected, and every preexisting terminal retains its address and creation
timestamp. Signing authority remains `Fennec Local Development`; credential
helper SHA-256 remains
`f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`.
The temporary update-feed/version launchd overrides and profiling marker were
restored. The complete launcher suite passed, including Codex/Claude retained
frame, silent grace, cancellation, and reconnect wake tests. Real TUI acceptance
used Codex; actual VPN and Ethernet cable changes were not separately exercised.
The explicit remote `fen run-agent codex --version` path also returned
`codex-cli 0.169.0`. All task-owned fixture servers and acceptance runners stopped,
and all changed source files matched between the cloud and Mac by SHA-256.

Final evidence lives on the Mac under
`/tmp/fennec-recovery-performance/compression/`; package, race, and launcher logs
are under `/tmp/fennec-recovery-build/`. Source changes are on
`fix/recovery-latency`, based on `a229eb0`, in matching cloud/Mac worktrees.
This task does not publish a production release.

## Second goal: reduce recovery toward measured network latency

The user rejected the previous 8-second gate as insufficient. A new active goal
uses the installed `0.3.11150353-dev` measurements above as its baseline. The
existence of several seconds of SSH setup, UI cold execution, or daemon draining
does not establish an architectural lower bound; these costs must be removed
from the interruption path or overlapped where safe. This goal is not complete.

The first candidate, `0.3.11151009-dev`, changes the following paths:

- Start a compatible existing terminal service immediately after authenticated
  ownership and host-key checks. Existing-session attachment no longer waits
  for native credential gathering, fingerprint checks, or helper upgrades. New
  launches remain gated on confirmed route configuration.
- Queue SSH exec without requesting an extra acknowledgement; the versioned
  protocol's ready frame is the execution confirmation. Channel-open is now
  separately timed.
- Opt in to independently compressed terminal-output frames, expanded in the
  daemon before forwarding to existing shims. Both flow-control windows and
  mailbox limits count uncompressed bytes. Old clients and servers retain their
  original representation; decompression checks size, checksum, and frame type.
- Use the running installed daemon's own native adapter to stop its UI. Schedule
  matching backend startup independently of the new native UI's first execution.

Real installed Codex-shim acceptance of that first candidate **failed** the new
3-second recovery gate: daemon restarts took 6,132 / 3,796 / 6,716 ms; recovery
after actual Wi-Fi routing restoration took 3,240 ms (baseline 4,367 ms). All
four samples reached input-ready original sessions and passed cloud readback
before failing the latency gate. They are not successful performance acceptance.
SSH handshakes alone took 1,833–3,833 ms in the restart samples. Evidence:
`/tmp/fennec-recovery-performance/latency-v2/` on the Mac.

The next candidate, `0.3.11151017-dev`, retains the authenticated SSH connection
during a known local routing outage. Local consumers enter their existing
reconnect UI while close frames are held until connectivity returns. Heartbeats
pause; a real pong is required before the channel becomes ready again. A dead
connection or changed NAT mapping falls back to fresh authenticated SSH setup.
No agent process is relaunched and no uncertain mutation is replayed. Tests
cover local reconnect signaling, deferred closes, failed offline launches,
same-transport restoration, compression compatibility, byte credits, and bounded
decompression. Mac race tests and signed package verification passed. Real
update acceptance took 10,868 ms, with the new daemon answering after 7,464 ms.
Installer stages were 2,883 ms for signature verification, 266 ms for UI stop,
235 ms for reopening, and 3,411 ms overall. This is a functional pass, not
completion of the performance goal. Evidence: `latency-v3/update-*.log`.

The first v3 restart took 9,518 ms and was over budget. Preparation also lost
its SSH connection while the local route remained available, leaving new
launches unconfigured. After confirming that the subsequent unreceipted launch
created no remote session, the acceptance loop was stopped and the existing
resident configuration was reconciled. All three user sessions were preserved.
The verifier's failure-output read could block on a surviving PTY slave; it now
terminates only its owned client process group and bounds diagnostic capture.

Preparation now retries typed transient connection failures even when there is
no additional routing event. Authentication and validation errors are not
classified as transient. Tests cover a mid-preparation EOF with a continuously
online route, single-flight recovery, and cancellation. This retry is restricted
to idempotent preparation; it never repeats an uncertain session launch.

The v4 candidate demonstrated that serially probing a dead retained connection
can add 1,500 ms before fresh SSH: its Wi-Fi recovery took 5,214 ms. The v5
implementation races that probe against one freshly authorized handshake. Only
the winning connection can execute commands; the loser is canceled and closed,
including a handshake that completes concurrently with cancellation. A strict
pong is required for reuse, rather than accepting buffered terminal output.

### v5 installed checkpoint (superseded), goal still active

Installed: `0.3.11151046-dev`, control build
`cca3523adb11038260c17cf5d8cc4d10ec17ae733c9b478cdae70f5a42abfe28`.

| Operation | Original goal baseline | Current observed result |
| --- | ---: | ---: |
| Downloaded update to original TUI input-ready | 15,026 ms | 10,868 ms (v2 to v3 update; not repeated for v5) |
| Restart to original TUI input-ready, all three samples | 7,754 / 7,825 / 4,869 ms | 8,934 / 6,516 / 5,830 ms |
| Complete cloud preparation for those restarts | 4,263 / 3,069 / 2,802 ms | 16,182 / 5,444 / 5,087 ms |
| Wi-Fi routing restoration to original TUI input-ready | 4,367 ms | 1,555 ms |
| Probe/reuse of original SSH after Wi-Fi restoration | Not retained | 797 ms; reuse confirmed |

The Wi-Fi sample improved by about 64%, but it is only one successful reuse
sample. Restart and preparation are not consistently improved and remain
failures. The 3-second intermediate screening gate is not the final physical
latency target. The v5 acceptance script exited nonzero. Do not average away
the slow startup or describe the entire goal as accepted.

Each recovered v5 sample confirmed the original installed CLI PID, cloud
address and creation time, retained draft, rendered input, authoritative cloud
readback, retained terminal frame, no retry counter, and 1–3 ms Ctrl+C detach.
All owned test sessions were stopped. The user's `t2vk`, `t33c`, and `t34v`
sessions retain their original creation timestamps. No release was published.

The required real warm-operation suite was also run against the installed v5
binary and **failed**: fresh SSH took 5,145 ms (5-second gate), and fresh/existing
`sessions.send` samples took roughly 0.96–1.08 seconds (500-ms gate). Mature
terminal input, reads, attachment, resume, and stop samples passed. The suite
already distinguishes Herdr's initial ten-second settling behavior from mature
terminals; neither case can be omitted from acceptance. Its temporary model
selection was restored. Log: `/tmp/fennec-recovery-build/warm-ops-latency-v5.log`.

Cloud package tests, Mac race tests, source/localization checks, UI checks,
signature verification, and the packaged Chromium smoke check passed. The
stable credential helper digest is unchanged. Fixture servers and acceptance
runners are stopped; update-feed/version overrides are unset. Real recovery
testing used Codex, not every harness or actual VPN/Ethernet changes.

Next work must address connection ownership across daemon replacement, avoid
serializing native update startup with backend readiness, and overlap local
preparation input gathering with authenticated SSH establishment. The current
early-resident path still performs a separate checkpoint health exec after
starting the protocol, adding work to full preparation. The reverse connector
also still establishes a second SSH connection. These are remaining structural
costs, not proven physical limits.

Evidence for this checkpoint is in `/tmp/fennec-recovery-performance/latency-v5/`
on the Mac, with earlier failed attempts retained under `latency-v2/`,
`latency-v3/`, and `latency-v4/`. The newest package/race logs end in
`latency-v5.log`. All changes remain uncommitted in the matching
`fix/recovery-latency` worktrees.

### Retained SSH carrier and overlapping preparation (v6 through v8)

The daemon's initial SSH connection now uses an account-scoped local transport
carrier on macOS. A separate launchd job keeps the authenticated WAN connection
alive across daemon bootout and application replacement. A private Unix socket
and SSH public-key authentication protect local leases; the worker verifies the
actual upstream host key. Its scope includes the endpoint/account-token digest,
runtime, client public key, host public key, key path, and protocol generation.
No API token or private key is put in argv or worker configuration. Fresh API
ownership/key checks still gate all commands. Logout and environment changes
revoke workers; an unleased worker exits after 30 seconds. Channels have separate
lifetimes, and no command or unknown mutation is replayed.

This preserves the SSH connection, not the resident terminal protocol process:
normal operations continue using one resident protocol channel per daemon, but
daemon replacement still starts a new channel/service on the retained SSH.
There is no agent-process pool. Recovery after an unusable connection can still
race a direct, freshly validated SSH connection against the retained probe.
The reverse connector remains separate and is not yet consolidated into the
carrier. Carrier bootstrap currently targets macOS launchd; portability and
fallback when the worker cannot start need additional hardening.

Tests exercise retained transport reuse across local-client replacement,
concurrent channel isolation, stdout/stderr/EOF/exit-status forwarding, TCP
channel forwarding, remote failure propagation, local identity rejection,
explicit revocation, scope separation, and idle expiry. Linux package tests and
Mac race tests passed. The first installed carrier candidate was
`0.3.11151112-dev`, control build
`417a6ac6d63c308bc4f5a751879c0f6ae9330e6a48072bbabed6d3793a1592d4`.

v6 measured restart-to-input times of **5,085 / 3,242 / 3,185 ms** and preparation
times of **4,499 / 2,619 / 2,925 ms**. All three restarts used the same carrier
PID; local SSH leases took about 5 ms in the latter samples. Wi-Fi recovery was
**4,158 ms** and did not reuse the old connection. These samples failed the
intermediate 3-second screening gate.

v8 additionally overlaps read-only SSH-key API requests with runtime/owner
resolution. A mandatory gate prevents creating/attaching keys or publishing a
host pin before the resolved runtime agrees. Tests cover rejection and changed
hints, including real HTTP provisioning with zero mutations on gate failure.
Local native inputs/fingerprints now run after owner verification while the
remaining connection work proceeds. If inputs are already ready, preparation
uses the combined checkpoint-check/service command; otherwise early existing
session recovery remains ahead of those inputs. The separate health exec still
occurs on the latter path and remains an optimization opportunity.

Routing events can arrive in bursts during Wi-Fi or VPN changes. Canceling a
recovery attempt no longer destroys an otherwise-live suspended transport. The
next route snapshot can probe it again. A canceled shared health probe is not
reused by the next caller. Regression tests cover cancellation during a routing
burst followed by successful reuse of the same transport. The actual v8 Wi-Fi
sample still needed a new SSH handshake, so the test does not establish a real
Wi-Fi latency improvement.

### Latest installed checkpoint, goal still active

Installed through the actual Update flow: `0.3.11151125-dev`, control build
`7b79ffd40d650d22ec86719387d661cb3a4593d41d2e6766684b17550e2c6e3e`.

| Operation | Goal baseline | v8 measured result |
| --- | ---: | ---: |
| Downloaded update to original TUI input-ready | 15,026 ms | 10,878 ms |
| Restart to original TUI input-ready, all samples | 7,754 / 7,825 / 4,869 ms | 5,557 / 2,181 / 2,121 ms |
| Complete cloud preparation, all restart samples | 4,263 / 3,069 / 2,802 ms | 2,612 / 1,665 / 1,676 ms |
| Wi-Fi routing restoration to original TUI input-ready | 4,367 ms | 5,758 ms |

The same carrier PID served all three restart samples and the update. Fresh
runtime/SSH API validation decreased from approximately 1,550 ms to 790–1,040 ms.
Two restart samples pass the intermediate 3-second gate, but the first restart,
update, and Wi-Fi sample fail. The physical-latency goal is **not complete**.
Do not substitute the two fast restart samples for the whole batch or claim a
network improvement. The acceptance scripts returned nonzero.

Update stages: clone 8 ms, deep signature verification **3,869 ms**, stop UI
276 ms, reopen UI 337 ms, installer **4,497 ms**. There is still **3,647 ms**
between installer completion and entry into the new CLI's `ensureControl`.
Once there, daemon replacement/registration/readiness took 277 ms; the new daemon
answered **8,421 ms** after the update began. The uninstrumented gap must be
traced through child spawning, executable entry, and native startup; it is not
proven to be an immutable OS cost. Signature verification also remains on click.

The first v8 update attempt failed before installation: no shim launch receipt
arrived and Darwin's `wait4` blocked while reaping the killed owned CLI. The
verifier was stopped with SIGQUIT to retain its Go stack. Authoritative session
inspection confirmed only the three original user sessions before a new attempt.
Failure cleanup now bounds both process reaping and PTY diagnostic capture.
Failed-attempt artifacts remain under `update-attempt-1-*`; no uncertain launch
was replayed. The second attempt installed v8 and recovered the same owned TUI.

The actual installed warm suite also **failed**. Cold SSH passed at 4,145 ms.
Model switches took 560 / 506 ms. Fresh sends took 1,000 / 980 / 965 ms; existing
sends took 816 / 915 / 813 ms. Their remote Herdr send stage took 509–768 ms.
One mature send also failed at 761 ms (remote send 203 ms); the other mature sends
passed. Usable attach/resume samples were 250–350 ms, and all three actual Codex
startup samples passed at 349 / 324 / 320 ms. Preserve the fresh, existing, and
mature failures. The suite restored its temporary model choice and cleaned up
its owned fixtures. Log: `/tmp/fennec-recovery-build/warm-ops-latency-v8.log`.

The signed package passed `make check`, `make check-ui`, deep signature/package
verification, and Chromium smoke checks. Mac race tests passed for CLI, Runta,
terminalstream, and sshcarrier. Recovery tests checked original CLI PID/cloud
identity and creation time, retained draft/frame, fresh input and authoritative
readback, no retry counter, and Ctrl+C detach. Real harness coverage is Codex;
Claude, other harnesses, VPN, and Ethernet remain unverified in these runs.

At the latest checkpoint daemon PID was 93693, authority remained
`Fennec Local Development`, and the credential-helper digest remained
`f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`.
Original sessions `t2vk` / `t33c` / `t34v` retained creation timestamps
1794650696611 / 1794660066896 / 1794661987020. All task-owned cloud sessions were
cleaned up. Fixture server PID 85044 was stopped; update-feed/current-version
overrides are unset and the profiling marker is absent. No release was published
and no source changes were committed.

Evidence is on the Mac under `/tmp/fennec-recovery-performance/latency-v6/` and
`latency-v8/`. v7 was a staged build, not a separately installed/tested candidate.
Next work: trace and remove the update startup gap, move safe verification work
out of the click path, preserve the resident protocol across daemon replacement,
remove duplicate reverse-connector establishment, replace recovery polling with
readiness notifications, and investigate Herdr's mandatory fresh-send settling
without bypassing input correctness or masking failures.

## v10–v15 follow-up

Real signed-bundle probes isolated the startup gap: first execution of a private
whole-app copy took 3,351 / 3,384 ms, subsequent execution 21–25 ms, and execution
after renaming the same copy 25 ms. Background update preparation now verifies
the private candidate with the installed signer, warms its side-effect-free help
command, and retains a process-local integrity seal. Apply checks file hashes,
metadata, symlinks, and extended attributes before stopping the UI and again
before atomic publication. Timestamp-only validation is insufficient. The v12
staging attempt failed before installation because the callback required an
`.app` suffix; the integration now temporarily restores that suffix and has a
regression test. Kernel readiness notifications wake reconnecting clients, which
still verify live daemon readiness; older daemons retain polling compatibility.

Installed v14 (`0.3.11151219-dev`) real measurements:

| Operation | Milliseconds |
| --- | ---: |
| Downloaded update to original TUI input-ready | 4,381 |
| Installer | 1,306 |
| Candidate integrity scans | 473 / 546 |
| launchd kickstart to process entry | 35 |
| Restart to original TUI input-ready | 3,631 / 3,333 / 2,133 |
| Cloud preparation during those restarts | 2,600 / 2,667 / 1,693 |
| Wi-Fi route restored to original TUI input-ready | 1,581 |

Update and two restart samples still fail the intermediate 3-second screening.
Original CLI/session identity, frame/draft retention, fresh input with cloud
readback, and detach checks passed. A separate configuration-miss restart took
about 11 seconds and must not be treated as a warm-cache sample. The warm
operation suite has not been rerun since v8; its failures above remain unresolved.
Evidence: `/tmp/fennec-recovery-performance/latency-v10/`, `latency-v14/`, and
`agents-single-restart-state.jsonl` on the Mac. Focused cloud tests, Mac
installation/updater race tests, and signed package checks passed.

The reported repeated loading concerns the per-agent header, not the global
preparation banner. One update test caused a second restart during cleanup; its
runner now restores fixture overrides before the actual update. This does not
prove the product's per-agent loading recurrence is fixed; trace active agents
and partial route updates separately.

v15 (`0.3.11151236-dev`) removes only the agent loading label's ellipsis via
`.detected.loading-shimmer::after`, preserving shimmer. Light/dark browser
previews verified the suffix is absent and the animation remains enabled. The
verified signed bundle is installed locally; daemon PID 59319 reports authorized
and broker-ready, with Codex, Claude, and Pi configured. The stable signer and
credential-helper digest are unchanged; profiling is disabled and the owned
preview server is stopped. Performance acceptance remains incomplete.

## v16–v18: resident health and configuration reuse

Installed v16 (`0.3.11151250-dev`) adds the typed `preparation_health` operation.
It runs the same fixed marker, binary, Herdr and optional gateway checks over
the existing resident channel, with SSH fallback only for older capabilities.
Invalid requests, negative health, malformed replies and transport failures
cannot establish readiness. The remote worker now bounds its check to five
seconds too. Real v16 restart samples measured health at 611 / 229 / 217 ms,
total preparation at 1,989 / 1,521 / 1,625 ms, and original TUI input-ready at
3,089 / 2,893 / 2,283 ms. The first recovery still fails the 3-second screening.

The v16 Wi-Fi sample failed at 3,703 ms after routing returned. The retained
socket's strict probe expired after 1.5 seconds; two fresh SSH handshakes then
ran concurrently (resident connection and reverse connector), costing about
1.8–1.9 seconds each. They are still independent; coalescing these connections
remains required. Evidence: `/tmp/fennec-recovery-performance/latency-v16/`.

Native preparation previously published all configured agents, cleared its
result accumulator, and published partial completions as replacement lists.
This could briefly revoke a ready peer. Results now update by agent and retain
verified peers; failures replace success rather than being hidden. Early success
also requires verified binary inventory. Per-agent observers saw one readiness
wave per restart in these samples, but this does not cover every UI recurrence.

The user reported repeated `Migrate credentials` during restarts. Three measured
v16 ordinary restarts hit the full preparation cache without account-sync work.
However, executable digests were part of that same cache, so installing a new
cloud service unnecessarily invalidated unchanged credentials and model routes.
v17 adds a separate configuration digest from the same source-input snapshot;
it retains account/runtime scope, native payload, model/plugin preferences,
integration mode, preparation schema and setup scripts, excluding executable
bytes. A full verified hit can populate the new field for an older checkpoint.
After installing changed executables, reuse additionally requires the previous
remote configuration marker, all agent binaries, Herdr and gateway health.
Failed checks and changed credentials still take the synchronization path.
When remote configuration semantics change, bump `preparationSchema`; a binary
change alone now intentionally does not invalidate applied configuration.

Actual Update v17 (`0.3.11151300-dev`) to v18 (`0.3.11151304-dev`) changed the
cloud terminal SHA256 from
`0a510b1c8376ff69a5d3b1e6782ee1a77567ccfba599ccd09cde792bef70047d` to
`f93c7eaf225e0209f26b05a046f4de100fb510bffa861540c1705749264fa100`.
The daemon reported `configuration_reused_after_installation`, a path that
returns before credential synchronization. The original TUI recovered with
cloud-verified fresh input in **3,795 ms**, versus v14's 4,381 ms and the goal
baseline's 15,030 ms. This still fails the 3-second screening. The installer took
1,526 ms and the replacement daemon answered at 1,993 ms. Background agent
preparation completed later; the profiling marker was removed after the TUI
verifier, so that profile cannot establish total background preparation time.
Evidence: `latency-v18/update-e2e.log`, `update-profile.jsonl`, and
`agents-v18-update.jsonl` under `/tmp/fennec-recovery-performance/` on the Mac.

The v18 installed warm suite **fails** its unchanged per-sample 500 ms gate:

- Model changes: 503 / 509 ms.
- Fresh sends: 1,035 / 1,028 / 844 ms; existing sends: 819 / 781 / 849 ms.
- One mature send: 587 ms; one read: 666 ms; one refresh: 614 ms.
- One usable resume: 531 ms; one stop: 573 ms.

Other samples, including actual Codex startup, passed. The send trace still
attributes most excess to Herdr settling. Preserve these failures rather than
using averages or receipt latency. Log:
`/tmp/fennec-recovery-build/warm-ops-latency-v18.log`.

Focused new regressions, Mac race tests, vet, package/signature checks and UI
checks passed. Full cloud CLI/setup/terminalstream and runtime packages passed
in their final runs. An earlier credential propagation test failed once; an
isolated rerun and subsequent full CLI run passed, with no production fix for
that intermittent failure. An unrelated MCP-cache test failed because its fake
server's asynchronous writes could reorder the refresh and its processing
barrier. It now waits for the first write; 100 repetitions and the full runtime
package passed without weakening assertions.

The local signed v18 daemon is PID 84404, build
`eb3e0800777d80acdbde392b754f842d0e737f7903215801734c3f1b047bf9cb`.
Original sessions t2vk/t33c/t34v retained their original creation timestamps
after the real update. Update fixture PID 82295 is stopped, observation probes
have exited, update overrides are unset, and profiling is disabled. The full
performance goal remains active; no commit, push or release was performed.

## v19–v22: credential invalidation and reverse-link regression

v19 shares the daemon's verified SSH connection with the reverse connector,
coalescing its reconnect with the resident connection. A measured v20 restart
reported `connector.connect = 0 ms`; pairing, relay setup and alias registration
still cost three serial operations. Scope checks fence account/runtime changes
before and after borrowing. Standalone connectors retain their original API
ownership validation.

v20 records private per-input hashes, never credential contents, and public
changed-input names. The reported recurring setup was reproduced: only
`magpie/logins.json` changed, but this invalidated the complete configuration
and reran the gateway install/service scripts. One restart took 4,748 ms for
preparation, although the original TUI recovered in 2,023 ms. Another unchanged
restart hit the cache but took 3,102 ms for input readiness. A private local
field-hash observer subsequently identified only `seen` on two login records.

v21 canonicalizes gateway JSON for fingerprinting and excludes only the login
record's top-level `seen` observation timestamp. Every other field, unknown
fields, nested authentication fields and policy changes remain dependencies.
Gateway lifecycle proof is now independent of account/model content: reuse
requires a matching account/runtime/script fingerprint and a live remote
marker/service check. Failed health still repairs the gateway. Progress leaves
credential migration when account synchronization completes, instead of keeping
that label through route configuration. After the observed `seen` changes,
the installed daemon reported `hit` despite raw/semantic input diagnostics
showing `logins.json` changed.

The retained SSH worker now keeps two empty session channels after its first
authorized request. It does not prestart commands or agents. Real restart
channel-open times fell to 0.2–0.6 ms; changing the worker protocol required one
cold transport replacement during v21 installation. Tests block fresh channel
opens to prove a replacement uses an already confirmed channel.

v21 Update took 6,314 ms; restart samples took 4,158 / 1,999 / 2,028 ms;
Wi-Fi restoration to original TUI input took 3,664 ms. All four functional
identity/input checks passed, but the update, first restart and Wi-Fi samples
failed the unchanged three-second screening. Critically, real reverse SSH then
became unavailable while the Mac remained online and the UI showed connected.
This is a regression, not an acceptable performance tradeoff. The user restored
connectivity by restarting the local daemon and its retained transport worker.
Original cloud sessions t2vk/t33c/t34v kept their creation timestamps.

Review found the shared-connection change had missed `serveRelay`'s unconditional
`client.Close()` during cleanup. v22 cancels only the borrowed relay's channels,
uses context-aware forwarding/channel opens, and rebuilds reverse bridges after
topology changes while the shared connection owner handles SSH recovery. New
tests exercise actual forwarding failure, cancellation with parked bridges, and
delayed channel-open replies while requiring a peer command to remain usable.
These tests supplement, rather than replace, actual reverse-SSH acceptance.

v22 also overlaps local login-shell discovery with cloud authentication and
coalesces concurrent readers into one process. It retains the original shell
allowlist, cache invalidation and cancellation semantics. Focused Mac race tests,
full cloud CLI/setup/localssh/sshcarrier tests, vet, source/UI checks, and signed
package verification passed. The deployed development version is
`0.3.11151408-dev`, build
`3113a99b2be73d29c36260220484ab96ea55f2ebace94857f07b3c83023efaf9`.

Actual Update v21→v22 reused configuration after the executable installation,
without gateway installation/start or credential-migration stages. Original TUI
input recovery took 4,573 ms, with the replacement daemon answering at 2,621 ms.
Three subsequent restarts took 4,515 / 2,210 / 1,791 ms and hit preparation cache.
The first restart and Update still fail the screening. These results do not
supersede the failed 500 ms warm-operation gate or establish a physical limit.
Evidence is under `/tmp/fennec-recovery-performance/latency-v20/`, `latency-v21/`
and `latency-v22/` on the Mac; build/test logs are in
`/tmp/fennec-recovery-build/`. The performance goal remains active.

## v23–v24: recovery proof and an unresolved installation interruption

v23 limits reverse-bridge cancellation to offline transitions. Ordinary online
route notifications no longer repeatedly rebuild a healthy relay. Its real
Wi-Fi acceptance still failed timing: 10,339 ms from route restoration to
verified original-terminal input, 17,151 ms including the outage. Session
identity, retained terminal content, input readback and detach passed. Actual
reverse SSH also passed and the local recovery watchdog did not fire. The
reverse observer's 22,886 ms includes waiting for terminal acceptance; it is
not a measured reverse-link recovery time.

The v23 trace first recorded a 456 ms successful retained-connection probe,
then a roughly six-second stall and heartbeat retirement, followed by a fresh
connection. An unnumbered late pong could satisfy a new recovery probe, and an
old heartbeat deadline could retire a newly resumed connection. These are
reproducible protocol defects; they are not yet proven to explain the entire
live delay.

v24 advertises `probe_id`, echoes each ping ID, and acknowledges only the
matching pending probe. Suspension invalidates a pre-outage probe. Heartbeat
retirement is fenced by the suspension generation. Legacy peers retain normal
heartbeat compatibility but cannot prove retained-connection recovery using an
unnumbered pong; recovery falls back to authenticated reconnection. Regression
tests cover stale replies, matching recovery replies, old heartbeat deadlines
and legacy interoperability.

Final cloud terminalstream/CLI/localssh/sshcarrier tests passed. Mac focused race
tests, source/UI checks and complete signed-package verification passed for
`0.3.11151433-dev`. The four v24 protocol/test source files reached the Mac
worktree before building. The installed version and live v24 behavior have not
yet been confirmed.

Starting `/tmp/fennec-latency-v2-install.sh` through a background SSH shell again
made reverse SSH unavailable. Both ordinary and nonmultiplexed SSH attempts
failed. Closing the specifically identified task transport's cloud SSH child
PID 790359 did not restore access; cloud agent sessions were not stopped. The
user has been asked to restart the local daemon/transport worker so installation
logs can be inspected. The install script's membership in the retiring daemon's
launchd job is a possible cause: nohup does not establish an independent job.
This is a hypothesis pending Mac logs, not a confirmed product diagnosis.
Future installation/restart acceptance and recovery watchdogs must run as
independent launchd jobs.

Additional cloud tests verify stdin, EOF and exit-status forwarding through
retained channels over successive daemon leases (20 repetitions passed). This
test and this task record remain pending synchronization while the Mac is
unreachable. v24 restart/Wi-Fi acceptance has not run; repeated reverse SSH
acceptance, source reconciliation and profiling/test-job cleanup remain open.
The performance goal is active and the existing 500 ms warm-operation failures
remain failures. No commit, push or release was performed.

The follow-up connectivity check still failed. `cmd/run-macos-check` now submits
an explicitly selected command as a unique, independent launchd job. It requires
a new output directory, preserves arguments without a shell interpolation step,
captures output, and atomically publishes the command's actual exit code only
after that command finishes. A bootstrap observation failure identifies the
job to inspect rather than automatically retrying it. Its Linux worker check
correctly recorded exit code 7 as failure; Go vet and the Darwin/arm64 build
passed. Actual launchd lifecycle verification is pending Mac connectivity.

After synchronization and rebuilding the helper on the Mac, submit installation
acceptance with a fresh directory, for example:

```sh
go build -o /tmp/fennec-run-macos-check ./cmd/run-macos-check
/tmp/fennec-run-macos-check -output /tmp/fennec-install-v24-independent -- /bin/bash /tmp/fennec-latency-v2-install.sh
```

Inspect the returned launchd job and `result.json` after reconnecting; a missing
result is not success or proof that the worker stopped. Use the same independent
runner for restart/Wi-Fi acceptance and a separately submitted recovery watchdog.
Remove each completed task-owned registration with `launchctl bootout` using its
exact returned job name. Source and task-record changes from this continuation
are retained in the cloud and still pending Mac synchronization.

The third consecutive goal turn rechecked the Mac using a fresh, bounded SSH
connection and again received `Fennec: this Mac is not reachable: context
deadline exceeded` (exit 255). Available cloud checks and the independent runner
are complete; further diagnosis depends on the Mac's installation logs and
actual process state. The goal is blocked on restoration of Mac access, not
complete. Preserve the full performance objective and resume with log inspection,
source reconciliation and independent-job acceptance when connectivity returns.

## v25–v26: retained transport incident and runtime liveness

The user restored access with a local Codex session by replacing the transport
worker, preserving existing cloud sessions. Recovered logs disproved the earlier
installation-interruption hypothesis: v24 installation completed successfully,
including matching backend startup and signature/helper checks. Its preparation
remained blocked for 7,435,128 ms. Connector pairing repeatedly stalled, including
90,001 ms samples; after replacing the worker, pairing completed in 426 ms.
The regression was an indefinitely reused retained transport, not a failed bundle
installation or a reason to weaken credential/security checks.

The worker's local SSH handshake hid an unresponsive upstream. It retired that
upstream only when `ssh.Client.Wait` returned; a half-open connection could keep
all new leases stuck indefinitely. A TCP-blackhole regression failed before the
fix and passed after bounded health retirement was added. Retirement now fences
the specific upstream, closes its physical socket before channel cleanup, and
closes only leases bound to that upstream. A late failure cannot close a new
connection generation. Ordinary lease release continues preserving peers.

v25's connection-level keepalive was insufficient. Actual fault injection on the
runtime SSH hop did not trigger the new worker health retirement; the local
recovery watchdog fired, so this acceptance failed. Connection-level responses
cannot establish end-to-end runtime liveness through an intermediate gateway.
The ordinary v25 daemon restart preserved its CLI, session, draft, input readback
and detach, but took 4,135 ms and failed the unchanged three-second screening.
The new independent install runner completed successfully. Its first recovery
guard failed to start because scp had failed to transfer the executable; the
guard was subsequently transferred through tar/SSH and independently registered.
Later disruption tests explicitly checked that their guard process was running.

v26 verifies unpredictable bytes through one runtime echo process (`exec
/bin/cat`) on a dedicated SSH channel. This process interprets no input, reads
no files, and launches no agent. Its first use is gated on an authorized channel
request; speculative SSH authentication alone cannot launch it. Subsequent
probes reuse that channel. This replaces the earlier empty-channel-only worker
design with an explicit end-to-end health service. A known local offline period
preserves the retained connection; after routing returns, a failed bounded probe
retires it. Worker protocol scope advances to v4 so updates cannot keep the
previous implementation resident indefinitely.

Tests cover a fully blackholed TCP connection, a responsive gateway with blocked
runtime channels, a stalled already-running echo service, offline preservation,
healthy peer leases, and no runtime operation before authorization. The gateway
case failed with v25's global keepalive and passes with the runtime echo.

The explicit Mac integration test creates an isolated worker over the real
gateway. Its cloud observer pauses only that worker's newly created `/bin/cat`
process, independently schedules its resumption, and requires upstream retirement
followed by a fresh remote command and exact output readback. This passed on the
real Mac/runtime link without disrupting the production daemon or user terminals.
The 14.48-second test duration includes connection setup, deliberate setup waiting
and replacement verification; it is not a TUI recovery latency measurement.
Evidence: `/tmp/fennec-carrier-echo-probe.log` on the cloud.

v26 packaging, installed-path restart acceptance, final source reconciliation and
test-job cleanup are still pending. The overall performance objective has not
been achieved; prior timing and warm-operation failures remain failures.

## v27: busy-link protection and installed verification

v26 installed successfully, but its first cold preparation triggered health
retirement while runtime data was still arriving. That was another failed
acceptance boundary. v27 counts only actual runtime stdout/stderr on channels
bound to the same upstream generation as evidence of continued progress. Such
progress extends an outstanding probe deadline; gateway keepalives and local
handshake traffic do not. The worker still retires when both the end-to-end echo
and runtime data stop. Tests keep application output flowing while deliberately
delaying echo, and separately prove a live gateway cannot hide stopped runtime
data. Runtime activity is isolated by upstream generation. Worker scope is v5.

Cloud sshcarrier tests/vet and Mac sshcarrier/localssh race tests passed. The
isolated real Mac/gateway/runtime failure test passed again with the final source
(`14.74 s` total diagnostic duration, not a TUI recovery measurement), recorded in
`/tmp/fennec-carrier-echo-probe-v27.log`. Source/UI checks and complete package
verification passed. Installed version: `0.3.11151745-dev`, build
`836a4ffe4df08d5fe90c3eb2d4c44641451a451d9b9a7cc1c61bbd7e4d63465f`.

The independent install job exited zero. Its reverse-SSH observer passed, and the
recovery watchdog did not fire. A subsequent installed-shim daemon restart
preserved the original CLI, original session, draft, fresh input readback, and
detach. Recovery took **5,046 ms**, failing the unchanged three-second screening;
daemon availability was 643 ms and stop was 71 ms. The reverse-SSH observer passed
and that watchdog also did not fire. The retained worker PID 50361 survived both
installation startup and daemon replacement without health retirement. The
current daemon is PID 51600. Evidence is under `/tmp/fennec-v27-install/`,
`/tmp/fennec-v27-restart/`, and
`/tmp/fennec-recovery-performance/latency-v27/` on the Mac. Observer durations
include scheduling/retries and are not a TUI or pure reverse-link latency metric.

Authoritative session refresh confirmed all four user sessions remain live with
their original creation timestamps: t2vk 1794650696611, t33c 1794660066896, t34v
1794661987020, and t4vy 1794721683114. Completed v25–v27 task-owned launchd jobs were
removed using their exact registered labels; test logs and failure artifacts
remain available. No commit, push, release or user-session termination was done.
The recurring retained-transport failure now has a real-topology regression test
and installed startup coverage. The overall performance objective is still
incomplete; the warm-operation gate and this restart latency remain failures.

Final reconciliation matched all task source files on cloud and Mac; the updated
task record was then synchronized and its hash verified. Update-feed/version
launchd overrides are unset and the task-created profiling marker was removed.
The final live check still had daemon PID 51600 and worker PID 50361. Preparation
reported ready, but Claude's route remained `verification_failed` and Cursor's
route `credentials_required`, as already observed before the final worker fix;
this is not evidence that every agent route is ready. No credentials were reset.

## v28/v29: Claude route recovery and explicit credential synchronization

The user's `claude --resume` was rejected before reaching Claude because the
Mac daemon marked its cloud route `verification_failed`; the UI showed only the
saved cloud location. The remote settings selected `sonnet[1m]`, explicitly
pinned to `claude-opus-5-5[1m]`, while the saved Fennec route omitted the context
modifier. Readback now resolves explicit Claude alias pins and respects the
ANTHROPIC_MODEL override, accepts an existing Claude 1M modifier when the desired
route does not specify context, and preserves the settings bytes. Different
providers/models and explicitly requested context remain distinct. Unchanged
routes no longer invoke Magpie or hold its config lock for fallback CLI writes.

The expanded cloud agent card now offers Sync credentials with inline progress
and outcomes. The resident `credentials_sync` operation gathers Mac credentials
afresh, merges shared Magpie account stores or the selected native agent's files,
and verifies that agent's configuration. It does not install plugins, restart
the gateway, change the global native/gateway mode, or issue inference requests.
Native newer-grant rules and cloud login isolation remain in force; denied local
reads and ambiguous conflicts are surfaced without overwriting cloud accounts.
Results contain no credentials. Sync completion does not claim provider
inference authentication has succeeded. UI and CLI now explain failed model
configuration instead of presenting an unexplained cloud launch rejection.

The first v28 installation exposed a second upgrade defect: early session
recovery started the old on-disk resident before installing the new executable,
then kept using that old process for later operations. The new credentials
operation correctly refused to run without its capability. v29 permits early
resident reuse only when the artifact-scoped installation checkpoint matches;
changed builds finish installing before publishing the resident. Unchanged
restarts still reuse early recovery. This avoids treating wire protocol
compatibility as proof that updated behavior is running.

Validation: setup/cli/runtime/terminalstream package tests and vet passed on the
cloud; focused credential/alias race tests, source/UI checks, signing and package
verification passed on the Mac. `make preview-ui` was inspected in English and
Simplified Chinese, light and dark, including failure and sync states; result
changes measured zero layout shift. These previews are not native click or
provider-authentication evidence. Installed version is `0.3.11151823-dev`, build
`14921e897c366a597772cb69969824730af576329cbfe3c5bee4b466084d7461`, with the original
Fennec Local Development signer and unchanged credential helper hash.

The real `agent.credentials.sync` control request returned `synced` and Claude
`configured`. The normal Mac login shell launched a cloud Claude version command
through the installed broker, producing `2.1.325 (Claude Code)` from terminal
t4zv and a confirmed normal exit. A bare SSH shell initially lacked the original
Claude binary in PATH; that attempt failed before launch and was not counted as
acceptance. No inference request or resumption of the user's conversation was
performed. Existing user terminals t2vk, t33c, and t4vy remain live with their
original creation timestamps; the user had already exited t34v before this work.

The installed-shim restart test preserved the original CLI, cloud terminal,
draft, input readback, and detach. Its **5,265 ms** recovery still FAILS the
unchanged 3-second screen (daemon availability 414 ms, stop 88 ms). Warm cloud
preparation now hits the complete checkpoint and measured **2,897 ms**, versus
v27's 11,424 ms failing-route preparation; it performs no repeated account sync
or route configuration on that hit. This does not meet the physical-latency goal.
First post-fix full preparation was 8,375 ms, with Claude readback 207 ms. The
latest full Wi-Fi/update-to-TUI gates have not been rerun. Three performance
objectives and the strict 500 ms warm-operation gate remain unresolved.

Install/restart logs are under `/tmp/fennec-v29-install/` and
`/tmp/fennec-v29-restart/` on the Mac. Both independent recovery guards exited
without firing; reverse SSH works after both disruptions. Current daemon PID is
91527. Source reconciliation and task-owned job cleanup follow these checks.

## v30–v33: resident bootstrap, automatic Magpie sync, and PTY IO repair

The current measurement ledger is [performance-baseline.md](../performance-baseline.md),
with all numeric warm-operation samples in
[2026-11-15-v33-ops.json](../benchmarks/2026-11-15-v33-ops.json).
It separates real recovery, local/cache operations, completed remote operations,
and historical results that have not been retested on this build.

Warm connector bootstrap now performs pairing-public-key retrieval, existing
relay validation and alias registration in one resident `connector_prepare`
request. Identity, process command line, relay source revision, listener and
key permissions remain checked. Only explicit `needs_setup` before mutation
permits the legacy cold path; errors or ambiguous replies never replay setup.
The connector continues to borrow the shared SSH lease without owning peers.
Preparation can check the previous complete fingerprint in the resident-start
exec and reuse that proof only if freshly gathered inputs match. The artifact
checkpoint still forbids starting an obsolete resident during an upgrade.

Magpie credentials are watched with the existing platform file notifications.
The watcher starts synchronously before preparation gathers its snapshot,
debounces changes for 150 ms and sends changed providers/logins/plugin-auth
through `magpie_credentials_sync`. It ignores login last-seen timestamps and
format-only changes, retains unconfirmed content across failures, retries on
connection readiness and every five seconds, and uses the established cloud
merge rules. Native mode does not read/upload Magpie stores. Unrelated catalog
writes cannot block credential collection. No gateway restart, plugin install,
model mutation or inference probe is part of this operation. Tests cover atomic
replacement, interrupted uploads, edits during upload, newer cloud grants,
disabled integration, payload negotiation and confirmed resident-protocol writes.

The user requested removal of manual synchronization after reviewing the v31 UI.
The row, translations, timers, Swift bridge, control handler and unused manual
recovery operation/helpers are removed. Automatic native and Magpie sync remain.
Actual `make preview-ui` light/dark and English/Chinese previews confirmed no
manual control, residual row, or manual bridge callback. Preview fixtures are
not authentication evidence.

v31 restart exposed a reproducible PTY defect: `os.File.Fd()` restores a
Go-managed descriptor to blocking mode, including calls inside `pty.Setsize`.
The terminal service was enabling nonblocking once and then undoing it during
reads/writes/resizes, allowing stale poll readiness to block a session lock and
protocol progress. The new regression failed on the previous implementation.
v33 captures the descriptor once, enables nonblocking afterward, and uses that
descriptor for poll/read/write/resize. Tests verify nonblocking flags and actual
resize behavior. No retained SSH worker/health policy was changed for this fix.

Cloud affected-package tests/vet and Mac focused race, source/UI, full package
and signature checks passed. Installed v33 is `0.3.11151959-dev`, build
`65beaa5bd8d5ccbd60e33aa94cdf9e1ba099f4f0e1d70838bd844bad366fa18c`, signed by
Fennec Local Development; the credential helper hash remains
`f69a5f6c9bd8a928771bf151f564ff4c25c065df8ee390bad8291a0dbad539fe`.
The first v31 install runner could not execute a non-executable shell script;
it made no installation changes. A distinct subsequent job invoked bash and
completed. v32 was built but not installed; v33 includes the requested removal.

Latest real acceptance:

- v33 restart: preparation **1,716 ms**, connector setup **1,015 ms**, original
  TUI fresh input **5,221 ms**, daemon response **269 ms**, detach **2 ms**.
  Input, original CLI/session identity and draft passed; the 3-second screen failed.
- v33 Wi-Fi: local routing restored **6,449 ms** after disruption, original TUI
  input **3,375 ms after routing restored** (**9,825 ms total**), detach **3 ms**.
  Identity/input passed; the 3-second screen failed. Independent recovery guards
  did not fire and reverse SSH recovered after both disruptions.
- v33 warm operations: real attach **236–246 ms**, resume **230–271 ms**, real
  Codex TUI start **302–357 ms**, reads **185–379 ms**, stops **238–321 ms**.
  Fresh sends **968–1,037 ms**, existing sends **491–837 ms**, mature sends
  **457–547 ms**, models **514–743 ms**, refresh **460–575 ms** retain failures.
  The unchanged 500 ms suite exited nonzero. Full samples, not only successful
  ones, are committed in the ledger. No production credentials were changed to
  generate an automatic-sync timing.

The failed v31 restart timed out on restored input and retired a resident
heartbeat; it must not be counted as a successful 1,665 ms recovery. v33's
functional checks pass, but the remaining startup contention and control/model/
send tails do not meet the goal. Update-to-input has not been rerun since v22.
Original user sessions t2vk, t33c, t4vy and the subsequently created t529 remained
live with unchanged creation times at the pre-benchmark check; acceptance only
stops its own labeled sessions. Final reconciliation/cleanup and publication
are recorded with the accompanying commit.
