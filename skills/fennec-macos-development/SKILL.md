---
name: fennec-macos-development
description: Build, sign, restart, and debug the Fennec macOS companion, shared-channel session operations, UI state synchronization, Keychain access, cloud takeover, and CLI launch errors. Use for Fennec macOS development or recurring signing, session latency, and takeover problems; not for unrelated Runta runtime work.
---

# Fennec macOS development

Preserve existing source changes, user credentials, cloud sessions, and unrelated processes. Do not rebuild credentials, reset model preferences, or blindly retry an uncertain launch.

## Reuse the local signing identity

Development builds must use the existing `Fennec Local Development` certificate in the user's login Keychain. Never switch back to ad-hoc signing or generate a new certificate for each build: changing the signing identity can make macOS request Keychain authorization again.

```sh
./scripts/create-development-identity.sh
go build -trimpath -o /tmp/fennec-local-build/fennec-daemon .
FENNEC_DAEMON_BINARY=/tmp/fennec-local-build/fennec-daemon ./scripts/build-macos.sh
codesign -dvv build/Fennec.app 2>&1 | grep Authority
```

The creation script is idempotent. It must return the existing identity rather than importing another certificate. The expected signature authority is `Fennec Local Development`. The packager defaults to this identity; `FENNEC_SIGN_IDENTITY=-` is only an explicit ad-hoc escape hatch, not the normal development workflow.

The first use of a newly imported private key may require the user to allow `/usr/bin/codesign` in the system authorization dialog. Do not bypass the dialog, weaken Keychain ACLs, or allow every application to read credentials. After that one-time authorization, rebuild once more and verify that signing finishes without another prompt.

The signing key and the Runta login token are separate Keychain items with separate ACLs. An existing `com.runta.fennec` token created by an old ad-hoc build can still prompt after the signing identity is fixed. Ask the user to choose **Always Allow** once for the current stable-signed app, then restart that app and verify silent credential restoration. Never claim this migration is complete based only on a successful codesign or a new build without a signing prompt.

Do not replace the certificate merely because `security find-identity -v -p codesigning` reports it as untrusted. A self-signed development certificate is not a Developer ID distribution certificate. Release builds still require a stable Developer ID Application identity and separate notarization/update verification.

## Restart without losing cloud work

Self-signed certificates do not alone stabilize Keychain partition IDs: the login item can retain a cdhash partition even when its trusted-app requirement accepts the certificate. Credential operations now run through `fennec-credentials`, which validates its parent as Fennec signed by the same certificate. Preserve the cached helper binary across ordinary app rebuilds; compare its SHA-256 before/after. Recompiling the helper itself changes the Keychain access identity and can require authorization again. Do not reset credentials or loosen partition restrictions to hide this.

Build into a staging directory, verify the complete signature, stop the running Fennec app, then install the bundle at the existing canonical path. Never overwrite running signed binaries in place. Verify credential restoration after the new app starts; a successful build proves neither Keychain access nor broker readiness.

UI and Go control-plane artifacts must come from the same source snapshot. Never update only the native UI while reusing a Go artifact without its new methods. Packaging runs `cmd/check-control-contract` against UI method calls and writes the signed binary hash to `Contents/Resources/control-build-id`. `fen start` validates the running backend's build ID and replaces stale LaunchAgents serially. Restarting the UI alone does not update the backend; exercise the new interface after installing instead of treating an open app as acceptance.

Stop only the task-owned Fennec application process, then open `build/Fennec.app`. Do not terminate remote Herdr sessions or assume application restart deletes cloud work. Reinstall shims only if their daemon path is stale.

After restart, inspect:

- `~/.config/fennec/preparation.json` for preparation stage and runtime ID.
- `~/.config/fennec/route-results.json` for each agent's actual route status.
- `~/.config/fennec/connector-status.json` for a live connector.
- `fen daemon status` and `fen takeover` for the independent Go service and active takeover. Broker and connector lifecycle now live in that process.

Do not call the app fully ready solely because it launched or the connector is connected.

## Diagnose takeover before launching

Reuse any existing cloud agent binary. Install the publisher's `latest` stable release only when the cloud binary is missing; do not query or upgrade an already installed agent during preparation. Never synchronize, compare or require the local CLI's exact version for a new cloud session. The local executable is only a discovery target. Existing sessions retain their original executable; staged installation and atomic alias replacement protect them from failed first-time installations.

`Waiting for cloud takeover` with an enabled switch can be a stale preference, not an active broker. An agent is eligible only when its route status is `configured`. If it is not configured, the UI must show that reason instead of claiming takeover is pending.

Normal preparation synchronizes credentials and model configuration and verifies configuration readback. Do not send inference probes or use provider quota, temporary rate limits, or model-catalog health to gate TUI startup. Inference probes belong only in explicitly requested diagnostics; model request errors are presented by the agent during actual use.

Use a fresh, valid `provider/model` route when the configured model has actually been removed. Do not silently fall back to local execution or invent a replacement. Verify the resulting route becomes `configured` and that `launch-broker` is running.

## Session operations must reuse the resident channel

One Go daemon manages the user's shared cloud machine. UI and CLI share the control plane. Inspect the actual transport for launch, resume, stop/kill, read, send, and list/refresh; optimizing startup alone is insufficient.

- Normal operations reuse the daemon-owned SSH connection **and resident protocol channel**, with identity, runtime ownership, and host keys already verified. Reusing ControlMaster but executing a fresh `ssh ... sh -s` each time still creates an exec channel and is insufficient.
- Do not repeat `OwnedRunningRuntime → Connect → NewSession` in UI handlers, CLI subcommands, or post-click refreshes. The daemon coalesces verification/reconnection only for first connection, account/runtime changes, or a failed connection. Never remove identity/host-key checks for speed.
- Resident-channel operations are typed, carry request IDs, and have independent replies. Slow sessions must not block peers. Stop verifies the remote `fennec.session` label against the target and confirms completion. Failure or a lost reply must not automatically retry a mutation through another path.
- Confirmed operations update daemon cache and UI directly. Remove a stopped row immediately without another remote list; retain it with an unconfirmed state after failure. Revisions/generations must prevent stale refreshes from resurrecting it.
- Trace Go handler, IPC, remote protocol, and UI callback when changing an operation. New protocol capabilities require compatibility handling and updated running daemon/remote artifacts. Source edits alone do not prove deployment.

Distinguish click/CLI-to-confirmation wall time, resident-channel round trip, remote execution, and confirmation-to-UI-update time. Use labeled test sessions created by the task; never stop user work. Exercise stable/reconnecting error semantics, concurrency, and late refreshes. A warm channel must not create new SSH processes or exec channels. `cmd/profile-stop` measures fresh SSH, resident channel, and actual local CLI paths. State each boundary; transport-only measurements are not UI end-to-end latency.

Recorded incident: the same remote stop took about 6–10 ms; fresh SSH took 4.01–4.36 s; the resident channel took 274–282 ms. The old UI then waited for another full remote list. These are measurements from that run, not fixed guarantees; retain the structural constraints above.

Run `make test-e2e-ops` against the installed paths. Each warm operation sample must be ≤500 ms. Measure cold SSH separately (about 4 seconds, gate 5 seconds). Errors, invalid responses, timeouts, and over-budget samples must exit nonzero. Do not substitute averages, cached lists, or receipts for remote refresh/input-ready TUI, or shorten timeouts to manufacture speed. Failed end-to-end gates leave the performance goal incomplete. Ordinary unit tests do not connect to the user's machine; the explicit live target fails when login/runtime prerequisites are missing instead of skipping and claiming success.

## Product UI and internal diagnostics

### UI copy and controls

- Production UI has no Uninstall Fennec or Delete cloud computer entry or dialog. Both `fen uninstall` and `fen cloud` remain internal clean-macOS test-preparation CLI tools only, never exposed on Settings, Account, or Sessions UI.

- On first initialization, enable Magpie only when it is actually installed; otherwise disable it. Saved choices always take precedence. Configuration/cache remnants are not installation evidence, and a user's explicit off choice must not be automatically reenabled.
- With Magpie off, expanded agent panels show native account and real subscription information regardless of local/cloud location. Missing migratable credentials are normal signed-out state on the agent card, not a global Sessions credential error or ineffective retry.
- With Magpie on, show Magpie model choices separately from native subscription state. Never infer or fabricate plan, usage, or remaining quota when those data are unavailable.

- User-facing UI copy has no sentence-ending periods, including labels, descriptions, tooltips, states, errors, dialogs, and every translation.
- Rewrite multiple sentences as phrases, commas, or separate lines. Normalize sentence periods across languages, including ASCII `.`, while preserving dots in versions, URLs, filenames, code, and verbatim terminal output.
- Example: `Choose any model for your cloud agents`; keep the same concise meaning in localized resources.
- Effort sliders follow Magpie's thin gray track, discrete ticks, and white circular thumb, adapted to Fennec typography, spacing, theme, and keyboard focus. Do not use the browser's thick orange default track.
- Let the UI explain itself: saving models or changing settings updates the original control's selection, disabled state, or local loading state. Do not add global notices narrating the operation, such as a saving-model message.
- Async feedback must not shift layout. Preserve control/container dimensions, spacing, and positions. Reuse reserved icon space for loading; do not insert status rows, add/remove placeholders, or resize buttons as text changes.
- Prevent duplicate saves. Show server-confirmed state after success; restore actionable controls and concise recovery after failure. An unconfirmed choice is not saved state.
- Compare before, in-progress, success, and failure states on the actual UI, checking the whole panel for movement. A static screenshot is insufficient.

Do not turn engineering defects such as `CLI integration needs repair`, raw RPC/daemon codes, or missing interfaces into user tasks. Restored switches, asynchronous preparation results, and actual entry checks are different facts: a missing result is not proof of damage. The daemon deduplicates checks and repairs automatically, with details in private diagnostics. Preserve valid UI data and use loading during normal recovery. Offer actions when the user actually needs to sign in, authorize, or choose a model. Fix the cause and add regressions; deleting error copy alone is insufficient.

## Preserve actionable launch errors

Interactive cloud launches show only a transient single-line spinner with no text on stderr and erase it before attaching the agent. Do not print session IDs, reconnect commands, phase logs or other debug text during a successful launch. Pipes and noninteractive shells stay silent; failures retain a concise actionable error. Preserve terminal raw-mode handoff by clearing the animation before `exec`.

Do not replace broker or project-launch failures with only `launch not confirmed`. Preserve the safe underlying error through `internal/launcher/broker.go` and `internal/cli/launch_broker.go`. Never include API tokens, credentials, or unbounded server responses in these errors.

A pending launch journal entry has an uncertain outcome. Inspect the corresponding cloud session before retrying; do not delete the journal or repeatedly create replacement sessions. If a receipt exists, reconnect to that session.

## Verification

Run focused tests for changed packages and `make check-ui`. macOS lacks GNU `flock`, so Linux-specific setup tests can fail locally; report that boundary rather than treating it as a regression or weakening production locking.

Before reporting success:

1. Confirm the app uses `Authority=Fennec Local Development`.
2. Rebuild and observe no repeated private-key authorization prompt.
3. Confirm the requested agent route is `configured`, connector is connected, and launch broker is active.
4. Exercise a noninteractive command such as `codex --version` through the normal login-shell PATH. Argument validation alone does not prove a cloud launch; an `ls` of the local directory is also not remote evidence.
5. Report any unverified interactive terminal behavior separately.

Standalone `codex --version` is explicitly dispatched locally by `LocalQuery` and does not verify cloud execution. For cloud acceptance use `fennec-daemon run-agent codex --version`, or obtain a real launch receipt through the broker and verify the corresponding remote Herdr session and output.

## Current interaction requirements

- Use `make preview-ui` for visual iteration instead of rebuilding/installing a DMG each time. It reads UI source and uses isolated display fixtures, not real account authorization or performance evidence.
- Account cards are informational once signed in: no Sign in again or account-switching controls. When signed out, Sign in delegates to the native agent through the existing browser-session channel, not an embedded account-management screen.
- Background reads and temporary connection states must not trigger generic failure/Please retry toasts. Fix the cause, retain valid content during recovery, and keep actionable feedback near the affected control.
- Governance and security audit UI must speak in user terms and let the UI explain itself: never expose raw `sandbox-exec` / `Seatbelt` kernel strings, stderr dumps, process exit codes, internal subsystem tags (`EXEC` / `SFTP` / `TCP` / `HOOK`), `[REDACTED]` log tokens, or millisecond debug timings to end users.
- Everyday interactions, including model-catalog loading and Magpie switching, have a 1-second end-to-end budget. Retain the stricter 500ms warm-session gate and measure cold SSH/initial setup separately. Data availability or applied configuration is the endpoint; early receipts, preview fixtures, stale caches, or shortened timeouts do not prove success.
