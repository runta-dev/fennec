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

Fennec is an independent application. Selected Agent SVGs and compact layout metrics are reused under MIT; see THIRD_PARTY_NOTICES.md. Magpie's Go entry point, `internal/` packages, embedded web surface, and familiar-agent-first experience inform the layout.


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

## Signed macOS releases

Fennec's release packaging is implemented in `scripts/package-release-macos.sh`.
The **Build Fennec macOS Release** workflow in `runta-dev/errand` runs it using
Errand's existing `apple-release` environment. Apple secrets stay in that
repository; no private key or certificate password is copied into Fennec.

The workflow checks out an exact Fennec commit and verifies that it belongs to
`main` before exposing signing credentials to the packaging step. It produces:

- A Developer ID signed, notarized, and stapled `Fennec.app` inside ZIP and DMG.
- `SHA256SUMS`, notarization results, and `provenance.json` recording the Fennec
  source commit and the Errand Actions run.

Both the original app and the app extracted from the final ZIP pass signature
verification, staple validation, and Gatekeeper assessment. CI also builds an
ad-hoc signed arm64 package in Fennec's regular checks; that package is validation
only and must not be distributed as a release.

To build a signed artifact, dispatch `.github/workflows/fennec-release.yml` in
`runta-dev/errand` with `source_sha` set to the full Fennec main commit. `version`
is optional; by default it uses `0.2.MMDDHHmm` in Asia/Singapore. `CFBundleVersion`
uses the CI run number separately. Download the verified artifact from that run.
This workflow does not publish a GitHub Release automatically.

Required values in Errand's `apple-release` environment:

| Type | Name |
| --- | --- |
| Secret | `APPLE_CERTIFICATE_BASE64` |
| Secret | `APPLE_CERTIFICATE_PASSWORD` |
| Secret | `APPLE_NOTARY_KEY_BASE64` |
| Secret | `FENNEC_SOURCE_DEPLOY_KEY` (read-only deploy key for the private Fennec repository) |
| Variable or secret | `APPLE_NOTARY_KEY_ID` |
| Variable or secret | `APPLE_NOTARY_ISSUER_ID` |

Signing uses an ephemeral CI keychain, which is deleted along with temporary
private-key files on completion or failure. The local development helper cache
and login keychain are not modified by this workflow.

The source deploy key cannot push code. The workflow validates that the checked
out SHA is on Fennec main before the Apple signing step runs. The default Errand
`GITHUB_TOKEN` is not used to access the private Fennec repository.

## Install-location and distribution checks

`fen install --check` inspects the running bundle without starting a daemon.
`fen install [--replace] [--destination /absolute/path/Fennec.app]` copies a
verified bundle and retains the previous bundle for still-running clients.
The native first-launch UI uses the same Go installer. Runtime installation uses
stock macOS tools; it does not invoke the developer Go or Swift toolchain.

Run `make test-e2e-installation FENNEC_ACCEPTANCE_APP=/path/Fennec.app` on macOS
for a disposable read-only image, signature/copy/upgrade/permissions probe with
only system directories in child PATH. This does not replace Safari/quarantine,
fresh-user login, Keychain, reboot, or real cloud-session acceptance.

`check-bundle-permissions` gates public application/volume permissions in the
release pipeline. Signing key material remains private outside the app bundle.
