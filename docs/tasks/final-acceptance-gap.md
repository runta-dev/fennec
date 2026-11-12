# Final acceptance gap audit

Scope: every P0 and release gate in `TODO.md`. Status: not complete. This audit
records missing evidence; it does not narrow the goal or check off requirements.

## Current authoritative state

- Source: `b2a9b9e250b1a920da87637ad5a5bfaffe0b7392` (plus advisory lock cleanup on `cmd/profile-launch`)
- Release CI: [43140973896](https://github.com/runta-dev/errand/actions/runs/43140973896), completed successfully with Apple notarization and stapler verification
- Current installed app: `/Applications/Fennec.app`, version `0.2.11121023`
- Running daemon build: `5d5e703000381f9005e0a61dcdca3ce32802ac4b57b94dc4ac6c7817d31112de`
- Live Magpie switch now passes the 1-second gate (`444.3 ms` switch, `331.9 ms` restore), warm Codex TUI passes the 500ms gate (`342.3–390.1 ms`), and cold SSH Codex TUI passes the 5s gate (`3709.3–3999.6 ms`); see [candidate evidence](release-b2a9b9e.md)
- Offline detection/auto-reconnect (`c4933d3`), hiding `Delete cloud computer` from production UI (`c8fa4ba`), and cloud TLS egress CA + `fd` on `PATH` (`1c17fcf`) are shipped and verified live

CI packaging is not a notarized release, installed behavior, real authorization,
performance acceptance, or a clean-Mac result. Existing developer-machine
credentials and read-only measurements cannot establish fresh-user onboarding.

## Requirement-to-evidence matrix

| Requirement | Existing evidence and source entry point | Evidence still required |
| --- | --- | --- |
| Hidden developer dependencies | `onboarding-audit-2026-11-11.md`, cloud capability independent of local CLI installation, packaging checks | Current final DMG on a clean Mac without source/toolchain/cache, including custom paths/environment and absent local tools |
| DMG installation | `internal/installation`, `cmd/check-installation`, macOS CI package check | Current release signature/notarization/hash, default quarantine/Gatekeeper, Applications drag, spaces/DMG-launch/reinstall paths |
| Runta login | `internal/account`, control-plane tests; old installed daemon currently authorized | Fresh browser/Keychain state, cancel/deny/expire/network/retry/sign-out/account-switch, restart/upgrade credential restoration on current artifact |
| Launch at login | Platform/LaunchAgent implementation and tests | Real logout/login and reboot, disabled/denied/moved app, duplicate/stale service cases on current artifact |
| Native credential migration | `internal/setup/native_accounts*`, native account/login fixtures | Actual Codex/Claude/Pi file/env/Keychain/custom-dir accounts, denial/expiry/refresh/conflicts, isolated failures, task response |
| Unsigned-in agents | Browser sign-in path, retry/pending-state tests, no global credential blocker | Real vendor authorization/cancel/expiry with final UI, successful task after login without developer help |
| Magpie defaults and controls | Installed-only default, saved-off preservation, conditional settings writes, native catalogs, resident reconfiguration tests | Final-artifact fresh installed/absent gateway cases, switching preservation and UI/model timing |
| First cloud takeover | Shared-machine provisioning and bundled remote helpers, source/fixture tests | New dedicated Runta account with no machine reaches an input-ready agent TUI and completes a task |
| UI/CLI parity | Shared Go handlers, browser sessions, CLI commands and control-contract checks | Fresh login-shell command discovery, original commands, takeover on/off, CLI with UI closed on current installed candidate |
| Session lifecycle | `internal/ope2e`, resident transport/control tests, prior historical measurements | Current-artifact create/attach/resume/input/read/list/stop, network loss/sleep/wake/reconnect/cancel/abnormal terminal exit with no duplicate or lost work |
| Upgrade/reinstall/uninstall | `internal/installation`, `internal/uninstall`, recovery records and fixtures | Current signed old-to-new upgrade, move/rename, real clean-device cleanup, shell/LaunchAgent restoration, preserved remote work |
| Internal CLI cloud deletion | `cloud-deletion.md`, internal `fen cloud` / `fen uninstall --delete-cloud` CLI (hidden from production UI per product requirement) | Dedicated disposable runtime through CLI delete/recreate/unknown outcome/restart/account switch |
| UX and performance | Source UI tests, text-free terminal path, fail-nonzero profilers | Rendered pending/success/failure layouts, cold TUI <=5s, every warm session op <=500ms, ordinary UI ops <=1s through completed usable state |
| Final release evidence | Prior release and current ordinary CI are separate | Exact current signed/notarized DMG, source/hash/CI/macOS/initial-state/steps/outcomes/timing/evidence per scenario |
| Independent-user acceptance | No current evidence | Colleague completes full final-artifact flow unaided and records feedback |

## Required external inputs and boundaries

1. Live candidate path: the goal already authorizes a final CI-signed release.
   [Release run 43082833532](https://github.com/runta-dev/errand/actions/runs/43082833532)
   was dispatched for the source above. This prepares the production-signature
   alternative without inferring approval of a developer Keychain prompt. Verify
   artifacts and signing identity before any installation; the run itself is not
   release or user-path acceptance.
2. Dedicated Runta and native-provider test accounts, including a disposable cloud
   runtime for destructive cases. Never use the user's working runtime as a deletion fixture.
3. A clean Mac user, VM, or independent device where actual login/reboot and final
   DMG install/uninstall are permitted. A changed HOME directory is not equivalent.
4. Real user interaction for system/vendor authorization and final unaided colleague acceptance.

The development preview at `http://127.0.0.1:56075` is isolated fixture data. Its
functional tests and source delivery are verified; rendered visual acceptance
has not been claimed. Browser task-space ownership must be respected during that
remaining check.

Next iteration stays in fast local development mode (`build/Fennec.app` and
`ui-preview`) per user instruction until ready for final clean-Mac DMG
acceptance. All checklist items remain unchecked until verified on a clean Mac
with dedicated test accounts and final unaided user onboarding.
