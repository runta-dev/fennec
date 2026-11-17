# Fennec design

Reviewed against source `9553de0` on 2026-11-17. This document describes the
implemented architecture and separates it from remaining product requirements.
It is not installed-build or clean-Mac acceptance. See [the documentation
index](docs/README.md) for implementation references and historical evidence.

## Product contract

**Keep your agent. Leave the laptop.** Fennec runs supported coding agents on
one persistent cloud computer per user, with approved access to the user's Mac.
The Mac application is a connector and control surface, not a replacement agent
harness. Users keep their terminal, editor, tmux, cmux, and Herdr workflows.

Detaching the terminal does not stop the cloud agent. Cloud-independent work can
continue while the Mac is offline; local-dependent work must wait. A live process,
a healthy connection, and a task that can finish without the Mac are different
properties. Enabling cloud execution does not migrate an already-running local
process. Supported resume adapters can import an existing conversation into a
new cloud process.

The target complete workflow remains: prepare a real task, verify its cloud
dependencies, disconnect the Mac, complete the independent work, reconnect, and
reconcile the result without losing concurrent local changes.

## Implemented architecture

```text
Mac terminal / menu-bar UI
            |
     local authenticated IPC
            |
     resident Go daemon
            |
     shared SSH / resident protocol
            |
     cloud terminal service ---- Herdr-managed agent processes
            |
     device relay ---- Mac connector ---- approved local files and tools
```

| Component | Responsibility | Source |
| --- | --- | --- |
| CLI and launch shims | Agent dispatch, session picker, attach/resume/stop, configuration | `internal/cli`, `internal/launcher` |
| Resident daemon | Account state, preparation, connection recovery, shared UI/CLI operations | `internal/controlplane`, `internal/cli` |
| Native shell | Menu-bar panel, terminal presentation, authorization UI | `native/macos` |
| Cloud sessions | Herdr process lifetime, session identity, PTY attachment and operations | `internal/runtime`, `internal/terminalstream` |
| Mac connector | Paired SSH exec, SFTP and forwarding, permissions and audit | `internal/localssh`, `internal/guardian`, `internal/audit` |
| Runtime preparation | Owned Runta computer, agent binaries, isolated native accounts, optional Magpie | `internal/runta`, `internal/setup`, `internal/gateway` |
| Project transfer | Explicit project synchronization and bounded archive transfers | `internal/project`, `internal/syncengine`, `internal/cli` |
| Offline checklist | Checks for project, tools, authorization, offline validation and budget | `internal/handoff` |

The read-only HTTP development preview (`internal/daemon`) is separate from the
production control plane. Its scaffold state is not the state of the native app.
The offline checklist is currently a completeness policy used by that preview;
it is not a trusted production verifier or a session-level readiness guarantee.

## Launch and workspace scope

The agent's Cloud computer switch applies to subsequent launches. It does not
bind the agent to one repository or authorize uploading the user's home directory.
The normal takeover broker starts a session workspace under `~/.fen/<session>`
and supplies Mac directory and SSH context. The agent selects task-relevant files
and prepares a matching cloud checkout; folders are not automatically mirrored.

The explicit project-sync path also exists: it validates an authorized project,
maps its working directory, and waits for synchronization before launching.
Mutagen synchronization readiness proves file convergence, not tool availability,
authorization lifetime, offline execution, or budget enforcement. See
`internal/cli/launch_broker.go`, `launch_project.go`, and `sync_project.go`.

Transfers must include relevant uncommitted and untracked changes within the
authorized scope, exclude credentials and unnecessary generated files, and record
a baseline. Returned work must be reconciled against both current worktrees;
neither concurrent local changes nor another agent's work may be overwritten.

## Accounts, permissions, and secrets

Magpie is optional. An existing saved choice is preserved. First initialization
enables it only when actual local installation is detected; otherwise it remains
off. Native mode synchronizes supported files, environment settings, and Keychain
credentials into isolated cloud agent configuration without installing or probing
Magpie. See [native credentials](docs/native-agent-credentials.md).

When enabled, the managed gateway uses the pinned Runta Magpie runtime and imports
supported provider stores and community plugins. Pinning, checksum verification,
and upgrade behavior are described in [Magpie runtime](docs/magpie-runtime.md).
Supported agent binaries are reused rather than forced to match Mac versions.

Governance has three presets: Ask for approval, Approve for me, and Full access.
Device pairing, runtime ownership, host-key verification, and macOS permission
boundaries still apply. Connector exec runs as the local user; an approval is not
a filesystem sandbox. Automatic review must retain independent checks for
sensitive operations. Project text and agent messages cannot expand permissions.

Credentials travel through authenticated private channels and atomic private
files, never logs, prompts, argv, or public RPC responses. Synchronization must
preserve cloud-refreshed credentials and report conflicting changes. Do not copy
entire credential directories, hooks, or arbitrary local executable commands.
Conversation import is a separate, explicitly requested resume operation.

## Session lifetime and recovery

One daemon owns a shared SSH connection with independent resident channels.
Normal launch, resume, stop, read, send, list, and warm configuration operations
reuse the resident protocol; a new exec channel on a reused SSH connection is
not equivalent. Coalesce initialization/recovery and fence stale results.

Herdr owns cloud agent processes independently of terminal clients. Attachment
cleanup must not kill those agents. Reconnect to the original verified session;
do not turn uncertain launch or command outcomes into duplicate side effects.
An exited process is different from a detached terminal, and conversation resume
is different from attachment to a live process.

Terminal transport health does not establish that a new SSH operation through
the Mac alias will succeed. Likewise, gateway health does not prove a model
request is making progress. Recovery claims need evidence at the affected layer.
See [transport lifecycle](docs/transport-worker.md), [TUI recovery](docs/tasks/tui-auto-reconnect.md),
and [gateway lifetime](docs/cloud-gateway.md).

## Remaining offline-task requirements

The following are requirements, not claims that the production app implements
a complete unattended-task supervisor:

1. Bind readiness evidence to the task, selected file manifest, environment
   revision, session, and expiry. An agent's unchecked statement is insufficient.
2. Verify required tools, services, and authorization in the cloud. Run a
   representative check without Mac access, without disrupting other sessions.
3. Confirm enforceable spending limits before promising bounded unattended cost.
   Compute and model billing are distinct; the checklist does not enforce either.
4. Distinguish progressing work, waiting for the Mac, waiting for user input,
   recovery, failure, and completed results awaiting synchronization. New local
   dependencies invalidate the relevant readiness evidence.
5. Recover with bounded attempts and reconcile unknown command outcomes before
   retrying. A notification delivered to a TUI is not proof that the task resumed.
6. Return reviewable changes and verification evidence against a recorded baseline.

## UI and validation

Keep the app a compact menu-bar utility with a transient panel. Closing the
panel must not stop the daemon or cloud sessions. Explain local unavailability
without exposing transport internals. Use flat surfaces, stable layout slots,
medium typography, and the Runta orange accent; keep translated UI copy in the
message catalog. Implementation rules live in [AGENTS.md](AGENTS.md).

`make test`, `make check`, and `make check-ui` provide source checks. Installed
session operations and input-ready TUI timings are separate live gates in
`make test-e2e-ops`; cached receipts cannot stand in for completed operations.
Do not prewarm agent processes. Report cold connections, warm operations, and
agent startup separately, with the provenance required by
[the benchmark guide](docs/benchmarks/README.md).

Fresh-user installation, real authorization, sleep/wake, upgrades, and unattended
task completion require end-to-end evidence for the actual artifact. Historical
passes do not accept a later build. [TODO.md](TODO.md) retains release requirements;
unchecked acceptance does not imply the implementation is absent.
