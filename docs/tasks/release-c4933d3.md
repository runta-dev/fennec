# Signed candidate c4933d3 acceptance

Candidate: `0.2.11120939`, arm64, source
`c4933d3689ce2394f2d48ab1257ac53778fddd72`.

- Release CI: https://github.com/runta-dev/errand/actions/runs/43135887733 (success)
- DMG SHA-256: `018b692ce9f7c2f860b98d42875ab63e4057830c07f47adbf41ca599880938d9`
- ZIP SHA-256: `e965c1ff692d2ccdc9933f772b75729e865418e5b5cfdbad2db0e1d77bb7dca0` (verified via `SHA256SUMS`)
- Installed control build: `92df39c93e180f788e2c0913777797c61d221748ad0da0518c400e7fea2736a8`
- Apple notarization: App `033c4657-9dd0-46f0-b2c0-7777ea981154` Accepted, DMG `04ae22c2-ca09-4112-9997-e6315901e888` Accepted
- Stapler validation passed for both DMG and `Fennec.app`; `TeamIdentifier=4J499G24H7`

## Offline detection, retry suppression, and auto-reconnect

- Added `internal/connectivity.Monitor` using non-transmitting UDP routing probes (`1.1.1.1:443` / `[2606:4700:4700::1111]:443`) and wired it through `controlplane.Controller`, `daemonServices`, `SharedConnection`, `terminalChannel`, `fenSessionService`, and `localssh.Link`.
- While offline, background SSH/session/connector loops pause instead of hammering retries every 5s, in-flight setup dials cancel promptly without misclassifying offline loss as `runtime_preparation_failed`, and `Controller.Start` plus resident channels automatically resume when local routing returns.
- Fixed stacked duplicate banners on `Sessions`: `#runtime-panel` stays hidden when `stage` is `offline` or `failed` with zero visible sessions, and `#preparation` suppresses redundant `#runtime-message` refresh-failure notes.
- Verified `offline` (`en-US` and `zh-CN`), `failed`, `connecting`, and recovered states visually in `ui-preview` via `ego-browser`, plus unit tests in `internal/connectivity`, `internal/controlplane`, `internal/cli/offline_test.go`, and `scripts/tests/preparation-state.cjs`.

## Developer-machine upgrade and switch profile

- Upgraded `/Applications/Fennec.app` from `0.2.11120351` (`aa2a983...`, UI PID 45367, daemon PID 45396) to `0.2.11120939` (`92df39c...`, UI PID 84043, daemon PID 84067) via `fen install --replace --destination /Applications/Fennec.app` (`ui_restarted=true`, backup `/Applications/.fennec-update-4021644258/Fennec.app`).
- Account restored `authorized` silently without Keychain prompts; `state.get` reported `"online": true`, `runtime.stage: "ready"`, `broker_ready: true`, and `connector.stage: "connected"` while preserving runtime `01a1b144-56c1-7fc2-a4ab-cf8d84f8543e`.
- Live `cmd/profile-magpie-switch` measurement (`/tmp/fennec-c4933d3-magpie-profile.jsonl`):
  - Switch to Magpie: `2593.955 ms` (down from `3609.950 ms`–`5764.508 ms` in `2f11e5d`; wire wait minus remote execution dropped to ~`798 ms` with `configuration_gzip`, while local `magpie <agent> model` subprocesses took `866.860 ms` and remote route reconciliation took `867.096 ms` — still above the 1s budget).
  - Restore native mode: `609.868 ms` (within 1s budget, routes restored cleanly to Magpie off).
