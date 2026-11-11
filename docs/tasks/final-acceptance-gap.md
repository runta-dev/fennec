# Final acceptance gap audit

Scope: every P0 and release gate in `TODO.md`. Status: not complete. This audit
records missing evidence; it does not narrow the goal or check off requirements.

## Current authoritative state

- Source: `cd54c0389dbbfb68b524714a66ac2d25fabae4ab`
- CI: [43081984127](https://github.com/runta-dev/fennec/actions/runs/43081984127), completed successfully for that exact source
- CI covered Linux checks/race tests, build, UI tests, connector tests, and macOS arm64 package validation
- Current installed app: `/Applications/Fennec.app`, version `0.2.11120259`
- Running daemon build: `c4b95ef882b30921de5a0339f62d2d9cbcf74ca69461d7d1410a6bcd8895bc1a`
- Signed candidate upgrade restored authorization and the existing runtime; see [candidate evidence](release-cd54c03.md)
- The current staged local-development app passed signature/control-contract checks but was not installed or launched
- A prior development-signature startup stalled at Keychain restoration; the original production app was restored successfully

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
| Independent cloud deletion | `cloud-deletion.md`, shared RPC/CLI/UI, durable intent and failure tests | Rendered modal/focus/copy plus dedicated disposable runtime through delete/recreate/unknown outcome/restart/account switch |
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

Next live results must be recorded against the tested candidate. The first actual
Magpie switch failed the 1-second gate at 3.53 seconds. Preserve the sample/error and fix the
cause before rerunning; do not replace the metric with a receipt, average, cache
hit, fixture timing, or shortened timeout. All checklist items remain unchecked.
