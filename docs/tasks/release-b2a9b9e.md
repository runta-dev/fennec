# Candidate b2a9b9e measurements and fixes

Candidate: `0.2.11121023`, arm64, source
`b2a9b9e250b1a920da87637ad5a5bfaffe0b7392`.

- Release CI: https://github.com/runta-dev/errand/actions/runs/43140973896 (success)
- DMG SHA-256: `ac2ce4bebdde97b6636ec6517282a99ecfd23ebe2cf62ab375edc48593eb7524`
- ZIP SHA-256: `deb69bb6381f05fbc03927d0d981d3bbedd5400e213a8722b0fd28cf5266f095`
- Installed control build: `5d5e703000381f9005e0a61dcdca3ce32802ac4b57b94dc4ac6c7817d31112de`
- Apple notarization: App `2387490f-f9d8-49d6-af3c-ec4b16003a4e` Accepted, DMG `64f3ee18-88e5-49a0-9ba8-106e154f3dcd` Accepted

## Delivered fixes in this series (`c4933d3` -> `b2a9b9e`)

1. **Offline detection and auto-reconnect (`c4933d3`)**: added `internal/connectivity.Monitor` so background SSH/session/connector loops pause while the Mac has no default route, in-flight setup cancels without misclassifying offline loss as `runtime_preparation_failed`, duplicate `Session status unavailable` cards never stack under `#preparation`, and Fennec reconnects automatically when back online.
2. **Production UI hides cloud deletion (`c8fa4ba`)**: removed `cloud-computer.js` and `Delete cloud computer` UI bindings from Account/Sessions and public `fen --help`, retaining `fen cloud` and `fen uninstall` strictly as internal clean-macOS test-preparation CLI tools.
3. **Cloud TLS egress CA and `fd` on `PATH` (`1c17fcf`)**: preserved `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, and proxy environment variables in `NativeLoginCommand` (fixing `unable to get local issuer certificate` inside `Sign in`), and ensured `~/.local/bin/fd` is linked/installed on `PATH` (fixing Pi `fd not found. Downloading...`). Verified live on runtime `01a1b144-56c1-7fc2-a4ab-cf8d84f8543e`.
4. **Magpie switch <= 1s (`b2a9b9e`)**: added direct Go configuration readers for Magpie-routed `codex`, `claude`, and `pi` configs (with CLI fallback) and an mtime-validated cache for `nativeSourceEnvironment`.

## Live measurements on `/Applications/Fennec.app` (`0.2.11121023`)

- **Magpie switch (`cmd/profile-magpie-switch`, 1s budget)**: **PASS** (`exit 0`, `/tmp/fennec-b2a9b9e-magpie-profile.jsonl`)
  - Switch to Magpie: **`444.305 ms`** (`local_prepare_wall: 36.654 ms`, `local_routes: 1.838 ms`, `remote_round_trip: 386.392 ms`, `remote_ms.routes: 0.101 ms`)
  - Restore native mode: **`331.925 ms`** (`local_prepare_wall: 33.306 ms`, `remote_round_trip: 265.154 ms`)
- **Warm usable Codex TUI (`cmd/profile-launch --mode warm`, 500ms budget)**: **PASS**
  - Samples: **`390.063 ms`**, **`342.267 ms`**, **`364.679 ms`** (`spinner_visible: true`, `exit_code: 0`)
- **Cold SSH usable Codex TUI (`cmd/profile-launch --mode cold`, 5s budget)**: **PASS**
  - Samples: **`3709.254 ms`**, **`3888.121 ms`**, **`3999.592 ms`** (`spinner_visible: true`, `exit_code: 0`)
- **Installation bundle check (`cmd/check-installation`)**: **PASS** (`bundle_public_modes_verified: true`, `signed_copy_verified: true`, `upgrade_backup_verified: true`)
- **Session operations (`internal/ope2e`)**:
  - Passed <=500ms: `daemon.ping` (~9.5ms), `connection.status` (~6.5ms), `account.status` (~5.3ms), `agents.list` (~6.9ms), `settings.get` (~5.3ms), `takeover.get` (~5.4ms), `approvals.list` (~4.9ms), `state.get` (~5.1ms), `sessions.list` (~5.1ms), `sessions.refresh` (250–271ms), `sessions.allocate` (11–14ms), `read` (263–290ms on 8/9 samples, one transient 525/554ms WAN sample), `attach_usable` (322–375ms), `resume_usable` (370–441ms), `stop` (332–444ms), and mature `sessions.send` (`472.8 ms`, `475.3 ms` on 2/3 samples).
  - Remaining >500ms in `internal/ope2e`: fresh-terminal `sessions.send` (`679–885 ms`) includes `herdr-core send`'s built-in 10s startup-settle pause (`herdr_send: 407–562 ms` remotely plus ~265ms WAN RTT), and `sessions.launch` had a single `590.7 ms` sample.
