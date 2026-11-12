# Codex Desktop and cloud app-server

Status: architecture research, not a shipped Codex Desktop integration

Source snapshot: `openai/codex@c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee`
(2026-11-12). The managed daemon is experimental; pin its tested release and
re-read its lifecycle contract when updating it.

## What Codex Desktop actually runs

The Electron application is a client. The Codex app-server owns the agent loop,
including context, model turns, shell and MCP tools, sandbox policy, approvals,
thread persistence, and event streaming. A second TUI process is not a
Desktop-compatible replacement.

The local inspection found two different endpoints on this Mac:

- `/Applications/ChatGPT.app/Contents/Resources/codex-cli/.../codex app-server`
  is the desktop-owned process, using its stdio connection
- `~/.codex/packages/app-server-daemon/.../codex app-server --listen unix://
  --managed-daemon` is a separately managed daemon, with its control socket at
  `~/.codex/app-server-control/app-server-control.sock`

Fennec must not kill, restart, take ownership of, or migrate either live local
process. Doing so would only relocate or interrupt the existing client, not
transparently move its running agent loop to Linux.

## Recommended cloud architecture

Run a separate managed Codex app-server on the user's existing shared cloud
computer. Keep the Mac as the UI, approval surface, and explicitly authorized
edge connector:

```text
Codex Desktop or Fennec UI
            |
       local daemon
            |
     resident SSH connection
            |
     private RPC channel ---- cloud Unix socket
                                  |
                       managed Codex app-server
                                  |
                   agent loop, tools, threads, approvals
                                  |
                       isolated cloud CODEX_HOME
```

This is a native harness integration, not terminal emulation. Do not create a
second machine or another long-lived SSH connection for it. Keep the existing
Codex TUI adapter for terminal sessions; the Desktop/RPC adapter is a distinct
session type and must not masquerade as a Herdr terminal.

### Cloud lifecycle

- Give each selected cloud account an immutable, private `CODEX_HOME`. Do not
  point the daemon at the Mac's `.codex` or a mutable account-selection symlink.
- Start the complete managed Linux package with `codex app-server daemon start`.
  A package is required; the current single npm-installed executable is not
  automatically a complete managed daemon installation. If it is missing,
  bootstrap the publisher's complete package rather than copying the macOS one.
- Reuse the healthy daemon and its existing socket. Do not compare the Mac and
  cloud CLI versions as a routine launch prerequisite. Record the negotiated
  RPC capabilities for compatibility checks.
- Let that VM own subsequent credential refresh, with its own persistent host
  identity. Never place credentials in argv, RPC payloads, or logs.
- Do not let an automatic daemon update or a UI restart interrupt a running
  turn. Graceful shutdown defaults to 60 seconds and is bounded by configuration;
  reconcile saved threads before resuming them.
- A daemon shares the environment captured at startup, not per-client
  environments. Separate cloud account homes rather than attempting to switch
  credentials underneath a live daemon.

For Fennec's own RPC client, a private Unix socket is sufficient. Do not enable
Codex remote control merely to proxy that local socket. Do not expose a
non-loopback WebSocket listener.

### Transport and recovery

Add an app-server stream capability to `internal/terminalstream` and the cloud
`fennec-terminal` service. It dials the private Unix socket and carries complete
JSON-RPC messages with bounded size and backpressure. The outer stream needs a
unique RPC connection ID and explicit open/data/close semantics; it must not
confuse these messages with PTY bytes or terminal operation receipts.

The shared SSH connection stays owned by the existing Go control daemon. UI and
CLI use that same daemon. Initial daemon bootstrap is asynchronous preparation,
not part of every warm operation. Measure control operations to their actual RPC
response, with the existing 500 ms warm-operation budget under normal networking.

Each logical client owns an app-server connection and performs `initialize`
once, then sends `initialized`. Do not put independently interactive Desktop and
Fennec clients behind one unscoped shared JSON-RPC connection: requests,
responses, approvals, subscriptions, and disconnects are connection-scoped.

Persist the returned `thread.id` and `thread.sessionId`, not a synthetic Herdr
terminal ID. Use `thread/read` and paginated history to reconcile after a
disconnect; resume with `thread/resume` only when appropriate. Do not infer turn
success from reconnecting: only `turn/completed` with `turn.status=completed`
is success. Never replay a mutating request whose outcome is unknown.

## Permissions and local boundaries

Preserve app-server events, including `item/agentMessage/delta`,
`item/commandExecution/requestApproval`, `item/fileChange/requestApproval`,
permission requests, user-input requests, tool calls, and `turn/completed`.
Forward the exact approval decision to its originating connection. Closing a UI
window is detachment, not automatic approval, rejection, or session termination.

The cloud sandbox governs cloud files and commands. It is not authorization to
execute on the Mac. Local directories in RPC requests must not be presented as
though they were cloud paths. Local files, credentials, and MCP tools cross the
edge only through the existing explicitly approved connector contracts. The
new Desktop work must converge with the Codex permission-preset and approval
UX before claiming governance parity; the current TUI's full-access defaults
are not that completion.

## Native Codex Desktop client

The open-source daemon explicitly supports instances launched over SSH and
remote desktop/mobile clients. It provides `daemon bootstrap --remote-control`,
pairing and reconnect-capable remote-control transport. This makes a native
Desktop client technically plausible without moving the Electron app itself.

It is not yet a verified Fennec capability. The proprietary Desktop UI and its
exact host/pairing registration are outside the open-source Rust repository.
Do not promise native Desktop compatibility from socket forwarding alone, and
do not scrape or reproduce private Electron implementation details. Validate
the supported remote-control pairing flow on a separate test thread and host;
leave existing local Desktop sessions untouched.

If native pairing is unsupported for our use case, Fennec can still provide its
own client over the documented app-server protocol. That is a supported harness
integration, but it is not the original Codex Desktop UI.

The hosted Agents API and `codex exec-server` are a separate integration path.
They are not interchangeable with the local app-server daemon used here.

## Delivery and acceptance

1. Add isolated cloud daemon discovery, bootstrap, health, and private Unix
   socket proxying in Go. Test a second client connecting and disconnecting
   without terminating the daemon or another client's thread.
2. Complete a fresh `thread/start` -> `turn/start` turn in an isolated cloud
   workspace. Verify cloud-side tool effects and streamed events, then stop the
   test only after the turn finishes.
3. Verify reconnect/history, interruption, approvals, MCP behavior, expired
   authorization, cloud restart, and offline Mac behavior. In particular, no
   approval or operation may be duplicated after an uncertain disconnect.
4. Validate the official Desktop remote-control/pairing flow separately from
   Fennec's own app-server client. If this fails, report the native UI boundary
   rather than silently substituting a terminal.
5. Keep the real Codex Desktop E2E separate from the existing TUI performance
   suite. UI closure, app restart, and daemon restart must each prove thread
   continuity and preserve all unrelated sessions and credentials.

## Sources

- [Official app-server protocol](https://developers.openai.com/codex/app-server)
- [Official Codex platform guidance](https://developers.openai.com/blog/codex-as-a-platform)
- [Official self-hosted VM credential lifecycle](https://developers.openai.com/siwc/token-sharing-open-source/self-hosted-vms)
- [Open-source managed daemon contract](https://github.com/openai/codex/blob/c0cc9fc820f62e4dbdde68214dd6d792d6c8a7ee/codex-rs/app-server-daemon/README.md)

The local managed-daemon README states the SSH-client intent, experimental
status, shared-environment limitation, and update/graceful-shutdown behavior.
The transport implementation supports stdio, Unix sockets, WebSockets, and
remote-control connections with connection-scoped request routing.
