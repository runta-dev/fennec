# macOS popover acceptance

## 2026-11-10: fen resume latency and loading

The old resume path cleared its spinner immediately after the local service
reply, before starting SSH attach. Measured local lookups were 9–14 ms, while
usable-screen times were 1192–1920 ms. Resume now uses the resident Go terminal
channel and a separate stream identity for attaching an existing Agent session.
It does not call the Agent creation path. Loading remains until terminal output
arrives, and the session-title suffix filter stays active.

Final packaged-build resume samples were 753/845/863 ms from command execution
to an input prompt, with local lookup around 1 ms. The same isolated session was
resumed and detached three times, all with a visible spinner and successful exit.
`cmd/profile-resume` provides the Go measurement. Resume may restore a screen
without replaying the welcome banner, so the input-prompt condition also accepts
the existing Codex composer placeholder; an update dialog is not accepted.

A real final-output/exit race was fixed: failed late output-credit writes no
longer cause automatic reconnection before a buffered exit frame is read. A
socket regression test closes the peer's read side before sending the final
data and exit frames, and verifies normal client exit. PTY child TERM is now
explicitly propagated. Input forwarding tests also passed on the Linux runtime.

## 2026-11-10: startup network-floor investigation

The user subsequently excluded an Agent process pool. A diagnostic persistent
command-channel prototype therefore starts a new Herdr/Codex session for every
sample. With the same effective PATH as production, three request-to-input-prompt
measurements were 820/686/570 ms, all accepting Ctrl-D and exiting normally. The
initial SSH/channel preparation took 4237 ms and is reported separately. This is
not yet the production broker path and does not claim production concurrency,
backpressure or reconnect acceptance. Prototype sessions were isolated and
cleaned. An earlier invalid sample used a different installed Codex binary and
hit its update dialog; it was excluded rather than counted as a TUI launch.

The current shared-connection build reached warm usable TUI samples of
1053/1107/1133 ms and fresh-connection samples of 4215/4308/4288 ms. The
measurement now requires both the Codex header and input prompt, and all six
samples accepted keyboard Ctrl-D and exited with code zero. Runtime and binary
were already provisioned, as in the original approximately 14-second baseline.
Cold mode establishes a distinct new SSH transport on demand; warm mode uses
the daemon's explicitly preheated connection. These results are intermediate,
not completion of the user's subsequent network-floor requirement.

Additional isolated measurements on the same runtime:

- An already established command channel echoed eight small messages in
  254–264 ms, median 255.37 ms.
- Opening a PTY/exec session through a ready OpenSSH master took 757 ms in the
  traced sample. Client setup requests were sent together, but confirmations
  arrived on separate network round trips.
- Direct guest-side Codex startup with immediate local terminal-query responses
  reached the input prompt in 399/275/275 ms; the latter two queries began at
  approximately 24–25 ms, leaving an approximately 250 ms terminal startup wait.
- A raw Go SSH comparison with and without request replies did not establish a
  consistent gain; it is diagnostic tooling only, not a production replacement.

Fennec's warm broker portion was approximately 15–20 ms. The remaining target
is to validate a persistent command-channel approach instead of paying new SSH
session setup on every launch. An RTT plus guest-startup budget is a hypothesis
to measure, not a claim that the physical minimum has already been reached.

## 2026-11-10: credential and takeover recovery

The original remote Kimi 429 was a five-hour account allowance exhaustion, not
missing synchronized credentials. The gateway log recorded the upstream reason
and a suppression deadline. Canonical provider data and eligible Claude/Codex
login credentials matched between this Mac and the runtime. Local Kimi probes
returned a distinct 403 browser-signature rejection. The configured model was
preserved throughout; no fallback or credential reset was applied.

Credential migration now tracks per-login auth baselines: local-only refreshes
update unchanged remote entries, remote refreshes survive unchanged local input,
and concurrent changes fail explicitly. Synthetic-secret tests cover these
cases. An isolated instance of the actual remote Magpie binary passed
`scripts/tests/gateway-credential-reload.py`: two atomic credential replacements
were used by the same gateway process on successive upstream requests.

Probe failures distinguish 429, temporary transport/server errors, 401, and 403.
Transient routes retry with backoff and publish replacement state while keeping
healthy peer results. The native app schedules another preparation attempt after
the bounded retry round. The SSH recovery integration test verifies a first
rate-limit exit followed by successful configuration of the same selected model.
Selected-but-unready agents no longer report that takeover was never enabled.

The Keychain item trusted the fixed certificate but also retained cdhash
partition restrictions. The separately signed credential helper is cached and
copied without rebuilding/re-signing during ordinary app builds. It validates
the running parent's certificate and Fennec identifier before accessing secrets.
Direct invocation from a shell was rejected with exit 77 and no credential
output. Its SHA-256 remained unchanged across modified-main-app rebuilds, and
credential recovery succeeded after installation and restart. Main-app credential
reads now wait for the bounded helper operation instead of discarding an
authorization result after ten seconds.

Final deployment used a verified staged bundle and the canonical application
path. Signature verification, CLI race tests, focused setup tests, Swift
typecheck, and UI checks passed. The real broker/PTY launch created session t1f
and returned Codex 0.167.0 with only a transient text-free spinner before attach.
A subsequent real cloud Codex execution (t1g) used
`kimi-code/k3.1-pro-highspeed`, returned `FENNEC_READY` without tools, and exited
successfully. Connector state was connected and Codex route configured. OpenCode
still requires its own model selection and was not part of this Codex repair.

These are observed development-build results, not a guarantee that certificate
rotation, helper source changes, Keychain locking, or OS policy changes never
require future authorization. Older acceptance entries below are historical.

2026-11-06: replaced the rejected SSH configuration window with an AppKit transient NSPopover. Native build and ad-hoc signing passed. Accessibility inspection identifies a popover rather than a standard window; visual inspection confirms no title bar, compact rows and Runta orange. Local Claude Code and Codex CLI were both detected without execution. Clicking login issued a real request; production rejected the Fennec client identity with HTTP 400. No successful authorization or takeover is claimed.

Remaining acceptance: register the Fennec client in the Runta API schema and identity service, implement browser authorization/polling/Keychain persistence, then verify manual cloud takeover. Previous SSH preview acceptance does not validate this product flow.

## Custom-rendered popover

Rebuilt with WKWebView bundled HTML/CSS inside NSPopover, 440 × 560 logical pixels. Visually checked on Mac: compact brand toolbar, segmented navigation, rounded agent cards, inline expansion, orange login call-to-action and bottom status bar. Clicked Codex expansion, Sessions and Account tabs, and Account login. Both supported local agents were detected. Takeover stays disabled pending authentication/backend integration. JavaScript syntax check and native compilation passed. No standalone main window or SSH settings remain.

Native bridge accepts only fixed action strings from a local main frame. Web navigation is restricted to file URLs and CSP disallows network content. Credentials are not passed into the web view.

Latest refinement: arrowless borderless panel, animation disabled, 440 × 390 size, Magpie compact metrics and original Claude Code/Codex SVGs. Native build, JS syntax and diff checks passed. Latest visual verification was blocked by ScreenCaptureKit error -3811; do not treat the prior screenshot as validation of this revision.

General settings now includes persistent language selection for Simplified/Traditional Chinese, US/UK English, Japanese and Korean, plus system/light/dark appearance. Fixed the gear action to open preferences rather than account. Native compilation and JavaScript syntax checks passed. Native UI inspection located the Settings button, but the click automation timed out; language switching and persistence still need interactive acceptance.

SSH connector: native Mac smoke check with OpenSSH passed deny-without-execution,
approved stdout and exit code 7, and parent-stdin EOF shutdown. Test approval was
supplied through the private IPC by the harness, not the Popup. Interactive Popup
approval and cloud reverse connection remain unverified/unimplemented respectively.
Removed the duplicate Sessions card heading and count; the tab names the content.

Reverse connector transport: added remote-loopback SSH forwarding, bounded
connections, bidirectional copying and cancellation cleanup. TCP integration tests
passed with race detector on the Singapore dev machine. Device alias generation
and strict host-key config tests passed, including injection rejection. These are
transport components, not yet a deployed reverse connection in the user runtime.
Current live installer state remains the earlier xz failure; gzip fix has not yet
been observed running through the app while credential access is unresolved.

2026-11-06 credential lifecycle diagnosis: process sampling proved the main
thread was blocked in restoreLogin -> prepareRuntime -> SecItemCopyMatching.
Moved credential restoration to a dedicated serial background queue and reuse
the successful token in memory for native API/daemon requests; clear it on logout.
A second process sample confirms the main thread is now in its normal run loop,
while the protected Keychain read remains on com.runta.fennec.credentials.
This fixes UI responsiveness but does not bypass or complete system authorization.
Pi 1.5.0/OpenCode 1.18.18 version detection passed locally; updated cloud installer
requires pinned Node 22.22.0. Production installation of those additions remains
unverified until credentials are available.

Latest production preparation reached `agents_installed` with status
`magpie_running_provider_configuration_pending`: exact CLI version checks and
Magpie's loopback `/v1/models` readiness succeeded in the shared runtime.
App automatically launched `link-runtime`; initial observed state is connecting.
Do not treat this as proven reverse transport or completed Magpie provider setup.
Agent installer no-op test now executes the generated script with a fake npm
that fails on any invocation, proving matching versions skip network installs.

Production connector now reports `connected` for the device-specific Mac alias
through the guest loopback relay (no gateway remote-forwarding policy changes).
The live link-runtime process remains running. This proves provisioning/pairing/
alias setup, not yet a user-approved command from the production runtime.
A separate real OpenSSH integration test, `scripts/tests/relay-ssh.py`, passed on
the Singapore dev machine: SSH traverses the Python relay into the Go approval
server, denial executes nothing, approval returns exact stdout and exit code 7,
and channel half-close completes. Approvals are supplied by the isolated harness.

Connector status correction: persisted `connected` survived app restart despite no
link-runtime process. Production command test timed out during banner exchange
while login restoration was pending. Added startup/exit/logout invalidation plus
app/link PID and timestamp to diagnostic connector state. Credential restoration
now renders an explicit waiting state. Native build and JS syntax checks passed;
never infer current connectivity from a persisted state without checking its PID.

Local forwarding: direct-tcpip handler added for SSH -L/-D, with per-channel
pairing checks, optional approval, connection cap and cancellation cleanup.
Real OpenSSH -L test passed on Singapore: byte transfer plus denial of a new
forward channel on the same SSH connection after removing its paired key.
SOCKS negotiation and production forwarding still require separate acceptance.

Credential migration refinement: an unchanged local credential hash now preserves
remote-refreshed tokens without reporting a conflict. When both local and remote
changed since the baseline, migration still refuses to overwrite. Python receiver
execution tests passed under the Go race suite. Current production state reports
`magpie_credentials_migrated_routing_pending`.

Upstream migration research: local Magpie is 0.1.2902; remote pinned CLI is
0.1.3142. Current upstream `backup --sign-ins` carries plugin auth as well as
eligible subscription records, while a whole restore also changes settings,
profiles and plugins. Do not restore a whole backup blindly. Consult
`internal/provider/signins_carry.go` for account identity and refresh-time merge
semantics before extending model-only migration to plugin-backed subscriptions.

Route shell execution regression: matching gateway-prefixed model readbacks on
all four supported CLIs produce zero config writes; mismatched readbacks produce
exactly four repairs. Passed with race detector. Live model catalog now contains
19 models, including devin/gpt-6-astra-priority, but not codex/gpt-6.1-sol. Thus
Codex's desired route is available while Claude/Pi's desired route remains blocked;
OpenCode still has no explicit local model. Do not substitute fallback models.

Missing-catalog models now receive one bounded, 16-token synthetic inference
probe per model per reconciliation. Only a nonempty successful chat result allows
catalog preflight bypass; user-selected models are never substituted. Shared
Claude/Pi model probes are deduplicated within the reconcile run. Existing SSH
route integration tests still reject failed probes. Go race suite passed. This
revision was built in an isolated validation bundle; production rollout pending.

Live route reconciliation through the same production setup module now succeeds
for Claude Code and Pi on codex/gpt-6.1-sol and Codex on
devin/gpt-6-astra-priority. The missing step was Magpie's provider catalog refresh
after successful inference verification; it is now part of reconcile, once per
provider per run with a bounded timeout. OpenCode remains model_selection_required
because its local selection is default. No substitute model was chosen.

Production inference acceptance after account-level migration and route reconcile:
- Pi 1.5.0: --print --no-session --no-tools --no-extensions --no-mcp, returned
  FENNEC_OK, exit 0, using its configured Magpie route.
- Claude Code 2.1.318: -p --tools "" --no-session-persistence, returned FENNEC_OK,
  exit 0. It emitted an unrecognized_model notice for codex/gpt-6.1-sol but the
  routed request succeeded.
- Codex 0.166.0: --no-daemon exec --ephemeral --skip-git-repo-check --sandbox
  read-only, prompt prohibits tool use; provider magpie, model
  devin/gpt-6-astra-priority, returned FENNEC_OK, exit 0. Reported use of bundled
  bubblewrap because system bubblewrap is absent. No tool execution observed.
These prove three CLI inference paths, not persistent interactive sessions,
OpenCode model selection, or the whole cloud-takeover product flow.

SFTP connector added using pkg/sftp, with an explicit whole-session file access
approval under the current OS user (not a per-path sandbox). Real OpenSSH SFTP
binary upload/download/delete passed in an isolated temporary directory on the
Singapore dev machine. Added to test-connector CI target. Production Mac file
transfer and native permission Popover acceptance remain outstanding.

Herdr production persistence acceptance: launched only the isolated terminal
fennec-persistence-check (t1) on the Fennec cloud computer with --outlive-parent.
Disconnected SSH, opened a new SSH connection, observed the same live process,
sent input through hc send, then hc wait returned exit_code 0 and hc read showed
FENNEC_READY/FENNEC_DONE. Removed that finished test terminal via hc rm t1.
This verifies Herdr lifecycle, not yet an interactive agent launched from Fennec UI.
Added a real renderer-function test preserving working/blocked/done, finished,
unknown and missing statuses distinctly; unknown Herdr statuses remain visible.

Live Fennec Herdr script acceptance: generated LaunchScript from internal/runtime,
launched a fresh labelled Pi terminal in /tmp on the actual user runtime; Herdr
returned t2 live. Disconnected and used generated StopScript with the same
Fennec session ID; returned finished with matching label, exit 0. Removed only
that finished acceptance terminal. No user task was submitted. UI launch remains
separate acceptance. Billing API delegation delivered open PR runta-dev/runta#1792
at 31fd73cb2541fb7071e89bb96d3d46ad01a37384; not deployed or merged.

Production Herdr input acceptance: generated SendScript sent literal
`literal $(not-a-command)` to an isolated Fennec-labelled terminal t4 in the cloud
computer. Herdr returned started=null/status=null/turn=0 for the plain shell,
then wait observed exit 0 and read returned RECEIVED with the exact literal text.
No shell interpolation occurred in the send adapter. Removed the finished test
terminal. Receipt means input submission, not agent work completion.

Production Mutagen acceptance: isolated MUTAGEN_DATA_DIRECTORY and SSH wrappers
with strict host-key checking synchronized a new temporary Mac directory to a
new /tmp/fennec-sync-* directory in the actual user cloud computer. Initial file
arrived; paused synchronization, edited the same file independently on both sides
and created a cloud-only file. Resume/flush propagated the cloud-only file back
and retained both conflicting versions; Mutagen reported the conflict. Terminated
only the isolated test session/daemon and removed only the test cloud directory.
The existing Forge Mutagen daemon was untouched. This tests reconnect after sync
pause, not laptop sleep/network failure or production project registration.

Real Go Mutagen Manager acceptance (cross-built test binary on Mac) passed:
Ensure creates once and reuses the same session, Flush yields Ready, source bytes
arrive, and .env stays excluded. Initial test exposed macOS Unix socket path
length failure in a long testing directory; shortened isolated path passed.
Manager now checks the macOS socket path limit and returns an actionable error
instead of waiting for a daemon connection timeout. No production sync changed.

Production terminal attach acceptance: Fennec attach-session used strict host-key
validation and Herdr attach for labelled test t5. Local PTY displayed
FENNEC_ATTACH_READY, typed hello returned VALUE:hello. Ctrl+backslash detached
with local exit 0. A new SSH connection confirmed the same remote terminal was
still live, then killed and removed only t5. This validates terminal attach/input/
detach and persistence, independently of PATH interception or project transfer.

Launch queue cancellation verification (2026-11-06): the journal now uses a
context-aware serialization gate. A canceled queued request returns without
waiting for the active launch, invoking its launcher, or writing a pending
record. Existing duplicate/restart and unknown-outcome protections remain.
Remote `go test -race ./internal/launcher ./internal/cli` passed on
runta-gcp-singapore in the isolated /tmp/fennec-check.6DbnK9 mirror. The runta-gcp
alias was checked first and failed due to expired gcloud authentication.
This verifies daemon request cancellation, not native logout end-to-end behavior;
the installed macOS app was not rebuilt or restarted for this change.

Connector lifecycle verification (2026-11-06): the native app no longer starts
the SSH listener while signed out. Runtime linking starts it after authorization;
logout closes its parent input, terminates the connector, clears pending
approvals and closes the permission popover. Output callbacks from a previous
connector instance are ignored. `swiftc -typecheck native/macos/Fennec.swift`
and `git diff --check` passed. Native logout/popup interaction still needs live
UI acceptance; the installed app was not replaced by this source check.

The current source was synced to the isolated Singapore mirror and
`make test-connector` passed: real SSH relay deny/no execution, allow/stdout/exit
status, device-scoped Never Ask persistence and pairing revocation, approved
SOCKS5/direct forwarding, and SFTP upload/download/delete. These use isolated
keys and loopback services on the dev machine; they do not prove production
cloud-to-Mac approval UI behavior.

Native connection recovery (2026-11-06): connector and runtime-link process
termination now schedules account-scoped exponential retry (1s to 30s).
Connector termination clears stale approvals and restarts the link against the
new listener; callbacks must match the current Process instance. Logout/quit
cancels scheduled retries. Successful connected events reset the delay.
Swift typecheck and diff whitespace checks passed. A freshly cross-built
darwin/arm64 daemon from the Singapore source mirror was packaged with the
current Swift shell at /tmp/fennec-validation/Fennec.app; compilation and ad-hoc
signing passed. This is build evidence, not a fault-injection runtime test.
The previous connector-status.json claimed connecting, but neither recorded PID
existed when checked; that snapshot cannot serve as current connection evidence.

Live verification (2026-11-06): opened /tmp/fennec-validation/Fennec.app and
confirmed its process exists. Preparation remained waiting_credentials across
subsequent observations and no connected state was observed. Native CUA access
failed with ScreenCaptureKit error -3811; no approval popup interaction was
performed and no Keychain authorization was bypassed.

Using the existing dedicated runtime SSH identity with strict host verification,
the production runtime reported Claude Code 2.1.318, Codex 0.166.0, Pi 1.5.0,
OpenCode 1.18.18, Magpie 0.1.3142 and Herdr Core 0.1.18. Herdr server status
reported a live server with zero terminals. The loopback Magpie models endpoint
returned 28 entries. The executable is herdr-core (hc-core is not installed);
the implementation already uses herdr-core. These observations verify installed
versions/server availability, not model inference or cloud-to-Mac connectivity.

Credential restore timeout (2026-11-06): native restore now has a 10-second UI
deadline and an attempt identity. Timeout, explicit login, logout and quit
invalidate the authentication context; late reads cannot authorize an obsolete
attempt. The Security framework call may still return later, but no longer
keeps the application in waiting_credentials indefinitely. Existing localized
credentials-required text is reused; no Keychain access policy was relaxed.
Swift typecheck and full packaging passed at
/tmp/fennec-credential-validation/Fennec.app. Stopped only the previous /tmp
validation instance and launched this build. Its process remained live and
preparation.json transitioned from waiting_credentials to credentials_required
within the bounded observation. This verifies the native state transition;
successful login and visual popup acceptance remain unverified.

Device login completion (2026-11-06): Keychain update/add now runs on a dedicated
serial background queue instead of the AppKit main thread or the potentially
stalled restore queue. Device authorization and token polling carry an account
revision; obsolete callbacks cannot authorize a different login attempt.
Authorization is published only after successful credential persistence.
Swift typecheck, check-ui and full packaging passed at
/tmp/fennec-login-validation/Fennec.app. No real device authorization or Keychain
write was exercised in this verification. OS credential persistence can still
require user interaction; background execution does not bypass that requirement.

macOS connector protocol acceptance (2026-11-06): ran relay-ssh.py,
never-ask.py, forward.py and sftp.py on the actual Mac using the cross-built
daemon packaged in /tmp/fennec-login-validation/Fennec.app. All passed using
temporary pairing keys/state: denial with no command output, approval with
stdout and exit 7, per-device remembered approval surviving daemon restart,
revoked pairing rejection, SOCKS5/direct forwarding and SFTP file operations.
Extended never-ask.py to send clear_trust over the real parent pipe, wait for
trust_cleared, then deny a new request from the previously remembered device;
this passed on Mac, followed by regrant and pairing-revocation checks.
Tests cleaned up their own listeners and temporary state. Decisions were sent
programmatically through the daemon protocol, not clicked in a native popover;
the transport was isolated loopback, not the production runtime link.

Model readiness gate (2026-11-06): ReconcileRoutes now probes every explicitly
selected model, including models present in the catalog. Catalog presence alone
cannot enable takeover. The existing 16-token, tool-free synthetic request is
deduplicated by model within each reconciliation. Catalog refresh remains limited
to missing models. Remote race tests for setup and CLI passed, including a
catalog-listed model whose inference fails and two agents sharing one probe.
These tests exercise SSH protocol orchestration with a controlled server, not
live provider inference. Agent-specific inference remains a separate acceptance
requirement; the overall preparation state is not upgraded to ready by this edit.

Live model probe reconciliation (2026-11-06): cross-built the current
cmd/reconcile-routes diagnostic on Singapore and ran it on Mac with the existing
dedicated runtime key and strict known-host verification. The diagnostic first
stopped on local OpenCode version detection. Changed only this routing diagnostic
to report that error and continue (the production installation gate is unchanged).
The next completed run returned configured for Claude and Pi at
codex/gpt-6.1-sol and Codex at devin/gpt-6-astra-priority. Under the current
reconcile implementation these results require successful real gateway inference
probes and Magpie model readback. OpenCode remains model_selection_required;
its local version detection also failed. This run did not launch agent CLIs or
read API/Keychain credentials, and did not migrate model credentials.

OpenCode version diagnosis (2026-11-06): the detector selected
~/.opencode/bin/opencode, which exits 0 and prints `opencode v2.1.4`.
/opt/homebrew/bin/opencode independently reports 1.18.18. The parser rejected
the leading v; it now normalizes an optional v before a complete semver while
still rejecting malformed/shell-suffixed tokens. Remote race tests for agent,
setup and CLI passed. A fresh darwin/arm64 daemon run on Mac reported all four
versions without errors, including OpenCode 2.1.4 at the detected path.
Cloud OpenCode was previously verified as 1.18.18, so exact local/cloud parity
is not achieved; executable selection and package availability need reconciliation
before changing the cloud installation. No agent installation changed here.

OpenCode selection/package check (2026-11-06): explicit
PATH=/opt/homebrew/bin:/usr/bin:/bin:~/.local/bin made the fresh daemon select
/opt/homebrew/bin/opencode 1.18.18. The inherited non-login PATH places
~/.opencode/bin first and the daemon correctly selects 2.1.4, matching PATH
lookup in that same process environment. Earlier shell observations used
different environments and do not establish a detector ordering defect.
Direct npm registry metadata requests returned HTTP 404 for opencode-ai/2.1.4
and published version 1.18.18 for opencode-ai/1.18.18. The 2.1.4 executable's
distribution identity remains unverified; do not silently replace it with the
Homebrew version or assume npm can install the same version in the runtime.
Native preparation and launch-time PATH can still differ and need an explicit
executable/version handoff to guarantee parity for intercepted terminal launches.

Launch version handoff (2026-11-06): run-agent resolves the original executable
from the invoking terminal PATH, queries that exact path, and carries its version
through broker/project/session requests. The Herdr launch script checks the
cloud executable before writing the launch marker or creating a terminal.
Mismatch returns a reconcile-required error instead of launching another version.
Remote agent/launcher/CLI/runtime race tests passed; an executed generated-script
test confirms local 2.1.4 versus cloud 1.18.18 creates no Herdr terminal.
Automatic version reconciliation at launch and the older direct cloud-agent/UI
paths are not yet covered; the version field remains optional for those callers.
No production agent installation or native application build changed in this step.

Version handoff boundary checks (2026-11-06): project launch validates the
requested version before invoking synchronization. Added assertions that the
parent broker preserves the terminal version and the mapped cloud launch request
retains it, plus a test that an invalid version reaches neither upload nor launch.
Remote CLI/runtime race tests and vet for agent/launcher/CLI/runtime passed.
This closes request validation/field-loss coverage, not automatic installation
of a different version or native application acceptance.

Agent upgrade preservation (2026-11-06): npm now installs each changed agent
into a fresh private prefix. Only after its executable reports the requested
version are managed entrypoint symlinks replaced atomically. Failed npm runs
and wrong-version outputs clean staging and preserve the previous executable.
Successful prefixes remain so old processes are not broken by immediate deletion.
Version verification accepts the same optional v prefix as local detection.
Remote setup/CLI race tests passed, including executed installer scripts with
controlled npm failure, wrong output and successful activation. No live cloud
package upgrade was performed; parallel upgrade arbitration remains outstanding.

Concurrent installation (2026-11-06): added an agent-scoped flock before active
version inspection, held through staging, verification and activation. Lock wait
is bounded at 180 seconds. An executed-script concurrency test starts two installs
of the same version and confirms one npm invocation; setup/CLI race tests passed
on Singapore. Production runtime reports /usr/bin/flock, util-linux 2.39.3.
This serializes installers only. Different requested versions can still alternate
the active entrypoint, and launch-versus-upgrade coordination remains necessary
before automatic installation is added to session startup.

Versioned launch/upgrade coordination (2026-11-06): launches carrying a version
now acquire a shared lock on the installer's agent lock, with a bounded 180s
wait, and hold it through Herdr launch submission. The exact resolved executable
is version-checked and passed to Herdr instead of a mutable PATH alias. An
executed-script test changes the alias after verification and confirms Herdr
still receives the verified path. Remote runtime/CLI race tests passed. The
logical launch marker retains the original intent so retries remain comparable.
This does not yet install missing versions automatically or isolate mutable
legacy npm installations; new staged version prefixes remain retained.

Launch-time exact-version preparation (2026-11-06): versioned session requests
now invoke the staged/locked agent installer after authenticated runtime ownership
checks and SSH connection, before Herdr launch. Matching installed versions take
the no-network path; unavailable packages stop before attempting a session.
The launch operation has a five-minute deadline when version preparation is needed.
CLI/setup/runtime race tests passed, including installer selection and failure
propagation. No real production install/launch through this authenticated API
path was exercised. Concurrent devices requesting different versions can still
cause a final version mismatch (which is rejected), rather than silent substitution.

Clean-source verification (2026-11-06): the reused dev mirror contained a stale
cmd/session-acceptance absent locally. Created a fresh remote directory
/tmp/fennec-source-validation.9RjB3r and copied current source without .git/build/bin.
Full `go test -race ./...` and `go vet ./...` passed there; its test package list
no longer includes the stale program. Cross-built a trimpath darwin/arm64 daemon
from that snapshot and packaged /tmp/fennec-current-validation/Fennec.app.
Local check-ui, Swift typecheck, diff whitespace checks and packaging passed.
This package contains the current version handoff, staged installs, install locks
and launch-time preparation; it was not launched or substituted for the active app.

Current daemon shell acceptance on Mac (2026-11-06): local-launch.py and shims.py
passed against /tmp/fennec-current-validation/Fennec.app's daemon. Fixed the
test's cwd expectation to compare the canonical macOS temporary path and taught
the fake original CLI --version. Added an actual private Unix-socket receiver:
the real PATH shim/run-agent sent version 0.166.0 and preserved literal arguments.
The receiver deliberately rejects before any cloud launch. Disabled takeover
preserved cwd/stdin/argv/exit status; enabled takeover with no broker failed rather
than running locally. Tests used temporary HOME/PATH and did not edit shell rc.

Production installer reuse (2026-11-06): generated current AgentInstallScript
outputs on the clean-source Singapore mirror, then executed them over strict
verified SSH against the actual Fennec runtime for its existing Claude 2.1.318,
Codex 0.166.0, Pi 1.5.0 and OpenCode 1.18.18. All four scripts exited 0.
Before/after version readback matched; Claude/Codex/Pi retained their configured
Magpie routes and OpenCode remained default. Herdr retained the same server PID
and reported zero live/finished terminals. This verifies the real existing-version
reuse branch, not a new package download/upgrade or terminal launch. The temporary
script-generator source was removed from the clean mirror after use.

Real package install acceptance (2026-11-06): executed the current generated
Codex installer in an isolated temporary home on the production runtime, using
the existing Node toolchain. npm downloaded and installed two packages in 12s;
the activated executable reported codex-cli 0.166.0. A second execution retained
the same resolved staged-prefix symlink, verifying installed-version reuse.
The wrapper removed its own temporary tree on exit. Existing user agent entrypoints
and model configuration were not changed. Codex warned that helper PATH aliases
cannot be created under /tmp; the version command still exited successfully.
This proves real package download/verification/activation, not interactive agent
execution, native login, or production cloud-to-Mac approval.

Remaining real package installs (2026-11-06): ran the current generated installer
for each agent in separate temporary homes on the actual runtime. Claude
2.1.318 downloaded two packages in 18s, OpenCode 1.18.18 three in 30s, and Pi
1.5.0 123 in 39s. All reported the exact requested version, exited successfully,
and a second reconciliation retained the same staged installation prefix. Each
wrapper removed its own temporary tree. Existing agent installations and model
configuration were untouched. Pi emitted a node-domexception deprecation warning;
no installer error occurred. Together with Codex this verifies real download and
activation for all four current installer families, but not OpenCode 2.1.4 or
end-to-end agent sessions through the app.

Preparation state UI (2026-11-06): credentials_required, failed and the terminal
agents_installed stage no longer claim active work through an endless spinner.
They expose login or retry using existing native bridge actions; actual connection,
installation and route configuration retain the spinner. Added a DOM-state test
for busy semantics and action dispatch, wired into check-ui; all UI checks passed.
This verifies rendering logic with a controlled DOM, not the native WebView's
visual layout or real device login. No complete/ready status was invented.

Preparation retry isolation (2026-11-06): native prepareRuntime now rejects
starting another preparation while its current Process is running, even if a
retry action cleared the UI latch. Output and delayed termination callbacks must
match both the account revision and the current Process instance, preventing an
older run from overwriting a newer run's state. Swift typecheck and diff checks
passed. Actual repeated-click/native-process acceptance remains unverified.

Production native denial acceptance (2026-11-06): CUA access recovered. The
running credential-validation app showed the signed-in user and Sessions empty
state. Its live link PID existed, relay loopback ports were reachable, and the
connector state reached connected (earlier opening_tunnel was intermediate).
From the real runtime, `ssh machine` requested
`printf FENNEC_NATIVE_APPROVAL; exit 7`. The native NSPopover displayed that exact
command and Never Ask / Deny / Allow once in the expected order. Clicked Deny;
the popover closed and remote SSH exited 126 with no marker output. This proves
real cloud-to-Mac routing and native denial on that running build. Approval,
Never Ask UI, and the newest packaged build remain unverified. The English main
UI still has Chinese native permission labels, a remaining localization issue.

Native permission localization (2026-11-06): locale.js now publishes its validated
language on load and change to the main-frame native bridge. Swift uses that
language for permission title, scope explanation and all three buttons across
zh-CN, zh-TW, en-US/en-GB, ja and ko. Pending approvals rerender on language change;
Never Ask remains leftmost. Swift typecheck, check-ui and diff checks passed.
This source change is not yet in the running credential-validation build and
multilingual popover layout still requires visual acceptance.

Latest native build launch (2026-11-06): packaged current Swift/UI with the
clean-source daemon at /tmp/fennec-native-validation/Fennec.app. Stopped only
the prior task-owned credential-validation app and launched the new build.
Confirmed its live process and startup diagnostic PID. Credential restore
transitioned from waiting_credentials to credentials_required without a connected
link. CUA capture again failed with ScreenCaptureKit -3811, so new localized
popover rendering is not visually verified. `security find-identity -v -p
codesigning` reported zero valid signing identities. Stable Developer ID signing
is therefore unavailable on this machine as currently configured; no Keychain
access policies were changed and no credential content was extracted.

Preparation/takeover invalidation (2026-11-06): starting preparation stops the
launch broker and clears stale route results. The existing CLI takeover preference
is kept during preparation so intercepted commands fail closed rather than run
locally. Successful preparation resumes only previously enabled agents whose new
route result is configured. Directory chooser and deferred broker restart callbacks
are fenced by account and preparation revisions; a stale chooser cannot grant
projects after logout or a newer preparation. Swift typecheck and diff checks
passed. Native retry/chooser concurrency has not yet been exercised interactively.

Correction to takeover invalidation (2026-11-06): do not remove selected agents
whose route validation failed: publishing that reduced selection would silently
route their next commands locally. Preserve selection, and resume the broker only
when all selected agents have configured routes. Apply the same gate to manual
broker restarts. Switches now reflect selection independently of broker readiness,
allowing explicit user disablement after a failure. Swift typecheck and check-ui
passed; this corrects the earlier note about restoring a subset automatically.

Takeover preference persistence (2026-11-06): native selection and authorized
roots are saved separately in a mode-0600 preference file keyed to runtime ID.
After authenticated preparation returns that same runtime, the app restores
selection; the existing route gate still controls broker startup and the daemon
revalidates directory grants. Normal quit retains CLI routing intent so a stopped
broker does not silently cause local execution. Explicit logout clears saved
preferences and routing selection. Swift typecheck and diff checks passed.
Restart/account-switch behavior still needs native acceptance; runtime scoping is
not a substitute for the API ownership checks that precede preparation.

Sign-in UI and signing preflight (2026-11-06): removed duplicate Sessions login
buttons and credential setup card; retained the header login and compact empty
list. Login feedback now lives outside individual tabs so failures remain visible
without switching to Agents. check-ui passed. Build script resolves an explicit
signing certificate name/fingerprint to exactly one valid identity before changing
the app. An invalid-identity run against a temporary existing app failed without
creating Contents or changing its sentinel. This does not supply the missing
certificate or resolve existing Keychain ACL prompts. Running app was not replaced.

First-launch sign-in state (2026-11-06): errSecItemNotFound now clears preparation
state and shows normal signed-out UI instead of a credential failure. Empty login
feedback hides the shared notice. Added check-ui coverage for one static login
entry, no tab switch on login feedback, busy-button state, authorization and logout
after removal of the duplicate Sessions login button. UI checks, Swift typecheck
and diff checks passed. This does not verify Keychain access or remove OS prompts.

macOS broker recovery acceptance (2026-11-06): cross-compiled launcher tests on
Singapore and executed the resulting darwin/arm64 binary on the Mac. All broker
tests passed: private receipt transport, rejection of public socket directories,
recovery of an abandoned Unix socket and rejection of a second owner while the
first listener remains usable. Tests used isolated temporary directories. Current
native app diagnostic still reports credentials_required/disconnected; this test
does not establish app restart or cloud session recovery.

Runtime selection validation (2026-11-06): EnsureShared now rejects a matching
list entry without an ID and a creation response without the Fennec purpose
marker, in addition to existing authenticated-owner checks. Remote runta/CLI
race tests passed, including multiple matching runtimes, malformed identities,
same-name unrelated runtimes and unmarked creation responses. This is controlled
API coverage, not proof of permanent cross-device uniqueness or live creation
response compatibility; server idempotency retention remains a separate boundary.

Independent agent preparation (2026-11-06, supersedes all-selected-ready gating):
shared services emit readiness separately and start the connector before individual
agents finish. Up to four agents install/probe independently, publishing cumulative
results as each finishes. Agent installation/version-detection failures do not
withhold successful peers. Magpie mutations alone use a cross-process flock.
Broker receives selected intent separately from its ready allowlist; native toggles
can enable configured agents during preparation. Broker-ready rechecks results
arriving during startup. Tests demonstrate slow install and slow model verification
do not delay another agent's configured result. Full Go race/vet and check-ui passed.
Built /tmp/fennec-independent-validation/Fennec.app from the current source and
fresh daemon; not yet launched. Native immediate enablement and real partial-failure
preparation remain to be accepted. Shared services and transport failures can still
stop preparation; this does not claim complete fault isolation or overall readiness.

Independent build activation (2026-11-06): stopped only the prior task-owned
native-validation instance and launched /tmp/fennec-independent-validation/Fennec.app.
Confirmed live PID 9462. Its index.html, panel.js, locale.js and panel.css match
current source byte-for-byte, including removal of duplicate login UI. Credential
restore reached credentials_required; CUA capture failed again with -3811.
No per-agent real preparation or button enablement was observed on this build.
Leave this build stable rather than repeatedly resigning/restarting for UI edits.

OpenCode v2 isolation regression (2026-11-06): test uses the real installer
selection function for OpenCode 2.1.4 and Claude 2.1.318, then verifies the
orchestrator publishes distribution_unsupported only for OpenCode and configured
for Claude. Unsupported OpenCode never reaches model configuration. Remote CLI
race tests passed. Current native app remains credentials_required/disconnected;
the new distribution classification has not been exercised in the running app.

OpenCode v2 follow-up (2026-11-06): Singapore fetched the official v2 installer
successfully (local requests had returned 403). It identifies @opencode/cli and
platform packages; registry metadata confirms 2.1.4 for Linux x64/arm64. Installer
selection now routes 2.x to @opencode/cli. Real isolated installation in the
runtime returned `opencode v2.1.4` after 22s; temporary files were removed.
Official v2 provider docs use providers/package/settings/modelID. A separate
temporary-HOME invocation of pinned Magpie's opencode model selection produced
only a model field, so it does not establish a working gateway provider config.
V2 Magpie provider compatibility and inference remain unverified. No existing
OpenCode model choice or cloud executable was replaced in these checks.

Pinned Magpie schema verification (2026-11-06): corrected the earlier incomplete
empty-provider experiment. In a disposable runtime HOME, added a synthetic
provider with localhost:9 endpoint and dummy key through Magpie's documented
provider command, then selected its model for OpenCode. Magpie 0.1.3142 wrote
top-level model/provider and provider.magpie fields models/name/npm/options.
Official OpenCode v2 docs instead specify providers/package/settings. No model
request was sent, no real credential was copied, and temporary files were removed.
This establishes a configuration schema mismatch requiring adaptation; model
readback alone cannot establish OpenCode v2 inference readiness.

Latest Magpie check (2026-11-06): both official GitHub latest-release endpoints
reported v0.1.3177. Downloaded its Linux CLI on Singapore and verified against
the release SHA256SUMS before execution. In a disposable HOME with actual OpenCode
v2.1.4 installed on PATH, added a synthetic localhost provider and selected its
model. Magpie 0.1.3177 still generated model/provider with npm/options, matching
the older schema. Therefore upgrading alone is not yet evidence of v2 compatibility;
Fennec's pinned version was not changed. The prior real-runtime manual v2-config
experiment ended with upstream SSH disconnect (exit 255), not a passing model-list
result. These checks do not establish v2 inference either way.

Correction: v2 accepts legacy provider configuration (2026-11-06). In a fresh
Singapore temporary HOME with OpenCode 2.1.4, both actual Magpie 0.1.3177 output
(provider/npm/options) and documented v2 configuration (providers/package/settings)
passed `opencode models --standalone` and listed the synthetic Magpie model.
A second test used legacy config and a local HTTP recorder: actual OpenCode run
sent POST /v1/chat/completions with model test/model and surfaced the deliberate
FENNEC_TEST_GATEWAY_REACHED error. This proves legacy schema routing, not successful
provider inference. It contradicts the earlier inference that a schema converter
is necessarily required. Do not add conversion solely because field names differ.
All files and test-only installs were isolated and cleaned; no real credentials
were involved and Fennec's pinned Magpie version remains unchanged.

Native independent-readiness acceptance (2026-11-06): the running independent
validation build was signed in. With the current route results showing OpenCode
installation_failed and Claude configured, clicked Agents and observed Claude's
switch enabled (not disabled). Clicking it opened the project authorization
chooser. Canceled the chooser and confirmed the switch remained off; no project
was granted or synchronized. This proves a failed OpenCode does not disable the
Claude switch after preparation, but not the timing of enablement during preparation
or successful broker/session creation. The chooser message and switch accessibility
label remain Chinese in the English UI and require localization.

Existing runtime migration inventory (2026-11-06): verified guest has one online
CPU and MemTotal 994648 KiB. Herdr reports zero live/finished terminals and no
Fennec workspaces directory exists. Public metadata inventory found 548 KiB of
Magpie configuration, 36 KiB Fennec configuration and 32 KiB SSH state; credential
contents were not printed. Agent state directories also exist and must not be
discarded. Current control-plane code makes vCPUs immutable; checkpoint restore
retains hardware, while the exposed image-build API accepts Dockerfile input,
not a runtime disk export. Thus an in-place memory patch alone cannot meet 2c4g.
No runtime was stopped, deleted, replaced or copied in this inspection.

Fresh 2c4g setup and OpenCode v2 inference (2026-11-06): new runtime
01a1b144-56c1-7fc2-a4ab-cf8d84f8543e reports 2 online CPUs and MemTotal
4020096 KiB. Real version commands returned Claude 2.1.318, Codex 0.166.0,
Pi 1.5.0, OpenCode v2.1.4, Magpie 0.1.3142 and Herdr 0.1.18. Connector
status reached connected. Current per-agent results configured Claude/Codex/Pi;
OpenCode requires local model selection. Grok plugin reports expired sign-in,
which does not invalidate the independently verified routes.
Ran installed OpenCode v2 in a disposable HOME/project against the existing
Magpie gateway using legacy provider/npm/options config, no upstream secrets,
and denied tool permissions. With stdin explicitly /dev/null, run --standalone
returned exit 0 and text FENNEC_OK. An earlier stdin-inherited attempt produced
no verification output and is not counted. Temporary state was cleaned. This
proves v2 real inference via pinned Magpie without a schema converter; native
OpenCode takeover still needs a user-selected model and session-flow acceptance.

New runtime to Mac exec (2026-11-06): issued `ssh machine` from
01a1b144-56c1-7fc2-a4ab-cf8d84f8543e for
`printf FENNEC_NEW_RUNTIME_APPROVAL; exit 7`. The real remote command first
reported waiting for approval, then returned the exact marker and exit 7.
No approval button was clicked by the assistant in this attempt; the native
observation instead showed an already-open project chooser with English copy.
No remembered-trust file exists at the default connector path. Record this as
cloud-to-Mac execution/output/exit evidence, not an observed Allow once click or
proof of its actor. Existing project chooser was left untouched.

macOS shell initialization acceptance: cross-built launcher tests on Singapore
and ran them on the Mac. A real new interactive zsh, with temporary HOME/ZDOTDIR,
resolved `claude` to the managed shim and executed its test marker. Existing rc
preservation/idempotency, ZDOTDIR selection and Bash profile sourcing tests also
passed. User shell configuration was not modified. This is shell hook acceptance,
not proof of an actual user CLI session reaching Herdr or Desktop interception.

Entry forwarding composition acceptance: the real daemon integration test now
covers PATH shim -> managed original entry -> original executable, as well as an
already-running Bash with cached command path. Disabled takeover preserves output
and exit 23; enabled takeover reaches the absent-broker error rather than silently
running locally. agent-versions resolves the original target and reports 2.1.318
without invoking the forwarding chain recursively. Remote race tests and executable
integration passed. No user CLI installation was modified; App entry-install
orchestration and macOS cached-shell acceptance remain outstanding.

macOS existing-terminal entry acceptance: a freshly cross-built daemon passed
entry-takeover.py on the Mac under both Bash and zsh. Each shell started and cached
claude before entry replacement; subsequent calls reached forwarding without
sourcing rc, preserved original output/exit status when disabled, and failed with
broker unavailable when enabled. PATH-shim composition and version detection also
passed. Fixed only the fixture's /var versus /private/var path comparison. Restored
temporary symlinks and cleaned test HOME; no actual installed agent entry changed.

Existing fish process forwarding: the executable integration test passed on
Singapore with fish 3.7.0. The same fish process resolves the CLI before wrapper
installation and invokes it afterward without sourcing config; original output,
exit 23, shim composition, version detection and absent-broker failure all passed.
Fish's default noninteractive stdin buffering stalled the first test driver;
terminated only those test PIDs and used a same-process read/eval loop with a
40-second outer timeout. This is process-level behavior, not interactive PTY or
macOS fish acceptance. Actual user CLI entries remain untouched.

Real non-Git Claude takeover: enabled CLI entry launched request
3732ca1d74f5fd16673703614cbde498 from /tmp/fennec-nongit-acceptance. Herdr t1
ran in its cloud session directory, finished exit 0 and retained
FENNEC_CLOUD_LOOP_OK. Original attach lost the short-task race and exited 11.
Updated attach handles an already-finished owned terminal by reading retained
output and returning its recorded exit code. A freshly cross-built daemon's
reconnect against the SAME session printed FENNEC_CLOUD_LOOP_OK and exited 0;
no model task was replayed. Claude also emitted an unrecognized_model warning.
This validates finished-session recovery, not byte-exact headless stdout/stderr
or the remaining race where a live session finishes during attach itself.

Claude warning investigation: live .claude/settings.json points ANTHROPIC_BASE_URL
at http://127.0.0.1:3425 and maps default Haiku/Sonnet/Opus to codex/gpt-6.1-sol.
Only non-secret fields and environment key names were inspected. Pinned Magpie
also supplies model capabilities/context/output settings. Latest tested Magpie
0.1.3177 uses the same direct provider/model naming in a synthetic-provider test.
Claude --help offers no demonstrated warning-specific compatibility fix. The
previous real inference passed despite unrecognized_model; do not hide the raw
warning or claim a newer Magpie fixes it without a verified request-level test.

Installed-entry short session acceptance: with real Claude entry pointing at
/tmp/fennec-session-ui-validation/Fennec.app and takeover enabled, invoked Claude
-p with tools disabled from the non-Git /tmp/fennec-nongit-acceptance directory.
Request a6f27f78be7d24d04b7123da488d2a4b returned FENNEC_SHORT_SESSION_OK through
the original local invocation and exited 0. No reconnect command was needed.
Claude's unrecognized_model warning remains visible. This validates a real short
non-Git cloud-loop launch/output/exit path, not byte-exact non-PTY stream semantics
or dynamic local-directory synchronization.

Herdr resize acceptance correction: with test terminal t5 still attached through
TerminalPTY -> OpenSSH -> Herdr, resized from 80x24 to 100x30 and requested stty
size inside the target terminal. Its live output was `30 100`, proving resize
propagation. After closing the local PTY, terminal.get still reported live. The
assistant then killed/removed only the ccccc... labelled test terminal. Earlier
200x120 readback after detach was not evidence that resize failed. App/xterm
visual resize remains a distinct unverified step.

Nested ignore integration: actual Mutagen 0.18.1 on Mac excluded src/*.generated,
src/deep/*.generated and src/cache/output using src/.gitignore while retaining
other/keep.generated. Git check-ignore independently confirmed the excluded test
paths. Existing source transfer, future root artifacts, remote temporary-file
exclusion and pause/resume checks also passed in this isolated fixture. This
covers positive root/nested patterns, not negations or live policy replacement.

### 2026-11-07: Cloud agent permission defaults

- Cloud launch now defaults Codex to `sandbox_mode="danger-full-access"` and `approval_policy="never"`; Claude to `--permission-mode bypassPermissions`; OpenCode v2 to `--auto` (explicit denies retained), v1 to `OPENCODE_PERMISSION={"*":"allow"}`. Pi already permits its built-in tools without an approval gate. Local connector approval remains unchanged.
- Remote runtime/CLI/localssh tests passed. Cross-built daemon atomically installed into the currently running validation app, preserving the broker and connector processes.
- Real local `/opt/homebrew/bin/codex exec` -> Fennec broker -> Herdr t10 confirmed Codex 0.166.0 reports `approval: never`, `sandbox: danger-full-access`. Its model-generated Python connected to the device-specific SSH alias endpoint and read `SSH-2.0-Go`; exited 0. Previous socket EPERM is resolved on this path.
- Probe intentionally did not authenticate or read local files; native file-access authorization remains a separate acceptance boundary. Existing sessions retain their original launch policy.

### 2026-11-07: Local read and connector restart follow-up

- Real cloud Codex session `1ddadd30a89db96a8e326b9ce50e1cef` read `/tmp/fennec-local-read-acceptance.txt` through the exact device SSH alias and returned `FENNEC_LOCAL_READ_ACCEPTANCE_20261107`, exit 0. Approval wait was observed; the click actor/action was not observed.
- App restart restored connector without system SSH sharing. Follow-up remote printf returned its marker after an approval wait. This was NOT a successful deny test: it executed, and no UI click was observed by automation.
- Native approval layout now fits content with 12pt vertical insets, 8pt spacing and a 32–100pt command viewport. Build passed; updated popup screenshot remains unverified because computer-use returned noWindowsAvailable.
- Found connector startup gated on model/service setup. Added independent `connector_ready` event after runtime SSH verification, before agent installation/credentials. Native shell uses this readiness independently; legacy shared-services event remains a fallback. CLI/localssh tests passed; new bundled build staged separately pending live acceptance.

- Independent connector build deployed to canonical validation app. Observed new app PID 69560: connector entered connecting during checking_resources and reached connected during restoring_model_accounts, before agent/model reconciliation finished. Same runtime ID retained. This verifies the startup dependency separation live.
- Further deny probe again executed after approval wait while UI automation reported noWindowsAvailable. No deny acceptance claim; stopped repeating UI probes.

### 2026-11-07: Packaged connector permission and cancellation acceptance

Executed `never-ask.py` and `relay-ssh.py` on the currently installed daemon with isolated keys and state. Real OpenSSH verified deny returns 126 without stdout, allow returns expected stdout/exit 7, remembered approval is per device, reset requires approval again, and revoked pairing overrides remembered trust.

Added `connector-cancellation.py` to `make test-connector`. Using real SSH and a shell with a heartbeat-writing child, verified both pairing revocation and parent approval-IPC EOF terminate the active command and child. Heartbeat stopped after client termination; each subprocess was cleaned up. These are actual packaged-service tests with programmatic IPC decisions, not native Popup click acceptance.

### 2026-11-07: Concurrent provisioning conflict reconciliation

Backend source inspected at runta-forge/worktrees/trail: control db/runtimes/create.rs locks provisioning requests by tenant/idempotency key, rejects input mismatch and retired keys. This is source evidence, not proof of the deployed backend revision or cross-device E2E.

Fennec now handles create HTTP409 with one read-only list reconciliation: reuse exactly one matching owner/purpose runtime; reject another owner, no match, missing identity or multiple matches. Never retries POST with a random key. Focused runtime tests passed remotely, covering successful reuse and failure cases. Durable uniqueness across different app versions/resource intents and recreation after deletion still need a backend-level contract; no live runtime was deleted or created in this check.

### 2026-11-07: Integrated build and stale resource correction

Full remote `go test -race ./...`, `go vet ./...`, and local `make check-ui` passed. Corrected launch.go formatting. Built integrated macOS app and verified code signature and exact bundled Magpie/xterm license contents.

Live UI inspection exposed stale Resources/UI in canonical running bundle: old Back to sessions button and empty Loading view remained despite new native/daemon binaries. Confirmed panel.js differed from source. Replaced complete Contents from the verified integrated app and relaunched; this corrects deployment consistency, but terminal render/input acceptance is still pending.

### 2026-11-07: Terminal acceptance blocked by credential restoration

After complete bundle update, current preparation state is credentials_required, with no daemon running. CUA returned ScreenCaptureKit -3811 then cgWindowNotFound, so no login or terminal action was verified. `security find-identity -v -p codesigning` returned zero valid identities. No Keychain ACL or OS authorization was bypassed.

Fixed session detail read's missing-auth/runtime guard to render an error and invalidate stale reads instead of leaving Loading indefinitely. Terminal attach nonzero exit now reports failed connection to the renderer rather than normal end. Native build passed; these follow-up native fixes are staged, not deployed while login recovery is unresolved.

- Login recovery review: login increments accountRevision and cancels restore; save success starts preparation. Added secret-free OSStatus/timeout diagnostics for restore/save, since credentials_required alone cannot distinguish locked Keychain, ACL, or timeout. No concrete root cause claimed from missing signing identity alone.
- Added explicit failed-terminal response when terminal-open lacks auth/runtime or valid dimensions, preserving UUID validation. This prevents the renderer from remaining apparently connected after native rejection. Native build/signature and terminal/login JS checks passed; staged follow-up build not installed yet.
- Live UI inventory reports browser access host unavailable. Current native state remains credentials_required; renewed device authorization and end-user Popup/TUI checks require working interactive access.

### 2026-11-07: Actual credential restoration failure captured

Installed the staged diagnostic native build together with its complete bundle. Unified log queries returned no events; launching that same app executable with stderr filtered only for `Fennec credential` yielded `credential restore failed (OSStatus -60008)`. `security error -60008` reports `Unable to obtain authorization for this operation.` State remains credentials_required and no daemon is running. This proves authorization failure rather than a runtime setup failure; the specific ACL/signing/session cause is not established. No token extraction, Keychain ACL change, or automatic OS approval was performed. CUA still reports cgWindowNotFound.

### 2026-11-07: Live installation drift audit

Rechecked current runtime `01a1b144-56c1-7fc2-a4ab-cf8d84f8543e` over strict known-host SSH, and current local daemon `agent-versions`: Claude 2.1.318, Codex 0.166.0, Pi 1.5.0 and OpenCode 2.1.4 match exactly on both endpoints. Magpie reports 0.1.3142; live SHA256 is 0895e20d8187bbcc70e12ea25a6975ed6b9ffd6fd2d515e8786e421100171f38, matching pinned amd64 checksum. Herdr reports hc 0.1.18. Guest reports 2 CPUs and MemTotal 4020096 kB.

Gateway /v1/models returns 28 entries. Configured routes remain Claude codex/gpt-6.1-sol, Codex devin/gpt-6-astra-priority, Pi magpie/codex/gpt-6.1-sol. OpenCode is still default (not configured); Grok plugin reports expired/missing account login. No model selection or credentials were changed. Version/config/catalog inspection is not a fresh inference acceptance result.

### Renderer lifecycle recovery

Native WebView content-process termination previously closed terminal transport but left the view unrecovered. Added reload with at most two attempts in a rolling minute, without restarting the app, connector or remote Herdr sessions. Latest login message/busy state is replayed when the new document finishes loading; authenticated state uses the existing restore path. Native build and bundle signature verification passed. Crash/reload behavior is not yet live-verified; build staged at /tmp/fennec-renderer-recovery/Fennec.app to avoid interrupting pending device authorization.

### 2026-11-07 09:16: Login recovery observed after notice fix

Live native AX shows Shiqi Mei rather than sign-in, and no empty notice node between navigation and agent list. Full screenshot failed (noWindowsAvailable), so visual acceptance is partial. Preparation reused runtime 01a1b144-56c1-7fc2-a4ab-cf8d84f8543e; connector progressed through pairing/configuring_alias. Claude/Codex takeover selections restored to Cloud computer. Sessions list loaded actual Herdr rows. Opening the dedicated Fennec terminal acceptance row produced a user-change signal then noWindowsAvailable, so no input was sent and terminal render acceptance remains unproven. Automation stopped interacting with the app after the user-change signal.

### Relay stale bridge queue regression

Reproduced using real loopback sockets: 24 connect-and-close device bridges with no agent traffic fill the old 16-entry queue; the next healthy bridge is immediately closed. Updated receive_bridges to discard readable/EOF idle entries on each new connection, preserving live entries. Updated relay.py acceptance to fill stale entries before checking bidirectional bytes and half-close. Mac and remote Linux tests passed; localssh Go tests passed. Existing cloud relay PID 2916 was inspected alive; its running code has not yet been replaced, so live reconnect recovery is not claimed.

### 2026-11-07: Live relay upgrade and process reconnect acceptance

Daemon reconciliation upgraded the verified device relay from PID 2916 (no revision) to PID 59768, revision 1bf3f913a7ee36c5a30bb103265db82d103e2b8aebceca15fb5abe8dc24e75e5. Connector reached connected and cloud agent-port TCP read SSH-2.0-Go from Mac.

Terminated only verified local link-runtime PID 9651. App automatically started PID 11169, progressed connecting/pairing/opening_tunnel/configuring_alias/connected, and reused relay PID 59768. Post-recovery banner read succeeded. Runtime and Herdr were not restarted.

Native Popup displayed exact command printf FENNEC_DENY_ACCEPTANCE. Attempted Deny interaction returned user-change warning; final command result was exit 0 with marker, so actual decision was allow. This is not deny acceptance. Stopped further UI interactions after re-query; programmatic isolated deny coverage remains separate.

### Relay lifecycle regression coverage

Extracted the exact production setup script into relaySetupScript and executed it on Linux in an isolated HOME. TestRelayLifecycle verifies same-revision PID reuse, stale-revision process replacement, and refusal to touch an unrelated live sleep process named by a corrupted state file. The unrelated process remains alive. Focused real-process test passed; test-owned relay processes cleaned up. This supplements live reconnect evidence without restarting the user's app again.

### 2026-11-07: Actual xterm first paint verified

Opened Sessions then dedicated Fennec terminal acceptance through native UI. AX exposed Terminal input focused; screenshot visibly rendered green FENNEC_TUI_READY on the xterm black canvas under the compact title/back toolbar. Also captured Agents screenshot with no blank notice card. Input attempt returned noWindowsAvailable; subsequent remote Herdr t7 read showed only FENNEC_TUI_READY, so input delivery remains unverified (not a pass). No user agent session was typed into.

### 2026-11-07: Native TUI keyboard round trip accepted

Dedicated t7 rendered FENNEC_TUI_READY. Native typeText('fennec-input') followed by Return visibly produced ECHO:fennec-input in xterm. Returned via Back to sessions; cloud Herdr readback confirms same PID 19751, state live, cols56/rows19 and retained echo. Thus typed input, display, and detach preserving remote lifetime are verified. Reopen attempt encountered user-change notification; reattach remains unverified.

Native paste('FENNEC_INPUT_CHECK') + Return yielded ECHO: without text; paste delivery is not accepted and needs investigation separately from the working keyboard path. No user agent session received test input.

### Native clipboard routing investigation

App had no NSApp.mainMenu/Edit responder commands. Added standard Command-X/C/V/A/Z actions through the first responder rather than reading clipboard from a background daemon. Native build passed. Initial installed variant exposed only the app menu (first menu slot is reserved by macOS), so added a separate application menu slot before Edit; corrected build staged. Live session navigation hit noWindowsAvailable before paste verification. Paste fix is not accepted yet; prior typed keyboard round trip remains verified.

### Edit-menu corrected bundle deployed

Installed corrected app-menu + Edit-menu build at canonical app path and verified bundle signature. Native AX now explicitly shows separate Fennec and Edit menu items. Account restored to Shiqi Mei; Claude/Codex cloud takeover preferences restored. Sessions navigation returned user-change notification and remained on Agents after re-query; stopped GUI actions. Actual clipboard delivery is still unverified; menu registration alone is not paste acceptance.
