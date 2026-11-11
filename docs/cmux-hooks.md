# Local hooks in cloud Codex sessions

cmux injects Codex hook commands pointing to generated Mac scripts. Those paths
cannot execute in a Linux sandbox (exit 127), and copying scripts does not supply
the Mac's cmux application or socket. Fennec bridges recognized hooks back to the
Mac through the existing resident terminal channel, without a new SSH connection.

## Permission and execution

Every local hook, including cmux, uses the daemon's existing approval queue.
Registering an adapter is not authorization. Allow runs one invocation; Always
Allow remembers the authenticated owner, cloud runtime and hook fingerprint.
SSH device trust does not authorize hooks. The same native approval UI and
`fen approve <id> --always`, `fen deny <id>`, `fen trust clear` commands apply.
Revocation cancels active hook executions. Account cancellation, client disconnect,
and daemon shutdown also cancel work. No uncertain hook execution is retried.

The fingerprint includes the local command definition, executable contents,
regular file arguments (relative to its working directory), and explicitly watched
files. Changed definitions/files cannot use an earlier binding or approval. Start
a new invocation to bind changed hooks; attaching cannot rewrite running Codex argv.
Dependencies loaded indirectly must be declared in `files`. This is not an OS
sandbox or a defense against another process modifying files as the same user.

The terminal client forwards references and stdin to the daemon; it cannot execute
hook commands directly. Remote requests cannot provide executable paths,
environment, permission keys, or failure policy. Session manifests carry only
binding metadata. References are tied to a local lease, account and runtime.

## Additional plugins

The transport and permission engine are plugin-independent. Codex is the current
configuration adapter. Other agents need an adapter for their own hook protocol.
Fennec does not guess that an arbitrary absolute path should execute on the Mac.
Unrecognized hook configuration remains unchanged.

Declare custom local hooks explicitly, matching their exact Codex event/command:

```json
{
  "version": 1,
  "hooks": [{
    "name": "review-policy",
    "agent": "codex",
    "event": "PreToolUse",
    "command": "/Users/me/tools/review-policy.sh",
    "location": "local",
    "policy": "required",
    "executable": "/bin/sh",
    "arguments": ["/Users/me/tools/review-policy.sh"],
    "directory": "/Users/me/tools",
    "files": [],
    "timeout_ms": 10000
  }]
}
```

Use `fen hooks import /absolute/path/hooks.json`, `fen hooks list`, and
`fen hooks remove review-policy`. Import replaces the registered configuration,
not the permission store. A `sandbox` declaration explicitly leaves the matching
command on the cloud machine; Fennec does not install its dependencies.

Required hooks block on denial, missing registration, disconnection, timeout or
invalid responses. Nonzero results map to Codex exit 2 for blocking events.
Observer hooks may skip when unavailable or denied, but never execute unapproved.
Required local hooks support PreToolUse, UserPromptSubmit, Stop and
PermissionRequest. Native hook output and exit status travel back to the caller.

## cmux adapter

Only generated scripts under the invoking user's `.cmux/hooks` are recognized;
the event and content-addressed filename are validated. Explicit user definitions
take precedence. The adapter invokes fixed native cmux subcommands, with captured
workspace/surface routing and `CMUX_AGENT_HOOK_RELAY_ORIGIN=1`. Mac environment
and socket information are not sent to the guest. PermissionRequest is required;
lifecycle/display integration is observer. Neither category bypasses approval.

Matched handler timeouts include the approval budget. Other arguments and hooks
remain unchanged. Desktop and guest negotiate `hook_bridge_v1` before launch;
the helper is `fennec-terminal hook <session> <reference>`.

## Verification

```sh
make test-cmux CMUX_BINARY=/absolute/path/to/cmux.app/Contents/Resources/bin/cmux
```

The target requires the real cmux binary, runs its argument generator in isolated
HOME/CODEX_HOME, and its CLI against a private API fixture through the authorized
adapter. It also runs race tests for permission, transport and daemon components.
It does not change real cmux workspaces or install a Fennec build.

Reference release: v0.65.2.2; binary 0.65.2 (113); source
`12023d89feb5425c766ba05e8d8e4cfc176e3991`. The generator reproduces the reported
session-start hash and ten handlers across eight events. A colleague's actual
cmux/cloud session remains a separate post-deployment acceptance step.

Official implementation references:
- https://github.com/manaflow-ai/cmux/blob/12023d89feb5425c766ba05e8d8e4cfc176e3991/CLI/CMUXCLI%2BCodexFireAndForgetHooks.swift
- https://github.com/manaflow-ai/cmux/blob/12023d89feb5425c766ba05e8d8e4cfc176e3991/Packages/macOS/CMUXAgentLaunch/Sources/CMUXAgentLaunch/CodexHookInjectionSchema.swift
