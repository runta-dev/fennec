# Fennec: seamless end-user experience

Goal: a user downloads the official DMG and independently signs in, configures
agents, and uses cloud agents on a Mac without source code, developer tools,
development certificates, Magpie, old account state, or a prepared cloud machine.
The product provides actionable recovery rather than requiring developer guidance,
knowledge of internal state, repeated retries, or manual terminal repair.

Status: incomplete. Green CI, a working development machine, and isolated Linux
fixtures are not clean-Mac acceptance with a real account and the final DMG.
Do not check off items without verification.

Current requirement-by-requirement evidence gaps are recorded in the
[final acceptance audit](docs/tasks/final-acceptance-gap.md).

User clarification on 2026-11-11: finish development before arranging clean-device
acceptance. Also develop `fen uninstall` for test cleanup of development and DMG
installations. Default to local cleanup. An explicit option may delete the entire
cloud runtime, with a dangerous-action confirmation describing all sessions,
files, and data affected and binding the exact target. Confirm cloud deletion
before removing local credentials and the app; preserve recovery when deletion
fails or its outcome is unknown. Never delete the user's real runtime during tests.

Later user clarification: the production UI must not include an Uninstall entry
or dialog. Keep `fen uninstall` for clean-macOS test cleanup. Local cleanup keeps
the cloud computer by default, and cloud-computer deletion (`fen cloud delete` /
`fen uninstall --delete-cloud`) must also stay hidden from the production UI as an
internal clean-macOS test-preparation CLI tool only. Also cover dragging the app
to Trash: removing the app must not break original agent commands, and cloud
failures must never be mistaken for permission to run locally. Track the CLI cloud
deletion safeguards and remaining disposable-runtime verification in
[cloud deletion](docs/tasks/cloud-deletion.md).

Development evidence and outstanding work are in the
[onboarding audit](docs/tasks/onboarding-audit-2026-11-11.md).

## P0 development and acceptance

- [ ] **Hidden dependency audit**: enumerate paths, environment variables, system
  services, and caches used by installation, login, background startup, CLI,
  credentials, cloud preparation, sessions, upgrades, and cleanup. Record what
  the product supplies, behavior when missing, fixes, and tests. Runtime behavior
  must not depend on a source checkout, Homebrew, Go, Node, Magpie, developer shell
  configuration, or leftover developer services. Source audit recorded in
  [hidden dependency audit](docs/hidden-dependency-audit.md); keep unchecked until
  clean-Mac final-DMG verification completes.
- [ ] **First installation from the official DMG**: download from CI release links;
  verify signing, notarization, quarantine, and Gatekeeper. Drag into Applications
  and launch without developer certificates or helper caches. Cover spaces in
  paths, reinstalls, and accidental launches from the DMG.
- [ ] **Runta login**: complete login without existing Keychain tokens, browser
  sessions, or an old daemon. Recover from cancellation, denial, expiry, timeouts,
  network interruption, retries, sign-out, and account switching. Reuse credentials
  appropriately across restarts/upgrades without unnecessary repeated prompts or
  cross-account data reuse.
- [ ] **Launch at login**: product-owned service registration and controls reflect
  real system state. Actually log out/in and reboot, covering disabled startup,
  system denial, moved installation paths, stale services, and duplicate processes.
  A launchctl configuration entry is not sufficient acceptance evidence.
- [ ] **Native agent credentials**: without Magpie, cover files, supported environment
  variables, Keychain, and custom directories for publicly supported cloud agents
  such as Codex, Claude Code, and Pi. Preserve model choices; handle denied access,
  missing/expired credentials, cloud refreshes, and conflicts. One agent's failure
  must not block others. Keep credentials out of logs, argv, and public responses.
- [ ] **Users not signed in to an agent**: provide an actionable sign-in/configuration
  entry and continue automatically or with one recovery action. Missing-credential
  text and an ineffective Retry button are insufficient.
- [ ] **Magpie initialization and saved choices**: follow the later product decision
  recorded in the audit: enable on first initialization only when actual local installation
  is verified; otherwise keep it off. Preserve saved choices across upgrades. When off,
  do not install, start, or probe the gateway. UI and CLI share enable/model/disable
  controls, and switching preserves existing sessions.
- [ ] **First cloud takeover**: a new Runta account with no cloud machine can prepare
  its shared sandbox, binaries, credentials, and routes automatically. No preinstalled
  tools, old SSH channel, or preparation cache may be required. Selecting Cloud
  reaches a real input-ready TUI that can respond to a task.
- [ ] **CLI/UI consistency**: in a normal fresh terminal, verify command discovery
  and real invocations. Cover shell/PATH, original agent commands, takeover controls,
  fen operations, and continued use after closing the UI. Do not intercept unselected
  commands, leave developer-path shims, or delegate repair to the user.
- [ ] **Full session lifecycle**: create, attach/resume, input, read/list, and stop.
  Update UI promptly and reuse resident connections/channels. Network loss, sleep,
  wake, reconnect, cancellation, and abnormal exits must neither duplicate uncertain
  launches nor lose existing work or echo mouse/terminal control sequences.
- [ ] **Upgrade, reinstall, and cleanup**: upgrade the previous official DMG, move or
  rename the app, and reinstall while preserving UI/helper/daemon consistency,
  credentials, and sessions. Disabling takeover or uninstalling restores local
  commands without broken LaunchAgents/shims. Use dedicated test users/environments
  and preserve developer work.
- [ ] **Interaction and performance**: text-free terminal loading; UI states and direct
  controls provide guidance without internal diagnostics. Record actual wall time for
  first preparation, cold connections, and warm sessions. Enforce the agreed 5-second
  cold-TUI and 500 ms warm-operation gates separately, describe conditions, and fail
  nonzero above budget. Machine installation time, cached receipts, and hidden prewarm
  must not be substituted for the promised metric. Failed items remain incomplete.

## Release gates and evidence

- [ ] Maintain a clean-environment matrix: fresh users, existing native-agent logins,
  users without agent logins, and upgrades from the previous official DMG. Verify
  Magpie is off when absent, enabled on first initialization when actually installed,
  and never reenabled over a saved off choice. Also test explicit opt-in separately.
- [ ] Use repeatable Go tools/tests for the real installed paths, with dedicated accounts,
  sandboxes, temporary credentials, and labeled sessions. Missing conditions must fail
  or be recorded as unverified, never skipped and reported as a pass.
- [ ] Validate the final signed DMG on a clean Mac user, VM, or independent device, using
  product defaults and real system services. Changing HOME, fixtures, or existing
  developer-machine state does not replace this acceptance.
- [ ] For each result record DMG version/SHA-256, source SHA, CI run, macOS/architecture,
  initial state, steps, expected/actual results, duration, sanitized logs/screenshots,
  and remaining issues.
- [ ] After onboarding/use blockers are fixed, rebuild the signed/notarized DMG and rerun
  the full matrix from the download entry. Source tests do not replace package acceptance.
- [ ] A colleague or independent user completes the flow without developer hints; record
  feedback and evidence in the acceptance report.
- [ ] Distinguish source/CI, developer-machine, Linux-fixture, clean-Mac, and real-user
  evidence. Declare completion only when every required item has direct final-artifact
  evidence. Do not hide untested scenarios or residual issues.

Existing implementation and historical evidence:
[native credentials](docs/native-agent-credentials.md),
[macOS acceptance](docs/macos-acceptance.md), and
[control plane/performance](docs/tasks/go-control-plane.md), and
[model reads and switch acceptance](docs/tasks/model-read-acceptance.md).
Historical results are not acceptance of a newer build.

## Deferred compatibility: cmux Computer Use

User decision on 2026-11-11: `cmux-cua` is not a current priority; do not implement
or ship it yet.

- [ ] **cmux-cua / Computer Use MCP**: revisit the Mac MCP client, Unix socket, and cloud
  agent integration later. Native MCP and the Codex-specific profile are not strict
  parity: tool schemas, screenshot/accessibility-tree shapes, app approval, and
  signed-parent requirements differ. Agree on the contract before resuming. Never
  bypass cmux authorization through unverified-client or embedded modes. Future live
  acceptance must cover perception/input, macOS permissions, cancellation, disconnects,
  and session isolation.

**cmux hooks are fixed**: the Mac hook paths that caused cloud Codex exit 127 were
repaired and merged into main. Signed/notarized candidate `0.2.11111843` passed real
cmux → cloud Codex → local approval → cmux event-delivery acceptance, with seven
one-shot authorizations, actual tool execution, and Done status. This is separate
from the unsupported cmux-cua MCP capability; deferring CUA does not make the hook
fix incomplete. See [live hook acceptance](docs/cmux-hooks.md#real-release-acceptance-2026-11-11).
