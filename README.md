# Fennec

**Keep your agent. Leave the laptop.**

Fennec by Runta is a local companion for moving familiar agent loops into cloud-hosted sessions. Keep using your CLI; let an agent prepare the project and approved authorization it needs, verify the handoff, and continue after your device disconnects.

Fennec does not replace your model provider or harness. It handles execution placement, environment preparation, connection, and eventual reattachment.

## Status: foundation, not a cloud launcher yet

The intended product is a resident menu-bar/system-tray application, like Magpie, not a browser dashboard. The current HTTP UI is only a development preview. The next iteration should add a native application shell, a compact tray panel, a separate settings window, and a background connector lifecycle independent of window closure. Remove the marketing-style hero from the application surface. macOS behavior must be verified on a Mac.

Implemented:

- A dependency-free Go CLI and loopback-only HTTP control surface.
- Real PATH-based detection of Claude Code and Codex CLI, without executing either agent.
- Embedded, responsive light/dark UI with Runta's orange accent.
- Connector/runtime interface boundaries and fail-closed handoff readiness checks.
- A preparation skill specifying the intended workflow.

Not implemented: cloud provisioning, SSH/PTY bridging, device pairing, file transfer, secret grants, persistent sessions, billing enforcement, or desktop integration. The UI explicitly labels these limitations. No cloud resources are created and no credentials are accessed.

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

This is an original scaffold, not a Magpie fork. Magpie's Go entry point, `internal/` packages, embedded web surface, and familiar-agent-first experience inform the layout. Native tray packaging is deferred.
