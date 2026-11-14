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
| Cursor CLI | Keychain `cursor-access-token`, `cursor-refresh-token`, `cursor-api-key` for `cursor-user`, or `~/.cursor/auth.json` with file storage; selected preferences from `CURSOR_CONFIG_DIR/cli-config.json` | Isolated `CURSOR_CONFIG_DIR`, `CURSOR_DATA_DIR`, and `XDG_CONFIG_HOME`; file credential storage, `cursor-agent --force --trust --sandbox disabled` |
| Grok Build | `GROK_HOME` or `~/.grok`; `auth.json` (honoring `GROK_AUTH_PATH`), model configuration from `config.toml`, or `XAI_API_KEY` | Isolated `GROK_HOME`; `grok --always-approve`; cloud sign-in uses `login --device-auth` |

Cursor CLI retains native authentication when Magpie is enabled because Magpie
does not route Cursor CLI. Its model selection stays in the native CLI (`/model`
or `--model`). Grok Build follows the Magpie setting: enabled uses Magpie's
configuration and Fennec's model picker; disabled uses isolated native credentials.
Warm Grok model changes select an existing verified Magpie model table in Go,
preserving its API, key and context limit and disabling remote campaign overrides.
Both
installers publish only `cursor-agent` and `grok`, never `cursor` or `agent`.
Configuration migration excludes hooks, plugins, MCP commands, trust grants and
session history. Conflicting credentials preserve the cloud refresh or defer
the conflict rather than overwriting an unverified login.

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
- Pi: `--session <id-or-path>`, `--session-id <id>`, and `--continue`/`-c`.
  Explicit session directories and paths are resolved locally, then replaced
  with the selected ID in the cloud workspace's native session directory.
- Kimi Code: `--session <id>`/`-S <id>` and `--continue`/`-c`. Session state, agent journals,
  attachments and the session index are restored under `KIMI_CODE_HOME`, with
  agent and attachment paths mapped to the imported session.
- OpenCode: `--session <id>`/`-s <id>` and `--continue`/`-c`. Its native
  export/import commands handle database schemas and media storage. Both the
  v1 commands and v2 `session export`/`session import` interface are recognized.
- Cursor CLI: `--resume <id>`, `--continue`, `--resume=-1`, and `resume`.
  A consistent SQLite snapshot includes committed WAL pages. Chats are restored
  beneath the cloud workspace's native hash in `CURSOR_CONFIG_DIR/chats`.
- Grok Build: `--resume`/`-r` (ID, exact title, or latest) and `--continue`/`-c`.
  Native history, summary, checkpoints and session side state are restored in
  `GROK_HOME/sessions`. Ambiguous titles require an explicit ID.

Every `cwd` field is mapped to the cloud workspace.
Import uses the same active account directory as native launch, including
separate cloud logins. Cursor and Grok retain their isolated native directories
even when Magpie is enabled for other agents. Invalid account selections fail
without publishing the conversation into a different account.

The link to the cloud computer is slow (about 130 KB/s), so only what the agent
needs to continue is sent:

- Before sending, the Mac asks whether a native cloud copy already exists.
  An existing cloud conversation is authoritative, including after a later
  cloud compaction removes the original Mac marker. Repeated resumes do not
  upload or overwrite it. Local edits to an already imported conversation are
  not merged into the cloud copy; use a separate conversation to migrate a fork.
- A compacted Claude transcript is trimmed to what `--resume` loads: everything
  from the last compact boundary on, the messages that compaction preserved
  (`preservedMessages` and the `preservedSegment` chain), and small metadata
  records. Earlier history and the `api-request*` dumps stay on the Mac; a
  rewind past the last compaction is not available in the cloud.
- Codex keeps the latest `replacement_history` checkpoint, its retained
  messages and subsequent records. Session, turn and window metadata survive;
  old response bodies and screenshot duplicates are omitted. Plain and zstd
  rollouts are supported. Legacy checkpoints without replacement history and
  rollouts containing rollback records travel whole.
- Pi preserves parent IDs, model/thinking settings, extension state, context
  edits, the latest summary and its `firstKeptEntryId` range. Earlier message
  bodies are removed. Branched trees and unresolved retained IDs travel whole.
- Kimi Code preserves the wire's event positions and the exact user messages
  from which modern compaction recomputes its retained context. Covered
  assistant/tool payloads become small placeholders while tool-call identities,
  summaries, injections and side state survive. Legacy tail compaction and
  journals with undo records travel whole.
- OpenCode keeps the latest completed summary, its compaction parent, retained
  tail and subsequent messages in chronological storage order. Failed summaries,
  missing retained references, reverted sessions and provider-native windows
  that may need the original transcript are not trimmed.
- Cursor CLI keeps the current root and every reachable blob, including retained
  images, summary archives, tools and subagent state. Unreachable historical
  checkpoints are omitted from the transferred SQLite snapshot. Native archives
  still referenced by the current state remain; reduction depends on the native
  reference graph. Unknown database schemas and mutable blob IDs travel whole.
- Grok Build already stores the active model context in `chat_history.jsonl`.
  Fennec preserves it unchanged and removes covered message/tool payloads from
  `updates.jsonl` before the latest complete compaction checkpoint, preserving
  subsequent events and side state. Obsolete checkpoint files are omitted.
  Rewound journals, missing checkpoints and unknown checkpoint versions travel
  whole; pre-compaction scrollback/rewind is not migrated after a safe trim.
- The transfer is zstd at the "better" level. A 90 MB transcript with
  screenshots went from 30 MB to 0.3 MB in the Claude example; savings depend on
  what each agent retains. Original local files remain unchanged. Unsupported
  or incomplete formats are sent without trimming rather than guessing a cut.

Transfers use a dedicated, credit-controlled stream on the resident terminal
connection (protocol 10), with bounded framing and private atomic file writes.
They do not open another SSH exec channel or place transcripts in argv. Other
terminals and control operations share the connection during an upload.
Pickers without a selected ID list cloud conversations only.

Compaction rules follow the native implementations: Codex's
`session/rollout_reconstruction.rs`, Pi's `buildSessionContext`, Kimi Code's
`restoreLoopJournal`, and OpenCode's `filterCompacted`/session history loader.
Optional acceptance tests use installed loaders without inference requests:
`FENNEC_PI_SESSION_MODULE`, `FENNEC_KIMI_BUNDLE`, and
`FENNEC_OPENCODE_BINARY` enable the corresponding tests in
`internal/runtime/session_compaction_test.go`; `FENNEC_KIMI_BINARY` additionally
checks native session discovery. `FENNEC_TERMINAL_BINARY` exercises transfer
through a freshly built resident terminal process.
`FENNEC_CURSOR_BUNDLE` (the installed `index.js`) and `FENNEC_GROK_BINARY`
enable native Cursor loader and Grok export acceptance tests in
`internal/runtime/session_import_vendor_test.go`. Cursor's SQLite adapter uses
pure-Go SQLite so consistent snapshots work without a system sqlite3 executable
or a C toolchain. No provider inference is required by these checks.
