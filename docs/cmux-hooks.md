# Local hooks in cloud Codex and Claude sessions

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

The transport and permission engine are plugin-independent. Codex custom hooks and cmux Claude settings have
configuration adapters. Other agents need an adapter for their own hook protocol.
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

## Real release acceptance (2026-11-11)

The signed and notarized [0.2.11111843 candidate](https://github.com/runta-dev/fennec/releases/tag/v0.2.11111843), source `892a25f7f062b6a5a3c1d02b4f09d255b080951a`, was installed from its actual CI DMG and exercised in official cmux 0.65.2 (113), with cloud Codex 0.167.1. [Release CI](https://github.com/runta-dev/errand/actions/runs/43005873516) passed.

A real cloud task executed `printf` and returned its requested marker. Seven one-shot authorizations covered session-start, prompt-submit, inbox-drain, pre-tool-use, post-tool-use, stop, and inbox-stop. The real cmux workstream journal recorded sessionStart, userPrompt, toolUse, toolResult, and stop for the same session; the actual sidebar showed `codex · Done`. No persistent Always Allow grant was added. This was not a fixture socket or a direct synthetic hook call substituted for an agent event.

This verifies the reported cloud hook 127 fix and the exercised hook lifecycle on a Mac with existing login state. It does not establish clean-Mac onboarding, colleague-device acceptance, or live coverage of PermissionRequest and subagent events.

The independent `cmux-cua` MCP attachment still references a Mac-local client/socket and is not supported in the cloud. The user explicitly deferred this feature on 2026-11-11. Its native MCP profile and Codex compatibility profile have different contracts and authorization requirements; unfinished relay experiments were removed from the release worktree, not shipped. See the deferred item in [TODO](../TODO.md).

## Claude settings takeover (2026-11-17)

Claude's cmux wrapper passes a Mac-local `--settings` file. Forwarding that path
unchanged produces `Settings file not found` on Linux, whether the wrapper uses
`/var/folders/.../cmux-claude-settings.*` or the newer
`~/.cmuxterm/claude-settings/*.json`. Codex hook rewriting did not cover this path.

Fennec snapshots file-valued and inline Claude settings before broker dispatch,
rewrites recognized cmux handlers, and transfers the JSON as a bounded structured
launch field. The guest writes a mode-0600 session file under
`~/.local/fennec/launch-settings/<session>/`; argv contains only its path, never the
settings contents. Snapshots survive detachment and retain the original settings
for that launch. Unknown settings, matchers, async flags, and unrelated user hook
commands remain intact. Native `--continue` import reindexes the placeholders
when it inserts an explicit resume ID. Peers advertise `claude_settings_v1`;
older peers are rejected before dispatch rather than silently losing the file.
Cold starts also send settings in protocol frames rather than SSH command argv.

The Claude adapter recognizes exact command digests from official cmux 0.65.2.2,
then invokes fixed CLI subcommands through the existing authorized hook bridge.
It never evaluates the generated shell on the Mac. Modified/unknown cmux commands
fail explicitly rather than being guessed or silently discarded. `make test-cmux`
checks every handler emitted by the supplied official CLI, including the Claude
settings generator. Claude uses its own captured environment and bindings,
separate from Codex. Cloud transcript paths, cwd, process IDs, and local spool
routing are not passed to the local handler. Cron guards and permission decisions
are required hooks; observational hooks retain the bridge's fail-open policy.

Long-running handlers use the existing bounded bridge execution window (110
seconds for Claude inbox wait, permission feed, and auto-name). An idle inbox
wait is therefore not an all-day notification subscription. Local transcript
inspection is deliberately unavailable for a cloud transcript. These limits are
separate from starting Claude and delivering lifecycle/tool events.

Acceptance on 2026-11-17 used official cmux 0.65.2.2 and real cloud Claude Code
2.1.326. In a fresh cmux workspace, installed Fennec 0.3.11171712-dev reproduced
the missing-settings error. Signed development build 0.3.11171741-dev reached
an input-ready cloud TUI in the same terminal. Claude executed
`printf FENNEC_CMUX_CLAUDE_OK` successfully and returned that marker; cmux moved
from Working to Done. Its real workstream journal recorded Claude session-start,
user-prompt, tool-use, and stop events for native session
`029c77fe-dff9-486b-82de-26882a1fbe1f` (Fennec session `magenta-fern`). This was a
live agent round trip, not a fixture socket or a synthetic hook invocation.
The stable credential helper remained byte-identical across the development
install. No release publication is implied by this development acceptance.

The same live session detached and reattached successfully with `fen attach
magenta-fern`. Focused Go tests, the official CLI tests, UI/localization checks,
and targeted hook/transport race tests passed. The complete terminalstream race
suite exceeded a three-minute overall timeout in `TestConflatorRecordedSessions`;
the ordinary terminalstream suite passed. The installed-client operation gate
also reported five `sessions.send` samples above its 500 ms per-sample limit, so
this change does not claim complete performance acceptance.
