# Local agent shims: injection, management and removal

Status: current behavior documented from source `36f0e85` (2026-11-18); the
redesign below is approved in direction (decisions of 2026-11-18) and not
implemented. Update the "Current mechanism"
section when the code changes, and keep the incident record as dated evidence.

## Contract

When the user selects the cloud computer for an agent, typing that agent's
command in any of the user's terminals starts it on the cloud computer. When
the agent stays on this Mac, the original executable runs unchanged. Fennec
never runs an agent somewhere other than where the user chose without saying
so, never claims coverage it has not verified, and can be removed, including
by dragging the application to the Trash, without leaving the user's shell,
agents or Codex Desktop changed.

## Current mechanism

Three layers route a local command to the cloud computer:

1. **PATH shims.** `launcher.InstallShims` writes one `#!/bin/sh` launcher per
   supported agent into `~/.local/share/fennec/shims`, marked
   `# Fennec managed launcher v1`. Each runs `fen dispatch-agent <agent>`.
   `launcher.InstallShellIntegration` writes `~/.local/share/fennec/shell-init.sh`
   and appends one marked block to `.zshrc` (or `$ZDOTDIR/.zshrc`), `.bashrc`
   plus the bash login file, or `fish/conf.d/fennec.fish`. The script prepends
   the shims directory to `PATH` once, when the shell starts. Touched files are
   recorded in `shell-files.json`.
2. **Entry wrappers.** `launcher.InstallEntry` replaces the agent's own
   executable symlink with a marked wrapper that runs
   `fen dispatch-entry <agent> <original>`. The original link is kept in a
   `<path>.fennec-original.json` sidecar. The entry is the first match in
   `agent.EntryPath`'s search list: the daemon's `PATH`, then a fixed list
   (`~/.local/bin`, `~/.bun/bin`, `~/.npm-global/bin`, `~/.volta/bin`,
   `/opt/homebrew/bin`, `/usr/local/bin`, nvm and fnm installs). Only symlinks
   are wrapped.
3. **Dispatch.** Both launchers read `~/.config/fennec/takeover.json`. A
   selected agent is sent to the cloud broker; any other is `exec`ed locally
   with no message. If the installed Fennec executable no longer exists, the
   launchers run the original agent and print
   `Fennec is no longer installed here. Running locally.`

The shims, the shell block and the entry wrappers are installed in the launch
broker's ready step (`internal/cli/launch_broker.go`), which runs only after
the cloud computer's `shared_services_ready` event. The daemon retries a
failed broker in the background and discards its error
(`_ = serveLaunchBroker(...)` in `control_services.go`). `takeover.json` is
written separately by `reconcileSelectionLocked`, so the UI can show an agent
on the cloud computer while no terminal integration exists.

Codex Desktop (ChatGPT.app, `com.openai.codex`) uses a different mechanism.
`chatgptBridge.enableLocked` serves a loopback WebSocket and runs
`launchctl setenv CODEX_APP_SERVER_WS_URL ws://127.0.0.1:<port>/fennec/<token>`
in the user's launchd domain, then restarts a running ChatGPT.app with
`open -b com.openai.codex --env CODEX_APP_SERVER_WS_URL=...`. The variable is
deliberately kept when the daemon stops and is removed only when the user
moves Codex Desktop back to this Mac (`disableLocked`).

### Artifacts Fennec leaves on the Mac

| Artifact | Written by | Removed by uninstall | Removed after Trash |
| --- | --- | --- | --- |
| Shims in `~/.local/share/fennec/shims` | broker ready | yes (data) | yes, while the daemon runs |
| `shell-init.sh`, `shell-files.json` | broker ready | yes | yes, while the daemon runs |
| Marked block in shell startup files | broker ready | yes | yes, while the daemon runs |
| Entry wrapper and `.fennec-original.json` | broker ready or daemon repair | yes | yes, while the daemon runs |
| `~/.config/fennec/takeover.json` and preferences | daemon | yes (data) | no |
| `~/Library/LaunchAgents/com.runta.fennec.daemon.plist` | daemon install | yes | yes, while the daemon runs |
| `CODEX_APP_SERVER_WS_URL` in launchd | ChatGPT bridge | **no** | **no** |
| ChatGPT.app running with the bridge URL | ChatGPT bridge | **no** | **no** |

The Trash watcher (`internal/cli/trash_watch.go`) is part of the daemon. It
polls every two seconds and acts after two consecutive observations that the
bundle is in the Trash or gone. If the daemon is not running when the bundle
is trashed, or the Mac restarts first, nothing is removed, and the LaunchAgent
keeps pointing at a missing executable.

## Incident record, 2026-11-18

A colleague on Fennec 0.5.1 had Codex set to the cloud computer
(`takeover.json` was `{"agents":["codex"]}`), but `codex` in a new terminal
started local Codex 0.170.0. The cloud computer ran 0.169.1. Observed on the
Mac:

- `which -a codex`: `~/.local/share/mise/shims/codex`,
  `~/.local/share/mise/installs/node/24.19.0/bin/codex`, `/usr/local/bin/codex`.
  The Fennec shims directory was not on `PATH`.
- `.zshrc` had no Fennec block, `shell-files.json` did not exist, and the
  shims directory was empty or absent. `/usr/local/bin/codex` was the original
  Codex script, not a wrapper. The login shell was `/bin/zsh` with no
  `ZDOTDIR`.
- `.zshrc` runs `eval "$(mise activate zsh)"` at line 215, after the
  legacy `PATH` lines; `.zprofile` and `.zshrc` contain 15 active
  `export PATH=` lines.

Findings:

- The broker's ready step never completed on that Mac, so none of the three
  layers existed, and nothing in the UI said so. Which step failed was not yet
  determined at the time of writing.
- Had it completed, `mise activate` rebuilds `PATH` before each prompt and the
  entry search does not include mise directories. The shell block would have
  been reordered or dropped, and the wrapper would have landed on
  `/usr/local/bin/codex`, third in that shell's resolution.

## Environments the injection must handle

Each row names what breaks in the current design.

### Shell configuration

| Environment | Effect today |
| --- | --- |
| zsh with `ZDOTDIR` set in `~/.zshenv` | The daemon's environment lacks `ZDOTDIR`, so the block goes into `~/.zshrc`, which zsh never reads. Uninstall already asks the login shell (`setup.LocalShellDirectories`); install does not. |
| bash (Terminal.app starts login shells), macOS bash 3.2 | Handled for `.bashrc` and one login file; syntax must stay 3.2-compatible. |
| fish | Handled through `conf.d`. |
| nushell, xonsh, elvish, PowerShell | Unsupported shell is an error, which fails the broker's ready step and with it the cloud handoff. |
| Dotfile managers (chezmoi, yadm, stow, synced repositories) | The appended block is reverted on the next apply or sync, or lands in a repository file. |
| nix-darwin and home-manager | Startup files are read-only links into `/nix/store` and are regenerated on each switch. Appending fails, which also fails the broker. |
| Root-owned or immutable startup files (`sudo` edits, `chflags uchg`) | Appending fails, which also fails the broker. |
| A startup file that returns early or a later absolute `PATH=` | The block never runs or its prepend is overwritten. |
| `.zlogin` and macOS `path_helper` in nested login shells (tmux) | `PATH` is reordered after the block. |

### Per-prompt PATH managers

`mise activate`, direnv, asdf, and similar hooks rebuild `PATH` before every
prompt. A prepend made once at startup is moved back or removed.

### Long-lived processes

- A running tmux or screen server gives new panes the environment it started
  with.
- Terminals opened before installation are never changed.
- zsh's command hash keeps the old path in a shell that ran the agent before.

### Invocations that skip `PATH`

- Aliases to absolute paths. Claude Code's old local install writes
  `alias claude=~/.claude/local/claude`.
- `npx @openai/codex`, `bunx`, `pnpm dlx`.
- Absolute paths, `command codex`, `\codex`.
- IDE extensions (Codex and Claude Code for VS Code and Cursor, JetBrains)
  and Codex Desktop, which run their own bundled executables.
- Non-interactive contexts: scripts, Makefiles, `sh -c`, cron and launchd jobs,
  editor tasks, Emacs eshell. zsh reads only `.zshenv` there.

### Version and package managers

| Manager | Effect today |
| --- | --- |
| mise, volta, asdf, nodenv | Their shims are multiplexers (a link to the manager itself) and must never be wrapped. The real executable lives under a per-version install directory that the entry search does not include. |
| fnm | Each shell gets its own multishell directory under `~/.local/state/fnm_multishells/<pid>/bin`; there is no stable path to wrap. |
| nvm, including lazy-loading plugins | Lazy loaders define their own shell functions for global binaries. |
| Homebrew twice (`/opt/homebrew` and Rosetta `/usr/local`), MacPorts, a custom npm prefix, pnpm, bun, conda and mamba environments, devbox and flox, nix profiles | Several copies of one agent; the wrapped one is not necessarily the one that runs. Nix profile executables are often wrapper scripts, not symlinks. |

### Agent self-updates

Claude Code's native installer and Cursor's installer rewrite a
`~/.local/bin/<agent>` symlink to `versions/<v>` on each update. `npm i -g` and
`brew upgrade` reinstall the link. Each update silently replaces the entry
wrapper; `ReconcileEntry` preserves the new link as an external change.

### Permissions and system

`~/.local/share` or `~/.config` owned by root after `sudo npm i -g`, managed
(MDM) Macs, network home directories, security software that blocks writes,
and several accounts on one Mac.

## Redesign

### Principles

- Terminal coverage is a separate status from cloud readiness. A failure to
  write shell integration never fails the broker, the cloud handoff, or
  Codex Desktop.
- Coverage is reported from verification, never inferred from a successful
  write.
- Every change to a file Fennec does not own is recorded before it is made,
  marked, and reversible byte-for-byte.
- Configuration owned by another tool (nix, a dotfile manager, a read-only
  file) is never written. The user is given the exact line to add to it.
- The same plan executor serves explicit uninstall, Trash removal and
  repair.

### Stable dispatcher outside the bundle

Today every shim and wrapper embeds the path of the installed `fen`, so moving
the application, App Translocation, or an update to a different path breaks
them, and they must be rewritten whenever the application changes.

Install a small dispatcher and the cleanup executable in
`~/Library/Application Support/com.runta.fennec/bin/`, outside the bundle and
outside caches and temporary directories, so neither macOS nor cleaning tools
remove them by accident. Shims, functions and wrappers call only this stable
path. The dispatcher:

- finds the current application through the path the daemon registered and
  LaunchServices, so the integration never has to be rewritten on update;
- runs the original agent locally when the agent is not selected, when the
  application is gone (and starts cleanup), or when it is running inside a
  session the cloud computer opened on this Mac (see below);
- fails visibly, never silently locally, when a selected agent's broker is
  unreachable: it says why and, on a terminal, offers to run locally;
- resolves the original by searching `PATH` without Fennec's directories, so a
  version-manager shim (mise, volta, asdf) receives the call unchanged and
  still chooses the version.

The application refreshes both executables on each launch and checks them
against hashes in the manifest.

### Commands the cloud computer runs on this Mac

Cloud agents run local commands through the Mac relay, often as
non-interactive shells. A selected agent started that way must run on this Mac,
not be sent back to the cloud computer. The local SSH server sets a marker
variable for those sessions (as `FENNEC_LAUNCH_MODE` is handled today); the
dispatcher always runs locally when it is present, and the dispatcher also
refuses to dispatch when it is already inside a dispatched session.

### Shell code rules

The startup block only sources a Fennec-owned file, guarded by its existence,
so updates never touch the user's files again. The sourced code must:

- start no process and print nothing at shell startup (budget: under 5 ms);
- be valid in zsh, bash 3.2 and POSIX `sh`, with fish in its own file;
- survive the user's options: `set -u` (only `${name-}` expansions), `set -e`,
  `noclobber`, zsh `KSH_ARRAYS` and similar (`emulate -L zsh` inside
  functions);
- never redefine a name the user already defined as an alias or function. In
  zsh and bash an alias is expanded before any function or `PATH` lookup, so an
  aliased agent is reported as shadowed, with the alias text, instead of being
  overridden.

### Layers

1. **Shell functions (primary, interactive shells).** Define a function per
   supported agent, for example
   `codex() { fennec_dispatch codex "$@"; }`, in zsh, bash and fish. A
   function takes precedence over any `PATH` entry, so per-prompt managers,
   later `PATH=` lines, hash caches and multiplexer shims do not affect it.
   `fennec_dispatch` calls the installed `fen` by absolute path when it
   exists and otherwise falls through to `command <agent>`, so a removed
   application leaves a working shell. Do not define a function that an
   existing user alias or function already claims; report that agent as
   shadowed instead.
2. **PATH shims (non-interactive and fallback).** Keep the shims directory.
   For zsh, set `PATH` in `.zshenv` by default (decision of 2026-11-18). It is
   intrusive, but every zsh reads it, including the non-interactive shells
   that cloud agents and scripts use, so local tasks see the same commands as
   the user's terminal. The `.zshenv` part only prepends the shims directory;
   functions and hooks stay in the interactive file. Register a last-position prompt hook
   (`precmd_functions`, the end of `PROMPT_COMMAND`, `fish_prompt`) that moves
   the shims directory back to the front, and on the first prompt redefines
   an agent function that a later plugin (an nvm lazy loader) replaced.
3. **Entry wrappers (shells and tools that bypass both).** Resolve the agent
   the way the user's interactive login shell does (`zsh -ilc 'whence -ap'`,
   `bash -ilc 'type -ap'`, `fish -ilc 'type -a -p'`), and add mise, asdf,
   volta, nodenv and nix-profile install directories. Wrap each real
   installation, every installed version, never a multiplexer shim and never
   a file inside `/nix/store`. Watch the install directories (FSEvents) and
   wrap again after an agent or Node update replaces the link.
4. **Verification.** On setup, after each change, and periodically, start the
   user's login shell in a pseudo-terminal and resolve each selected agent
   (`type`, `whence -v`). Report per agent: covered by function, shim or
   wrapper; shadowed by an alias or function; not covered, with the reason
   (`mise_precedes_shims`, `startup_file_read_only`,
   `startup_file_managed_by_nix`, `unsupported_shell`, `broker_not_ready`).
   The Agents tab shows uncovered agents with a repair action or the line to
   add, and onboarding tells the user to open a new terminal and restart any
   tmux server.
5. **Agent-side notice (optional backstop).** Use Claude Code's
   `SessionStart` hook and the equivalent Codex hook, when available, to print
   one line when an agent set to the cloud computer starts locally.
6. **Diagnosis.** `fen doctor agents` prints, per agent, the user's login
   shell, the files read, the resolution chain (`type -a`, aliases,
   functions), each layer's state and the reason it is not covered, with no
   secrets, so a report needs one command instead of several screenshots.

Installation targets are chosen from the user's real configuration: the login
shell from `dscl . -read /Users/$USER UserShell`, and `ZDOTDIR` and
`XDG_CONFIG_HOME` from that login shell (`setup.LocalShellDirectories`, already
used by uninstall). A startup file is written only if it is a regular file the
user owns, is writable, and is not a link into `/nix/store` or a known
dotfile-manager source.

### Managed inventory

Replace the scattered records (`shell-files.json`, the sidecars, the ChatGPT
preference) with one versioned manifest,
`~/Library/Application Support/com.runta.fennec/integration.json`, written
atomically before each change:

- each startup-file block: path, exact inserted bytes and their hash, file
  mode and owner;
- each shim and function file;
- each entry wrapper: path, original link text and target, wrapper hash;
  the `.fennec-original.json` sidecar stays beside the entry so it can be
  restored without the manifest;
- the launchd environment: name, value set, and the value present before
  Fennec set it (normally none);
- whether ChatGPT.app was relaunched with the bridge URL;
- the cleanup agent and its executable hash.

Installation is a reconcile, not a sequence of edits: compute the desired
state, compare it with the manifest and the disk, and apply only the
difference. It runs at daemon start, after a selection change, and on file
events for the recorded startup files and agent install directories, with
backoff. Block markers carry a version so later layouts migrate in place.

At daemon start, reconcile the manifest with the disk. Repair drift Fennec
caused. When the user has removed a block or wrapper themselves, record that
and show it rather than adding it again. Migrate existing installations by
discovering current artifacts by their markers.

## Removal

### Requirements

Removing Fennec, through Uninstall or by moving the application to the Trash,
restores:

- every startup file to its content without the Fennec block, leaving the
  user's other edits untouched;
- every wrapped agent entry to its original link;
- `PATH` resolution to what it was, by removing the shims, functions and
  `shell-init.sh`;
- Codex Desktop to its local app-server: `CODEX_APP_SERVER_WS_URL` removed from
  launchd (or restored to the earlier value), and ChatGPT.app no longer running
  with the bridge URL;
- launchd to no Fennec agents.

Trash removal keeps Fennec's own data (`~/.config/fennec`, sign-in), so a
reinstall resumes; Uninstall also removes data and the cloud computer as
today.

### Detection that does not depend on the daemon

Add a second LaunchAgent, `com.runta.fennec.cleanup`, whose program is the
cleanup executable in `~/Library/Application Support/com.runta.fennec/bin/`,
outside the bundle so moving the application to the Trash does not remove it
(decision of 2026-11-18), signed with the same Team ID and checked against the
hash in the manifest. It runs at load (each
login) and through `WatchPaths` on the installed bundle's location, so a
bundle trashed while the daemon was stopped, or before a restart, is handled
at the next event or login. The daemon's existing watcher stays as the fast
path. Both use the same confirmation: the registered bundle is gone or in a
Trash, no `.fennec-update-*` replacement is in progress, LaunchServices
registers no other `com.runta.fennec` bundle, and two observations at least
two seconds apart agree.

### Cleanup order

Each step is idempotent, records its result in the manifest, and touches only
content whose hash matches what Fennec wrote.

1. **Codex Desktop.** If `launchctl getenv CODEX_APP_SERVER_WS_URL` equals the
   recorded bridge URL, run `launchctl unsetenv`, or `setenv` the recorded
   earlier value. If ChatGPT.app is running with the bridge URL in its
   environment, quit it with SIGTERM (the same graceful path as Quit,
   `restartChatGPT`) and reopen it with `open -b com.openai.codex` and no
   bridge variable. This restart is required (decision of 2026-11-18):
   restoring the user's environment is part of removal, and the bridge is
   already gone, so that instance cannot reach any app-server. Remove
   `chatgpt.json`'s token.
2. **Dispatch.** Remove `takeover.json` so any launcher that survives runs
   locally.
3. **Entry wrappers.** Restore each original link (`launcher.UninstallEntry`)
   when the entry still holds the recorded wrapper; otherwise leave it and log
   the reason.
4. **Shell integration.** Remove the marked block from every recorded file
   with an atomic rewrite preserving mode and owner; keep a copy of the
   original in the manifest directory. Remove the function file,
   `shell-init.sh` and Fennec-marked shims. Skip read-only files and record
   them.
5. **launchd.** Boot out and remove the daemon plist when it points into the
   missing bundle.
6. **Itself.** Remove the cleanup plist and executable last, after writing a
   final record to `~/Library/Logs/Fennec/cleanup.log`.

If the bundle reappears before cleanup completes (restored from the Trash), the
cleanup stops and the daemon reconciles on its next start.

## Tests

Shell fixtures run the real shells in a pseudo-terminal under a temporary
home, and assert what `type <agent>` resolves to:

- plain zsh, bash 3.2 syntax, fish;
- `ZDOTDIR` set in `.zshenv`;
- `mise activate zsh` and `--shims`, direnv, asdf, volta, fnm multishell,
  nvm lazy loading;
- a later absolute `export PATH=`, `.zlogin`, an early `return`;
- a read-only startup file, a root-owned startup file, a link into a
  read-only store;
- an alias to an absolute path, an existing user function;
- an agent self-update that replaces the entry link.

Removal scenarios, each asserting byte-for-byte restoration and the launchd
environment:

- Trash while the daemon runs; Trash while it is stopped; Trash followed by a
  restart; restore from the Trash before cleanup; an update in progress;
- startup files edited by the user after installation;
- ChatGPT.app running with and without the bridge URL; a
  `CODEX_APP_SERVER_WS_URL` the user changed after Fennec set it.

## Decisions, 2026-11-18

- Cleanup restarts a running ChatGPT.app without the bridge variable.
  Restoring the user's environment on removal is required.
- The zsh `PATH` entry is written to `.zshenv` by default, so the
  non-interactive shells that cloud agents use for local tasks resolve the
  same commands. The recursion rule keeps those sessions local.
- The dispatcher and cleanup executables live outside the bundle in
  `~/Library/Application Support/com.runta.fennec/bin/`.

## Open questions

- The cleanup executable's signature and Gatekeeper behavior outside the
  bundle must be validated on a clean Mac before shipping.
