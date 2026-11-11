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
