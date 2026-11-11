# cmux Codex hooks over Fennec

## Reported failure

cmux injects per-invocation Codex `-c hooks.<event>=...` arguments. The command
values reference generated Mac scripts such as
`~/.cmux/hooks/cmux-codex-hook-6f86d345090541b9-session-start.sh`.
Passing those arguments unchanged to a Linux cloud agent makes its shell exit
127 because the Mac paths do not exist there. Copying the scripts alone would
still leave their local cmux executable and socket dependencies unresolved.

## Compatibility boundary

Fennec recognizes only generated cmux scripts under the invoking user's
`.cmux/hooks`, validates the event and content-addressed filename, and rewrites
only their TOML command values to its remote `fennec-terminal cmux-hook` helper.
User hooks, other configuration, hook timeouts, and literal prompt arguments
remain unchanged. No local hook script is copied or executed from cloud input.

The helper sends a bounded request through a private guest Unix socket. Requests
and replies travel over the existing Fennec terminal protocol channel, scoped to
the session and request ID; each callback does not create an SSH connection or
exec channel. On the Mac, Fennec invokes the captured cmux CLI using fixed Codex
hook subcommands and the original workspace/surface scope. The official
`CMUX_AGENT_HOOK_RELAY_ORIGIN=1` mode prevents remote PIDs and transcript paths
from being interpreted as local resources. Local cmux socket credentials and
other local environment values are never sent to the guest.

cmux replies (including JSON and exit code 2) reach the remote hook. Missing or
disconnected desktop integration returns the neutral `{}` response, consistent
with cmux's optional integration; this does not disable unrelated hooks or
change cloud-launch failure handling. Hook work is bounded and does not block
the terminal read loop or a peer session.

The guest advertises `cmux_hooks`. A normal launch negotiates this capability
before launching. The explicit cold-SSH optimization negotiates first when cmux
is present, rather than sending an initial launch to an incompatible helper.
Desktop and guest helper artifacts must be deployed together.

## Verification

```sh
go test -race ./internal/cmuxhooks ./internal/terminalstream ./internal/launcher ./internal/cli ./cmd/fennec-terminal
make test-cmux CMUX_BINARY=/absolute/path/to/cmux.app/Contents/Resources/bin/cmux
```

The second target requires an actual cmux binary and fails when missing. It
executes the official argument generator in an isolated HOME/CODEX_HOME and the
actual cmux CLI against a private cmux API fixture. It does not modify a user's
real cmux workspaces or Codex configuration.

Verified with official release **v0.65.2.2**, binary version **0.65.2 (113)**,
source commit `12023d89feb5425c766ba05e8d8e4cfc176e3991`. The official generator
produced the exact session-start script hash from the reported failure and ten
handlers across eight hook events. The native CLI emitted `feed.push` for the
relayed session-start without local PID inference.

Regression coverage includes literal arguments and mixed user hooks, changed
script hashes, multiple sessions, private sockets, concurrent attachment,
foreign-stream replies, a slow hook with an independently responsive terminal,
exit-code/JSON propagation, and a disconnected desktop. The guest helper also
cross-compiles for Linux. A coworker's real cloud session and cmux UI still need
post-deployment acceptance; an API fixture is not that acceptance.

Source references (read as interoperability specifications, not shipped code):
- https://github.com/manaflow-ai/cmux/blob/12023d89feb5425c766ba05e8d8e4cfc176e3991/CLI/CMUXCLI%2BCodexFireAndForgetHooks.swift
- https://github.com/manaflow-ai/cmux/blob/12023d89feb5425c766ba05e8d8e4cfc176e3991/Packages/macOS/CMUXAgentLaunch/Sources/CMUXAgentLaunch/CodexHookInjectionSchema.swift
- https://github.com/manaflow-ai/cmux/blob/12023d89feb5425c766ba05e8d8e4cfc176e3991/CLI/CMUXCLI%2BAgentHookProcessBinding.swift
