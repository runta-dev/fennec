# Clean-environment onboarding audit: 2026-11-11

Final end-user acceptance has not passed. This record describes source changes and
isolated test evidence, with later development-machine observations identified separately.
The user chose to finish development before arranging a clean Mac/test account.
The local UTM inventory contained only a stopped Windows VM, not a prepared clean macOS environment.

## Developer-environment dependencies found and fixed

1. `ensureControl` previously compared only build IDs, so moving an app could reuse
   the same binary through an old daemon path and LaunchAgent. It now also checks
   the actual executable path and registration. Stale paths/registrations and
   nonresponsive services are replaced under the startup lock. Compatibility symlinks
   do not cause unnecessary restarts. Regression cases cover moved paths, old versions,
   old protocols without executable paths, missing registrations, escaping, and symlinks.
2. Swift's default deployment target depended on the build host. The old development
   UI/helper had `minos=26.0`; released 0.2.11111055 had `minos=15.0`. Compilation now
   explicitly targets macOS 13.0, Info.plist records that minimum, and package checks
   reject Mach-O minimum versions above 13.0. Helper caches are separated by target.
   The user's active helper and Keychain data were not replaced or reset in this step.

Validation: `go test -race ./internal/cli ./internal/controlplane`, macOS 13 Swift
checking, and `scripts/verify-macos-package.sh` for signed staging
`/tmp/fennec-onboarding-os13/Fennec.app` passed. This was neither a real macOS 13 run
nor a newly published DMG. Logs: `/tmp/fennec-onboarding-os13-build.log` and
`/tmp/fennec-onboarding-os13-verify.log`.

## Initial uninstall implementation

Implemented components:

- `internal/uninstall` defines shared confirmation/execution order: confirmation →
  stop automatic recovery → optionally delete and confirm the cloud runtime → disable
  login startup → stop local services → restore commands → delete Fennec-specific
  credentials → remove local data → remove the app itself.
- `internal/runta/DeleteOwnedShared` rechecks the confirmed runtime's user, owner,
  FENNEC_PURPOSE, and revision. An accepted DELETE is insufficient: observe deletion
  completion or 404. Never automatically retry with a new revision or select another runtime.
- Local plans enumerate only Fennec directories and bundle-ID-verified apps. Development
  archives/helper caches are limited to the current repository's build directory;
  source and native-agent credentials are preserved.
- Managed entries are restored unless another package manager already updated the
  command, in which case the new command is retained. Shell cleanup removes only
  uniquely bounded Fennec blocks, preserving user content, modes, and dotfile symlinks.
- Regression coverage includes local cleanup without cloud deletion, no mutations for
  incorrect confirmation, preserving local recovery after cloud failure, deletion-acceptance
  timeouts, changed ownership/purpose/revision, source/native-auth preservation, external
  command updates, and shell restoration.

`go test -race ./internal/uninstall ./internal/launcher ./internal/agent ./internal/runta
./internal/cli ./internal/controlplane` passed; log: `/tmp/fennec-uninstall-first-batch.log`.
All deletion tests used temporary directories/HTTP fixtures. No real cloud deletion
or local Fennec uninstall was performed.

Outstanding at that stage:

- Shared plan/execute/status APIs, `fen uninstall`, and a safe dry run.
- A dangerous-action modal with cloud deletion off by default and an exact target;
  explicit CLI confirmation parameters.
- Durable uninstall intent, preparation cancellation, prevention of runtime recreation,
  recovery from unknown outcomes, and retries.
- An independent worker that can finish cleanup after removing its own app, without
  terminating unrelated processes.
- Actual login-item/LaunchAgent unregistration, Fennec Keychain deletion, WebKit/UserDefaults
  cleanup, and recorded discovery of custom ZDOTDIR/XDG installation locations.
- Final signed DMG, clean-Mac/new-user/reboot, and independent colleague acceptance.

## Shared uninstall entry, recovery, and commands after app removal

The following were development changes and regressions, not a real self-uninstall or
real cloud-machine deletion. A later user decision removed the production uninstall UI;
see the final section of this record.

- Settings initially gained an uninstall modal, with `fen uninstall` as the auxiliary
  command. Both used the Go preview/confirmation/worker flow. Local cleanup was the
  default; cloud deletion required typing the exact runtime ID. CLI options included
  `--dry-run`, `--yes`, and `--delete-cloud --confirm-runtime <id>`.
- Previews bind installation and source hash and are revalidated before execution.
  Recovery can reconfirm only the same cloud ID/owner, never a replacement runtime.
  A new revision requires a new preview and confirmation.
- Durable intent at `.local/state/fennec-uninstall/active.json` outlives app cleanup.
  It is separate from ordinary settings and prevents automatic machine creation after
  failures/restarts. Preparation is canceled and drained; ordinary mutations pause,
  while reauthentication and uninstall recovery remain available.
- The worker runs from a private temporary directory with signed bytes identical to
  the confirmed app. Cleanup includes the currently registered bundle. Process matching
  is restricted to the current user and exact executable path, preserving the CLI
  caller and worker. Completion is published after local cleanup and marker removal.
- Cloud failure, process interruption, expired credentials, stale previews, and incorrect
  confirmation preserve recovery. Deletion is not automatically resent. UI offered
  sign-in/retry without exposing internal status codes.
- If the app disappears, managed entries/shims can execute the resolved original agent,
  explicitly indicating local execution and preserving arguments, cwd, and exit code.
  Cloud failures while the app still exists never trigger local fallback. Original
  executable discovery skips other Fennec shims to prevent recursion.
- Settings was the primary entry at this stage. Account's separate cloud-computer
  deletion entry remained unfinished; an uninstall checkbox did not prove that feature.

Evidence:

- `/tmp/fennec-uninstall-review-tests.log`: launcher, agent, CLI, controlplane, uninstall,
  and Runta-client race tests passed.
- `/tmp/fennec-uninstall-review-ui.log`: UI/dangerous-confirmation regressions passed,
  including default local-only cleanup, rejecting the wrong runtime ID, fencing stale
  UI replies, and avoiding automatic retries after failure.
- `/tmp/fennec-uninstall-review-vet.log`: Go vet passed.
- Signed staging successfully ran `fen uninstall --dry-run`, generating only a preview.
  No worker, Keychain deletion, service unregistration, app removal, or real cloud DELETE ran.

Real acceptance still needed independent cloud deletion, complete self-uninstall/crash
recovery, system logout/reboot, permissions and multi-copy SMAppService behavior,
background unregistration after dragging the app to Trash, persistent custom-shell
installation locations, and the clean-Mac/final-DMG/real-account/independent-user flow.
Fixtures, source tests, and staging were not final end-user acceptance.

## Separating launch at login from backend startup

Hidden behavior: the daemon plist combined RunAtLoad=true with
KeepAlive.SuccessfulExit=false. Consequently, even when the UI's Launch at login was
off, system login could start the backend and restore an account/cloud connection.
The system `launchd.plist` manual states that SuccessfulExit implicitly requires RunAtLoad.

The fix uses RunAtLoad=false and KeepAlive.AfterInitialDemand=true. Explicit UI/CLI
startup bootstraps and kickstarts the service; abnormal-exit recovery remains after
initial demand. AfterInitialDemand uses the system SDK's `launch.h` key without an
external-tool dependency. A failed native control connection reenters startup while
preserving existing UI data. Uninstall and startup share a lock, and startup rechecks
the durable uninstall marker to prevent UI recovery from relaunching during cleanup.

Real `TestLaunchAgentWaitsForExplicitDemand` passed in about 6.5 seconds:

1. Register a harmless probe under an isolated `com.runta.fennec.test-demand.*` label;
   registration alone must not execute it.
2. Explicit kickstart executes it.
3. SIGKILL the test process and confirm launchd recovery.
4. Bootout and bootstrap again; prior demand must not persist or cause automatic execution.
5. Clean up and confirm the test registration is gone.

Log: `/tmp/fennec-login-policy-demand-recovery.log`. Reproduce with
`make test-e2e-launchd-demand`; non-macOS fails, and ordinary unit tests do not run it.
This was not complete logout/login/reboot or clean macOS 13 acceptance. The probe did
not operate on the user's real Fennec service, Keychain, or cloud computer.

Two missed gofmt changes from the preceding commit were also fixed. Commit `99ee63f`
passed GitHub CI `42964771981`; later commits still need their own CI evidence.

## Expired login and uninstall recovery

Finding: Keychain restoration immediately marked an account authorized. With an invalid
old token, ordinary `Login()` returned that state idempotently, so the recovery sign-in
button did nothing.

Changes and development evidence:

- `account.login` accepts explicit `reauthenticate:true`, used by UI recovery;
  CLI equivalent: `fen login --reauth`. Concurrent authorization still coalesces.
- Persistent old credentials are retained until new authorization succeeds. Denial or
  cancellation does not erase recovery information.
- A 401 from account queries or runtime preparation returns to sign-in with session_expired.
  A 403/5xx does not sign out. Rejection is fenced by both token and epoch, so a late old
  response cannot invalidate a new login, even if a fixture reused the same token string.
- Uninstall drains account writes and cancels incomplete device authorization. Active
  uninstall rejects concurrent login. Reauthentication remains available after failure,
  while the durable marker continues preventing automatic cloud recreation.
- An account change before deletion returns a recognizable recovery error; the dangerous
  target remains bound to the original owner/ID.

Account, controlplane, CLI, and Runta-client race tests passed in
`/tmp/fennec-auth-recovery-final-tests.log`, along with `make check`, UI checks, and
macOS 13 Swift checking. HTTP/in-memory credential fixtures did not modify the user's
real login or Keychain. Real browser authorization, Keychain prompts, cancellation,
and upgrades still require the final signed DMG and a dedicated user.

### Duplicate login while writing Keychain

`Reauthenticate` previously waited for the credential write lock before checking whether
a flow was already authorizing. A repeated click during Keychain saving could wait until
the first flow became authorized and then start another flow. It now reuses the active
flow first and records the request epoch, preventing late requests from overwriting
newer login/sign-out state while they wait.

A controlled-write regression pauses a real device-flow HTTP fixture at token saving.
Repeated requests must immediately return the same authorizing epoch; after releasing
the write, only one authorization exists. Account race tests in
`/tmp/fennec-keychain-coalescing-tests.log` and `make check` passed without a real
Keychain prompt. Preceding commit `b8cbfd2` passed CI `42968246384`.

## DMG installation and distribution permissions

Two real gaps were found:

- Read-only/temporary app locations could attempt backend registration without complete
  installation guidance.
- Released 0.2.11111055 ZIP contents used 0700 for app directories/executables and 0600
  for UI resources. The release script's secret-protecting umask 077 leaked into app
  contents, preventing normal reading/execution by other local users.

Implemented:

- Go installation inspection/copy logic and a pre-UI location check. When needed, offer
  install, choose-directory, and quit actions plus progress. CLI: `fen install --check`
  and `fen install`.
- Publish copies only after signature verification. Reject unrelated same-name apps,
  overlapping paths, and symlink recursion into the source. Attempt rollback after a
  publication failure and preserve concurrently created destinations.
- Keep old bundles in adjacent `.fennec-update-*` directories to protect CLI processes
  still mapping old executables. Uninstall discovery includes verified backups and
  removes only empty backup parent directories.
- Background service, embedded terminal, and restart use a known installed copy whose
  signature/native executable digest match. UI resources remain in its own bundle.
  No reverse engineering of Gatekeeper's original path, quarantine clearing, or disabling
  of system checks was introduced. Reference:
  [Apple App Translocation Notes](https://developer.apple.com/forums/thread/724969).
- Build with explicit public bundle modes and check them during signing/notarization.
  Signing material stays in private directories outside the bundle. Shipped directories/
  executables are 0755; ordinary resources are 0644.
- Add `cmd/check-bundle-permissions`. The release pipeline mounts the final DMG to inspect
  volume-root/bundle permissions, signatures, staples, Gatekeeper, and CLI installation,
  rather than checking only staging.
- `make test-e2e-installation FENNEC_ACCEPTANCE_APP=/path/Fennec.app` uses Go to drive real
  read-only images, signed copies, upgrade backups, and permission checks. Child processes
  use only the system PATH.

Evidence: `/tmp/fennec-install-final-gate-tests.log`,
`/tmp/fennec-install-final-gate-verify.log`, and `/tmp/fennec-install-final-gate-probe.json`.
Isolated copying under restrictive umask/system PATH passed without changing the user's
LaunchAgent. This stage did not install into real Applications, launch new UI/backend,
or access Keychain/cloud resources.

Clean-device acceptance still needed Safari quarantine, real Gatekeeper first launch,
actual installation UI choices/handoff, another OS user, logout/reboot, and full first
use of the final notarized DMG. No new official DMG was published in this stage.

## Native model boundaries and expanded-panel acceptance

- With Magpie off, `native_models` / `native_model_set` use the resident channel.
  UI and `fen model <agent> [model|default] [effort]` share the Go control plane.
- Catalogs come from isolated native-agent caches. Pi lists only configured providers.
  The cloud validates catalog membership/effort and clears inherited cross-vendor
  choices to the agent default without changing local configuration.
- Expanded panels gained a full-width Model control, model icon, and an effort slider
  only where metadata supports it.
- Live inspection found that Codex adding `[tui]` triggered a false synchronization
  conflict. Preserve only cloud-generated TUI/notice/projects additions and valid model
  choices; overlapping provider configuration conflicts still reject overwrites.
- The canonical development app was rebuilt and exercised. Native Codex returned GPT
  choices and displayed GPT-6.1-Sol / low without Kimi; CLI rejected a Kimi choice for
  native Codex. Claude exposed native aliases only. Pi without a connected native
  provider showed Default, not an inherited Magpie model.
- Magpie was temporarily disabled and then restored to its original enabled state.
  Claude/Codex/Pi returned to configured, with broker ready. No inference, user-session
  deletion, or user-session stop was performed.
- Local installation retained the original signed credential helper, SHA-256
  `b0ee08622cd866bf1e1bfd7c5f7f76ba7eca51944364e329a6d47a33e3714d6e`.
  Full bundle signatures verified. This local-only helper reuse was not evidence of
  macOS 13 distribution acceptance.
- `make check`, `make check-ui`, Native setup race tests, and runtime/cli/terminalstream
  race tests passed. Three groups of full setup installation tests still failed locally
  because macOS lacked flock.

Evidence: `/tmp/fennec-native-tests.log`, `/tmp/fennec-model-control-tests.log`,
`/tmp/fennec-native-codex-models.json`, and `/tmp/fennec-models-restored-state.json`.
No new DMG was published at that point; clean-Mac installation/full onboarding remained incomplete.

## Isolating native OAuth from leftover gateway configuration

The next audit found that localhost gateway configuration could still cause
`local_provider_unavailable` despite an existing native OAuth login. Source export,
cloud application, and execution environment now remove proxy endpoint/key overrides
only when a native OAuth identity exists. Without native OAuth, a proxy API key is
never redirected to an official service. Claude OAuth in environment/settings.env is
covered, and another agent's shared environment credentials no longer override native identity.

Regressions cover saved Codex/Claude OAuth, Claude environment/settings OAuth, API-key
identities with stale OAuth fields, old-client exports, cloud TUI preservation, and
cross-agent environment isolation. Native setup race evidence:
`/tmp/fennec-native-identity-tests.log`.

Read-only credential-shape inspection found no saved/environment Claude native OAuth
and an empty Pi native auth-provider list on the development machine. They therefore
remained unavailable with Magpie off; fixture successes cannot stand in for those real
accounts' native login acceptance. The real development package configured Codex with
Magpie off and restored all three agents plus broker readiness when the original
Magpie setting was restored. Evidence: `/tmp/fennec-native-identity-live-off.json` and
`/tmp/fennec-native-identity-live-restored.json`. No inference or user-session stop ran.

Product-native sign-in, dedicated native test accounts, and final-DMG/clean-Mac acceptance
were still required. TODO items remained unchecked; development evidence did not prove
final end-user delivery.

## Native account entry and UI consolidation (development candidate)

Later user decisions changed the product boundary: first initialization enables Magpie
only when it is actually installed locally, otherwise disables it; saved manual choices
always win. Production UI no longer offers uninstall, while `fen uninstall` remains a
clean-macOS test tool. These requirements supersede earlier defaults and uninstall UI
descriptions in this record. All final TODO acceptance items remain required.

Implemented:

- `agent.account.get` / `fen agent account` read native account information separately
  for local/cloud locations, returning only allowlisted fields: account email, auth type,
  and explicit plan name. No tokens, API keys, or inferred quotas are returned. An explicit
  Claude OAuth token does not borrow another login file's email/plan. Missing migratable
  credentials yield a normal signed-out state; one agent does not block others.
- `agent.login.start/finish/cancel` and `fen agent login` share the Go control plane and
  resident protocol channel. An installed native CLI signs in inside a separate cloud
  directory; completion atomically selects that account. Stale/canceled requests cannot
  overwrite identity, and local credential sync cannot overwrite an explicit cloud choice.
  Canceled attempts' temporary credentials are removed after the login process is confirmed stopped.
- The daemon detects completion from public login IDs in the existing session listing,
  without a UI callback or another SSH channel. It persists source-selection hints by
  user/runtime and reconfirms configuration.
- Stable missing-credential states allow preparation reuse instead of repeated installation.
  Actual install failures or unconfirmed credential reads are not cached as success.
- Native mode shows account/subscription information. Magpie mode offers the appropriate
  local/cloud model selector; local choices cannot write cloud state. Save feedback stays
  in the control. Sliders use a thin gray track, discrete ticks, and a white circular thumb,
  and dragging controls are not replaced. UI copy follows the no-sentence-period rule.
- Sessions no longer turns unsigned-in agents into a global environment error. Agent
  results render before preparation state.
- Builds recopy UI resources so removed scripts cannot survive in reused staging bundles.

Development verification included Native setup, CLI, runtime, terminalstream, controlplane,
and uninstall Go/race tests, plus `make check` / `make check-ui`. Full setup still had the
existing macOS flock installation failures, requiring Linux CI confirmation. Evidence:
`/tmp/fennec-onboarding-final-focused.log`, `/tmp/fennec-final-control-tests.log`, and
`/tmp/fennec-scope-cache-tests.log`.

Limitations: native-login subprocess tests used an isolated Go fixture agent, not a real
subscription login. Dedicated accounts must still exercise actual Codex/Claude/Pi login,
cancellation, expiry, and task response. Staging/signature checks do not replace final
CI DMG, clean Mac, reboot, or independent-user acceptance. No real cloud computer was
deleted, and no development-signed package replaced the current official `/Applications` install.
