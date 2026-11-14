# Native credentials and optional Magpie

Fennec's default mode does not require Magpie on the Mac or cloud computer.
The Go daemon discovers each installed cloud-supported agent's native sign-in,
transmits it through the authenticated shared protocol channel (bounded private
SSH stdin for large envelopes), and prepares agent-owned cloud configuration.
It never sends an inference request to decide whether the TUI may start.

| Agent | Native sources | Cloud configuration |
| --- | --- | --- |
| Codex | `CODEX_HOME` or `~/.codex`; auth.json, selected model/provider fields from config.toml; configured macOS Keychain store | Isolated CODEX_HOME, file-backed auth.json |
| Claude Code | `CLAUDE_CONFIG_DIR` or `~/.claude`; macOS Keychain first, then .credentials.json; model and supported provider environment settings | Isolated CLAUDE_CONFIG_DIR, Linux credential file and onboarding profile |
| Pi | `PI_CODING_AGENT_DIR` or `~/.pi/agent`; auth.json, model settings and models.json | Isolated PI_CODING_AGENT_DIR |
| OpenCode | XDG data/config roots; auth.json and provider/model fields | Isolated XDG data/config roots |
| Kimi Code | `KIMI_CODE_HOME` or `~/.kimi-code`; credentials/kimi-code.json (OAuth), the region file, and from config.toml the default model, thinking settings, services and remote providers with their models (no hooks, no loopback or Magpie providers) | Isolated KIMI_CODE_HOME; launched with `--auto` (Never Ask) |

Supported provider environment variables are explicitly allowlisted in
`internal/setup/native_accounts.go`. The daemon reads these variables and custom
agent directory variables from the user's login shell, since a macOS LaunchAgent
does not inherit an interactive terminal's environment. No environment dump is
logged or returned to the UI.

Cloud files live under `~/.local/fennec/native`, with private directories and
atomic 0600 file writes. Synchronization retains cloud OAuth refreshes when the
local source has not changed. Conflicting changes fail without replacing the
cloud credential. Symlink destinations and arbitrary filenames are rejected.
An unreadable agent sign-in does not prevent other agents from preparing.
Local-only model endpoints and shell-command credential sources require explicit
configuration rather than silently guessing a substitute.

The Mac's agents and credentials are not rewritten. Native config exports omit
session histories, hooks, MCP commands, and local extensions. Existing cloud
sessions keep their process environment when the integration mode changes.

Magpie is enabled only by `settings.set {"magpie":true}` through Settings or
`fen settings magpie on`. The default is false, including upgrades and legacy
settings imports. Enabling it installs/starts the cloud gateway and permits
`fen model <agent> <provider/model>` or the agent card's cloud model selector.
Existing local Magpie provider stores are imported only in this opted-in mode;
they are not a prerequisite for native onboarding. Disabling uses the preserved
native configuration for new sessions; it does not kill existing sessions.

Implementation references were inspected at Magpie commit
`5c05ac41b9e6680779998e563287dc85c9d7f2da`:

- `internal/provider/codex_store.go`: Codex store selection and Keychain identity.
- `internal/provider/account.go` and `claude_dirs.go`: Claude Keychain precedence,
  directory-specific identities, hexadecimal Keychain output, onboarding profile.
- `internal/sessions/pi.go`: Pi's native configuration directory.

These references inform the Go adapters; Fennec does not execute Magpie in the
default discovery or credential synchronization path.

### Native model selection

With Magpie disabled, the cloud model picker uses the agent's own model catalog:
Codex's isolated `models_cache.json`, Claude Code's built-in aliases and account
model cache, and Pi's available models filtered to configured providers. Missing
catalog metadata offers Default; preparation never runs inference to discover
models. Pi's list command runs only on an explicit model metadata request, with
extensions and skills disabled.

`fen model <agent>` lists native choices. `fen model <agent> <model|default>
[effort]` uses the same daemon control methods as the UI. Catalog membership and
supported effort are checked on the cloud computer through the resident channel.
Native selections live in the isolated native configuration, independently of
Magpie route preferences. Repeated credential synchronization preserves explicit
cloud choices while still detecting unrelated credential conflicts.

Native synchronization removes inherited cross-vendor model IDs (for example,
`kimi-code/...` from ChatGPT-backed Codex) and lets the agent choose its default.
It never rewrites the user's local configuration or invents a replacement model.

### Native OAuth takes precedence over leftover gateway routing

When native mode has a saved Codex ChatGPT login, a Claude OAuth login, or an
explicit `CLAUDE_CODE_OAUTH_TOKEN`, export and cloud application remove the
proxy endpoint/key overrides from that agent's isolated configuration. Claude's
OAuth token is supported both in the source environment and in `settings.env`.
Without a native OAuth identity, an API key is never silently redirected from a
proxy to an official vendor endpoint.

The launch environment filters credentials by agent and prevents another
agent's shared environment entries from overriding an OAuth login. Inherited
model override variables are removed. Source files and cloud-refreshed tokens
retain the existing isolation/conflict rules; policy migration preserves cloud
TUI preferences. This does not create a native login when the user only signed
in through Magpie: the product still needs a native sign-in recovery flow.

### Account surfaces and cloud sign-in

Native mode shows the current native account in both run locations. Account
metadata is a whitelist derived from the agent's saved files and explicit token
claims; it is not a live quota probe. A missing account is a normal signed-out
state, not a global preparation failure. Magpie mode uses gateway model selection;
local changes use the local Magpie CLI and cloud changes use the cloud control plane.

`fen agent account <agent> [local|cloud]` reads the same account view as the UI.
`fen agent login <agent>` opens an isolated cloud sign-in session. Its successful
completion selects the cloud account without modifying the Mac's credentials.
The daemon observes completed login IDs through its existing session channel,
including when the UI is closed. Canceling invalidates the pending login before
stopping it; temporary credentials are removed only after stop is confirmed.

### Resuming a Mac conversation in a takeover session

A takeover launch runs the agent in a fresh cloud workspace (`~/.fen/<session>`),
so resuming a conversation that exists only on the Mac would fail with "No
conversation found". Before such a launch the broker copies the conversation to
the cloud computer, as cloud ChatGPT does for Mac threads:

- Claude Code: `--resume <id>`, `-r <id>`, `--resume=<id>`, and `--continue`/`-c`
  (the newest conversation for the directory, launched as `--resume <id>`). The
  transcript and its `<id>/` folder (subagents, tool results) are written under
  the cloud workspace's project folder.
- Codex: `resume <id>` and `resume --last [--all]` (launched with the ID found on
  the Mac). The rollout keeps its relative path under CODEX_HOME and adopts the
  cloud Codex's configured provider.

Every `cwd` field is mapped to the cloud workspace. A cloud copy with at least as
many records is kept. The transfer is zstd-compressed on the shared SSH
connection (`fennec-terminal import-session`), never in argv or a protocol frame.
A picker (`--resume` with no ID, `codex resume`) lists cloud conversations only.
Pi, Kimi Code and OpenCode conversations are not moved yet.
