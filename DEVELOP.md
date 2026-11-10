# Developing Fennec

Build, architecture, signing, and verification notes for contributors.
For product usage and terminal commands, see [README.md](README.md).

## Development

Requires Go 1.24 or newer, Node.js for UI checks, and Xcode Command Line Tools
for the macOS client.

```sh
make build
make check
make check-ui
make test
./scripts/create-development-identity.sh
FENNEC_DAEMON_BINARY="$PWD/bin/fennec" ./scripts/build-macos.sh
open build/Fennec.app
```

`make dev` serves the separate read-only development preview on
http://127.0.0.1:7420. It is not the desktop control client. Ctrl+C stops that
preview server, not remote agent sessions.


## Architecture

```text
macOS menu-bar UI ─┐
                  ├── local IPC ── resident Go daemon ── shared SSH ── cloud sessions
fen CLI ──────────┘                         │
                                     local connector
                                     approved access
```

The Go daemon owns authentication, cached state, runtime preparation, SSH
connections, and session operations. The Swift/AppKit shell presents a bundled
WKWebView and sends typed requests through `ControlClient`. UI restarts do not
own the daemon's lifetime. The Go terminal service carries PTY data and heartbeats;
Herdr owns the remote agent processes independently of terminal attachments.

`internal/controlplane` defines operations and account boundaries;
`internal/account` manages account state and credentials; `internal/runta`
implements the runtime API; `internal/terminalstream` handles multiplexed terminal
frames. `internal/setup` reconciles cloud binaries, credentials, plugins, and model
routes. See [DESIGN.md](DESIGN.md) for the detailed contracts and remaining roadmap.

## Layout

```text
main.go                         CLI entry
internal/cli/                   Commands and lifecycle
internal/controlplane/          Resident daemon operations
internal/account/               Account state and credential access
internal/terminalstream/        Terminal transport and heartbeats
native/macos/                   Swift shell and bundled UI
internal/agent/                 Local harness discovery
internal/daemon/                Read-only development HTTP preview
internal/gui/assets/            Embedded HTML, CSS, JavaScript
internal/connector/             Local execution contracts
internal/runtime/               Cloud session lifecycle and PTY attachment
internal/handoff/               Readiness policy
skills/cloud-handoff/SKILL.md    Agent-guided preparation workflow
docs/                           Design references
.github/workflows/              CI checks
```


## Design references

[DESIGN.md](DESIGN.md) describes the architecture, trust boundaries, and delivery stages. [docs/references.md](docs/references.md) records the Magpie layout reference and the Runta brand source.

Fennec is an independent application. Selected Agent SVGs and compact layout metrics are reused under MIT; see THIRD_PARTY_NOTICES.md. Magpie's Go entry point, `internal/` packages, embedded web surface, and familiar-agent-first experience inform the layout. The macOS development app is available; signed distribution is deferred.


## Local SSH connector (development)

`fennec connector` starts a loopback-only, public-key-authenticated SSH exec server.
The resident Go daemon owns its stdin/stdout approval channel; the UI presents approval requests through local IPC. The daemon pairs a runtime-generated key after validating ownership and maintains
a reconnecting reverse transport with device-specific SSH aliases.
Each exec requests one-shot approval with the exact command and peer fingerprint.
Approval grants execution as the current OS user, not a filesystem sandbox.
SFTP and direct TCP forwarding have their own approval requests. Interactive
local shell/PTY and administrator elevation are not provided.
The parent closes stdin to stop the service. Do not claim general SSH compatibility.

The bundle contains the Go executable `fen` in `Contents/MacOS`, with `fennec-daemon` as a compatibility symlink.
Cross-build that binary for the Mac architecture on the remote dev machine, then
set `FENNEC_DAEMON_BINARY` to its local path when running `make macos`.
`python3 scripts/tests/local-ssh.py` tests deny/allow, stdout, exit status and
parent shutdown using temporary keys and an isolated state directory.

### Signing and credential prompts

Development builds use `Fennec Local Development`; create it once with
`./scripts/create-development-identity.sh`. A fixed self-signed certificate alone
does not prevent Keychain prompts: existing items can also restrict access by
binary cdhash. Credential operations therefore use `fennec-credentials`, a
separately signed helper reused byte-for-byte from `build/credential-helper-cache`
when its source, certificate and architecture are unchanged. It accepts only a
Fennec parent signed by the same certificate, and returns credentials over its
inherited pipe. Credentials never enter logs or WebView state.
The helper's first access to an existing item can require user authorization.
Do not clear the helper cache or re-sign it on ordinary app builds. Changes to
the helper itself may require renewed authorization. Verify credential recovery
after rebuilding the main app; successful signing alone is not acceptance.
Ad-hoc signing remains available with `FENNEC_SIGN_IDENTITY=-`, but can cause
macOS to request Keychain access again after rebuilding. For distribution, set
`FENNEC_RELEASE=1` and `FENNEC_SIGN_IDENTITY` to the same Developer ID Application
identity for every release; release mode refuses an ad-hoc signature. Signing is
not notarization: notarization and installed-update acceptance remain separate.
Set `FENNEC_BUILD_DIR` to validate a build without modifying the running bundle.
Automatic credential restoration runs at most once per process; refreshing the
UI does not retry denied/locked Keychain access. Tokens remain in memory after a
successful read and are cleared on sign-out. System authorization is never bypassed.

Complete macOS packaging requires a freshly built Go executable:

```sh
FENNEC_DAEMON_BINARY=/absolute/path/fennec-daemon-darwin-arm64 \
  FENNEC_BUILD_DIR=/tmp/fennec-validation ./scripts/build-macos.sh
```

The packager refuses to silently reuse a daemon left in an earlier app bundle.
Build the Go artifact from the same checkout, locally on the Mac or by cross-compiling with `GOOS=darwin GOARCH=arm64 go build`. Compile the AppKit shell on the Mac.
