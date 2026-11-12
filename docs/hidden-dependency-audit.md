# Hidden dependency audit

This audit enumerates every local path, environment variable, external binary,
macOS system service, and cache used across Fennec's lifecycle. Production
runtime behavior does not depend on a source checkout, Homebrew, Go, Node.js,
Python, Xcode Command Line Tools, Magpie, developer certificates, or pre-existing
cloud state.

## 1. Local macOS binaries and system services

| Subsystem | Dependency | Supplied by | Behavior when absent or restricted | Verification |
| --- | --- | --- | --- | --- |
| Native UI & tray | `Contents/MacOS/Fennec` (`AppKit`, `WebKit`, `ServiceManagement`) | Bundled in `Fennec.app` (deployment target macOS 13.0) | Runs standalone; rejects read-only DMG, `/tmp`, and `.Trash` locations with an in-app install prompt | `cmd/check-installation`, `internal/installation` tests |
| Control daemon & CLI | `Contents/MacOS/fen` (`fennec-daemon` symlink) | Bundled in `Fennec.app` | Registered on demand via `/bin/launchctl` (`RunAtLoad=false`, `AfterInitialDemand=true`) | `TestLaunchAgentWaitsForExplicitDemand`, `internal/cli` tests |
| Runta Keychain helper | `Contents/MacOS/fennec-credentials` (`Security.framework`) | Bundled in `Fennec.app` with stable Developer ID / CDHash | Validates parent signature; missing token returns `signed_out`; denied/unavailable access returns `credentials_unavailable` without crashing | `internal/account` tests, signed upgrade checks |
| SSH transport | `/usr/bin/ssh` | Base macOS system (`/usr/bin/ssh`) | Paused when `connectivity.Monitor` detects no default route; coalesces handshake and reuses one resident `terminalstream` channel | `internal/connectivity`, `internal/launcher`, `internal/ope2e` |
| Bundle install/verify | `/usr/bin/codesign`, `/usr/bin/ditto`, `/usr/bin/open` | Base macOS system | Used only during bundle installation/upgrade; verifies bundle identity before replacing `/Applications/Fennec.app` | `cmd/check-installation`, `internal/installation` tests |
| Service registration | `/bin/launchctl`, `/bin/ps`, `/usr/bin/defaults`, `/bin/sh` | Base macOS system | Per-user `gui/<uid>/com.runta.fennec.daemon` only; requires no `sudo` or admin privileges | `internal/cli/control_daemon.go`, `uninstall_worker.go` |
| Native Keychain read | `/usr/bin/security` | Base macOS system | Optional per-agent fallback when Claude/Codex stores OAuth in macOS Keychain; exit code 44 (`errSecItemNotFound`) maps quietly to signed-out; denial isolates to that agent only | `internal/setup/native_accounts_test.go` |
| Launch at login | `SMAppService.mainApp` (macOS 13+) | macOS `ServiceManagement` | Surfaces `Allow in System Settings` button if background approval is required; never blocks app use | `native/macos/PlatformAdapter.swift` |
| Optional Git sync | `git` + `/usr/bin/xcode-select -p` | User-installed (only for explicit `fen sync`) | Normal cloud sessions use `AutoProjects: true` (`CloudWorkspace`) without Git; if `/usr/bin/git` is an unconfigured Xcode stub, `xcode-select -p` fails cleanly without triggering macOS's GUI installer popup | `internal/project/manifest.go`, `internal/syncengine/gitignore.go` |
| Optional Magpie | `/Applications/Magpie.app` or `magpie` in `PATH` | Optional third-party local AI gateway | Enabled on first initialization only when an executable binary is verified; otherwise off by default. Saved user choices always win | `internal/setup/local_magpie_model.go`, `internal/cli` tests |
| Optional local agents | `claude`, `codex`, `pi` on Mac | Optional user-installed CLIs | Cloud support for `claude`, `codex`, and `pi` remains available even when zero local agent CLIs are installed on the Mac (`Not installed` badge locally, full cloud takeover and browser `Sign in` available) | `TestCleanMacKeepsCloudAgentEntryPoints` |

## 2. Cloud sandbox dependencies (remote Linux runtime)

| Subsystem | Dependency | Supplied by | Behavior on fresh runtime | Verification |
| --- | --- | --- | --- | --- |
| Guest capacity check | POSIX `/bin/sh`, `awk`, `/proc/meminfo`, `getconf`/`nproc` | Base Linux container (no `python3` required) | Verifies >= 2 vCPUs and ~4 GiB before setup; fails closed with clear message if under-provisioned | `internal/setup/capacity_test.go` |
| Remote protocol server | `~/.local/fennec/bin/fennec-terminal` | Uploaded from `Fennec.app/Contents/Resources/fennec-terminal-linux-{amd64,arm64}` | Statically linked `CGO_ENABLED=0` Go binary; verified by protocol version and installation fingerprint | `internal/setup/terminal_binary.go`, `internal/terminalstream` |
| Node.js runtime | `~/.local/fennec/node/bin/node` (`v22.22.0`) | Installed and SHA-256 verified by `NodeSetupScript` | Downloaded into user home without `sudo` or `apt`; reused across restarts | `internal/setup/toolchain.go` |
| Search tool (`fd`) | `~/.local/bin/fd` (`v10.2.0` musl or system `fd`/`fdfind`) | Linked or SHA-256 installed by `NodeSetupScript` & `ensureCloudSearchTools` | Exposed on `PATH` so isolated `PI_CODING_AGENT_DIR` / native logins never fail or re-download `fd` | `internal/setup/toolchain.go`, `native_login_test.go` |
| Session multiplexer | `~/.local/bin/herdr-core` (`hc 0.1.18`) | Installed and SHA-256 verified by `HerdrSetupScript` | Supervises persistent agent PTYs across UI restarts and network drops | `internal/setup/herdr_test.go` |
| Cloud agent CLIs | `~/.local/fennec/agents/bin/{claude,codex,pi}` | Installed on demand via `npm` + POSIX `ln -s`/`mv -f` atomic symlink activation (no `python3` required) | Existing cloud binaries are reused; missing ones install latest stable into versioned staging directories | `internal/setup/install_test.go` |
| Outbound TLS CAs | `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, proxy env vars | Container environment + `/usr/local/share/ca-certificates/runta-egress.crt` fallback | Preserved in both `ExecNativeAgent` and isolated `NativeLoginCommand` without leaking provider API keys | `internal/setup/native_login_test.go` |
| Cloud Magpie gateway | `~/.local/bin/magpie` (`v0.1.3142`) | Installed only when user enables Magpie | Never downloaded, started, or probed when Magpie is off | `internal/setup/reconfigure_runtime_test.go` |

## 3. Local filesystem paths, state, and cleanup

| Path | Purpose | Created by | Behavior when missing / during cleanup |
| --- | --- | --- | --- |
| `~/.config/fennec/ipc/control.sock` | Single control-plane Unix socket (`0600` in `0700` dir) | `control-daemon` | Recreated automatically on `fen start` or app launch |
| `~/.config/fennec/ipc/launch.sock` & `sessions.sock` | Launch broker & session service sockets | `daemonServices` | Recreated when runtime reaches `shared_services_ready` |
| `~/.config/fennec/desktop-settings.json` | Theme, language, and explicit Magpie choice | `desktopSettings` | Initialized on first run (`magpie: true` only if `LocalMagpieBinary` exists) |
| `~/.config/fennec/cloud/` | Ephemeral SSH `runtime_key`, `known_hosts`, and ControlMaster sockets | `runta.PrepareOwnedSSH` | Recreated automatically on connection; private `0700` permissions enforced |
| `~/.config/fennec/ssh/` | Reverse local-connector host key, `device_id`, and command approval broker | `daemonServices` | Generated on first account initialization; bound to authenticated Runta user ID |
| `~/.local/share/fennec/shims/` & `shell-init.sh` | PATH shims (`claude`, `codex`, `pi`, `fen`) and shell hook | `InstallShims` / `InstallShellIntegration` | Shims fall back to local agent if `Fennec.app` is removed; cleaned up by `fen uninstall` or Trash watcher |
| `~/.local/share/fennec/shell-files.json` | Manifest of modified shell startup files (including custom `ZDOTDIR` / `XDG_CONFIG_HOME`) | `InstallShellIntegration` | Read by `uninstall.DiscoverLocal` and Trash watcher so custom rc files are always restored |
| `~/Library/LaunchAgents/com.runta.fennec.daemon.plist` | On-demand background daemon registration | `ensureControl` | Replaced when app path changes; removed on `fen uninstall` or within seconds of dragging `Fennec.app` to Trash |
| `~/.local/state/fennec-uninstall/` | Durable crash-safe uninstall recovery record | `uninstall-worker` | Prevents runtime recreation mid-uninstall; removed upon verified completion |
| `~/.local/state/fennec-cloud-lifecycle/` | Internal CLI cloud-deletion guard (`fen cloud`) | `cloudstate.Store` | Prevents accidental runtime recreation after internal `fen cloud delete` until `fen cloud create` |

## 4. Local environment variables read by Fennec

- **Shell & path discovery**: `HOME`, `PATH`, `SHELL`, `USER`, `ZDOTDIR`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`. Missing values fall back to `os.UserHomeDir()`, `/bin/zsh` on macOS, and `~/.config`.
- **Optional native-agent credential migration** (read via allowlist in `nativeSourceEnvironment`, never logged or exposed in RPC): `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_OAUTH_TOKEN`, `GEMINI_API_KEY`, `GOOGLE_API_KEY`, `GROQ_API_KEY`, `OPENROUTER_API_KEY`, `XAI_API_KEY`, `MISTRAL_API_KEY`, `AZURE_OPENAI_API_KEY`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION`, `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `PI_CODING_AGENT_DIR`.
- **Terminal presentation**: `TERM`, `NO_COLOR`.
