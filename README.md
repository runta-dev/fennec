# Fennec

**Keep your agent. Leave the laptop.**

Fennec by Runta is a local companion for moving familiar agent loops into cloud-hosted sessions. Keep using your CLI; let an agent prepare the project and approved authorization it needs, verify the handoff, and continue after your device disconnects.

Fennec does not replace your model provider or harness. It handles execution placement, environment preparation, connection, and eventual reattachment.

## Status: foundation, not a cloud launcher yet

The intended product is a resident menu-bar/system-tray application, like Magpie, not a browser dashboard. The current HTTP UI is only a development preview. The macOS companion now provides a native menu-bar entry and titleless popover with local agent discovery. Remove the marketing-style hero from the application surface. macOS behavior must be verified on a Mac.

Implemented:

- A dependency-free Go CLI and loopback-only HTTP control surface.
- Real PATH-based detection of Claude Code and Codex CLI, without executing either agent.
- Embedded, responsive light/dark UI with Runta's orange accent.
- Connector/runtime interface boundaries and fail-closed handoff readiness checks.
- A preparation skill specifying the intended workflow.

Not implemented: cloud provisioning, SSH/PTY bridging, device pairing, file transfer, secret grants, persistent sessions, billing enforcement, or agent desktop integration. The UI explicitly labels these limitations. No cloud resources are created and no credentials are accessed.

## Run locally

Requires Go 1.24 or newer. No Node build or frontend package installation is needed.

```sh
make dev
```

Open http://127.0.0.1:7420. The daemon only accepts a loopback IP address and validates browser host/origin headers. This preview is read-only; it is not a production connector security boundary.

```sh
make build
./bin/fennec agents
./bin/fennec serve -addr 127.0.0.1:7421
make test
make check
```

Ctrl-C stops the local preview server. Closing its browser tab does not stop it, but there is no cloud session to preserve yet.

## Intended experience

The following commands are proposed, not currently executable:

```sh
fennec claude
fennec codex
fennec attach <session>
```

1. Start a new cloud-hosted agent through a familiar local interface.
2. Authorize a connector to selected local project paths and actions.
3. Let the preparation skill stage task-specific files and dependencies, requesting explicit authorization when necessary.
4. Verify required work with the local connector disabled.
5. Only then report **Ready to disconnect**. Later reattach and review or export results.

Before verification, tasks depending on local resources must wait if your computer sleeps. Internal networks, device-bound authentication, hardware, or undiscovered dependencies may still prevent offline continuation. SSH is transport, not a guarantee of process survival.

Credentials are never implicitly copied from login directories. Cloud authorization requires explicit, scoped grants via a supported provider flow. No secret values should enter model context, transcripts, images, or checkpoints.

## Layout

```text
main.go                         CLI entry
internal/cli/                   Commands and lifecycle
internal/agent/                 Harness discovery; future adapters
internal/daemon/                Local HTTP control surface
internal/gui/assets/            Embedded HTML, CSS, JavaScript
internal/connector/             Local execution contract, no executor yet
internal/runtime/               Cloud provider contract, no adapter yet
internal/handoff/               Readiness policy
skills/cloud-handoff/SKILL.md    Agent-guided preparation workflow
docs/                           Design references
.github/workflows/              CI checks
```

## Product boundaries

- Start with one supported CLI adapter; additional CLI and desktop integrations require capability verification.
- Do not hijack unrelated processes, rewrite existing agent configuration silently, or promise migration of a live local process.
- No free cloud credits are assumed. Real cloud execution must disclose resource pricing, separate model fees, and enforce an explicit budget.
- No automatic merging over newer local changes; preserve a transfer baseline and require conflict resolution.

## Design references

[DESIGN.md](DESIGN.md) describes the architecture, trust boundaries, and delivery stages. [docs/references.md](docs/references.md) records the Magpie layout reference and the Runta brand source.

This is a Fennec scaffold, not a Magpie fork. Selected Agent SVGs and compact layout metrics are reused under MIT; see THIRD_PARTY_NOTICES.md. Magpie's Go entry point, `internal/` packages, embedded web surface, and familiar-agent-first experience inform the layout. The macOS development app is available; signed distribution is deferred.

## macOS companion

Run `make macos-run` with Xcode Command Line Tools installed. The development app is at `build/Fennec.app`. Run `./scripts/create-development-identity.sh` once to create its stable local signing identity.

Click the menu-bar icon to toggle a compact, titleless native popover. Clicking outside dismisses it without quitting. The popover discovers Claude Code and Codex CLI in common local installation locations without executing them. No SSH or port configuration is required.

The intended flow is **Sign in to Runta → browser device authorization → automatic agent discovery → manually enable cloud takeover**. After login, Fennec automatically prepares the user-owned shared runtime; agent execution remains opt-in.

Device login reuses Errand's registered `runta_crew` client identity, with `Fennec for Mac` as the device name. The native app opens Runta's verification URL, polls until approval/denial/expiry and saves the resulting token in its own macOS Keychain item. Credentials are never sent into the web view. The daemon provisions the shared runtime through Runta REST, reuses existing cloud agent binaries and installs the latest stable publisher release only when missing, and enables each agent independently when ready. Cloud agent versions are not compared with local CLI versions; existing sessions keep their original executable.

## Terminal session picker

`fen` and `fen attach` open the live cloud-session picker. `fen attach <session-id>`
attaches directly; this is also the command copied by the Sessions panel.
The shell integration installs the `fen` entry alongside the agent shims.
The local broker maintains the session list in memory and refreshes it through
its existing SSH connection. Opening the picker only reads local IPC; it never
opens a new SSH connection. Resume and Kill reuse the same authenticated SSH
master, and fail explicitly if it is unavailable rather than silently creating
a separate cold connection.

Use ↑/↓ or j/k to select, Enter to resume, x to request termination, r to refresh,
and Esc or q to quit. Termination requires Enter/y confirmation; Esc/n cancels.
Exited sessions and internal IDs are omitted from the picker. Noninteractive
callers must provide a session ID. Session ownership labels and pinned SSH host
keys are checked before attachment or termination.
For an exited Codex session, `fen resume <session-id>` can restore the unique
saved conversation associated with that workspace. It reads Codex's local thread
metadata on the cloud computer, never guesses from transcript text, and serializes
restart attempts so concurrent resume requests do not create duplicate terminals.
Ambiguous conversation metadata or an uncertain prior restart remains an explicit
error. The exit footer gives the corresponding `fen resume` command.

The option layout follows Codex's `update_prompt.rs`: numbered rows, a leading
selection marker, subdued supporting text, and a persistent keyboard footer.
The picker has its own implementation and preserves j/k navigation; x is reserved
for termination.

## Local SSH connector (development)

`fennec connector` starts a loopback-only, public-key-authenticated SSH exec server.
The native app owns its stdin/stdout approval channel. The daemon pairs a runtime-generated key after validating ownership and maintains
a reconnecting reverse transport with device-specific SSH aliases.
Each exec requests one-shot approval with the exact command and peer fingerprint.
Approval grants execution as the current OS user, not a filesystem sandbox.
SFTP and direct TCP forwarding have their own approval requests. Interactive
local shell/PTY and administrator elevation are not provided.
The parent closes stdin to stop the service. Do not claim general SSH compatibility.

The app expects a Go binary named `fennec-daemon` in `Contents/MacOS`.
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

Complete macOS packaging requires a freshly cross-built daemon:

```sh
FENNEC_DAEMON_BINARY=/absolute/path/fennec-daemon-darwin-arm64 \
  FENNEC_BUILD_DIR=/tmp/fennec-validation ./scripts/build-macos.sh
```

The packager refuses to silently reuse a daemon left in an earlier app bundle.
Build the Go artifact from the same checkout on the remote dev machine using
`GOOS=darwin GOARCH=arm64 go build`; compile the AppKit shell on the Mac.
