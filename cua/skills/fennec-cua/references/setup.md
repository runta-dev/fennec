# Setup: install, permissions, connecting an agent

fennec-cua is one macOS app, `FennecCUA.app` (bundle id `com.runta.fennec.cua`), that holds the `fennec-cua` command, the daemon, the pinned `cua-driver` (input and capture) and, in the full archive, its own Chromium. macOS 13 or later on Apple silicon.

## Is it installed?

```sh
command -v fennec-cua || ls ~/.local/bin/fennec-cua ~/Applications/FennecCUA.app
fennec-cua version
fennec-cua doctor
```

## Install from the archive

The archive is `fennec-cua-<version>-macos-arm64.tar.gz` (with Chromium) or `...-core.tar.gz` (without). Nothing needs administrator rights.

```sh
shasum -a 256 -c fennec-cua-<version>-macos-arm64.tar.gz.sha256   # when the .sha256 file came with it
tar -xzf fennec-cua-<version>-macos-arm64.tar.gz
cd fennec-cua-<version>-macos-arm64
./FennecCUA.app/Contents/MacOS/fennec-cua install
```

`install` copies the app to `~/Applications/FennecCUA.app`, links `~/.local/bin/fennec-cua` to it, clears the download quarantine on the installed copy, and prints the permissions to grant and the agent setup commands. It replaces an older install only after the new copy's signature verifies, and stops that install's daemon first. Options: `--app-dir DIR`, `--bin-dir DIR`. It never edits shell profiles: if `~/.local/bin` is not on `PATH`, it prints the line to add (`export PATH="$HOME/.local/bin:$PATH"`); until then call `~/.local/bin/fennec-cua`.

Gatekeeper: development archives are signed with a local certificate and not notarized. Downloaded with a browser, they carry `com.apple.quarantine`, and macOS refuses to run them ("cannot be opened" or "developer cannot be verified"). Clear it on the extracted folder before running `install`: `xattr -dr com.apple.quarantine fennec-cua-<version>-macos-arm64`. Archives fetched with `curl` carry no quarantine. A notarized release needs none of this.

## Permissions (the user must grant them)

| Permission | Why | Pane |
|---|---|---|
| Accessibility (required) | read accessibility trees, click, type, press keys | `x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility` |
| Screen Recording (required) | window screenshots | `x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture` |
| Automation (optional) | only if macOS asks while an action talks to another app | `x-apple.systempreferences:com.apple.preference.security?Privacy_Automation` |

The grants belong to the app "Fennec CUA", not to your terminal: the daemon is always started through LaunchServices, and cua-driver runs as its child.

```sh
fennec-cua permissions            # the list above, with this install's app path
fennec-cua permissions --request  # the daemon asks macOS: shows the prompts and lists the app in the panes
fennec-cua permissions --open     # opens the two required panes
fennec-cua quit                   # after a Screen Recording change; the next command restarts the daemon
fennec-cua doctor                 # Accessibility and Screen Recording must read "ok ... granted", screen capture "ok"
```

On macOS 15 and later, the first screen capture also needs a one-time answer: `permissions --request` makes macOS show "FennecCUA is requesting to bypass the system private window picker and directly access your screen"; the user clicks Allow. Until then `doctor` reports `fail screen capture` and screenshots fail, while accessibility reads and input work. Once the grants are on, close any macOS prompt still on screen (a prompt in front blocks input to other apps: input fails with `the front window is ... of UserNotificationCenter`).

These dialogs are for the user. An agent asks the user to switch "Fennec CUA" on and to answer the prompts; it never clicks System Settings or a permission prompt itself.

## `fennec-cua doctor`

Checks, in order (one line each): `app` (and its signature), `quarantine`, `cua-driver`, `browser`, `command` (the `fennec-cua` on `PATH`), `config` (the configuration file: it loads, or there is none and defaults apply), `daemon` (it starts one unless `--no-start`), `Accessibility` and `Screen Recording` as the daemon itself sees them, `cua-driver process`, `screen capture` (a real test capture), `browser surface`, and `stop` when the user stopped fennec-cua. Each line is `ok`, `warn` or `fail`; failures carry a `fix:` line. Exit status 1 when any check fails. `--json` prints `{"healthy": bool, "checks": [{name, level, detail, fix}]}`.

## Connect an agent over MCP

`fennec-cua mcp` speaks MCP over stdio and starts the daemon on first use. Tools: `js` and `js_reset`. `fennec-cua mcp-config [claude|codex|json|http]` prints these with your path filled in.

Claude Code:

```sh
claude mcp add --scope user fennec-cua -- "$HOME/.local/bin/fennec-cua" mcp
```

Codex (`codex mcp add fennec-cua -- "$HOME/.local/bin/fennec-cua" mcp`, or in `~/.codex/config.toml`):

```toml
[mcp_servers.fennec-cua]
command = "/Users/YOU/.local/bin/fennec-cua"
args = ["mcp"]
startup_timeout_sec = 30
tool_timeout_sec = 300
```

Any stdio MCP client (Claude Desktop, Cursor, ...):

```json
{ "mcpServers": { "fennec-cua": { "command": "/Users/YOU/.local/bin/fennec-cua", "args": ["mcp"] } } }
```

Use the absolute path: GUI clients do not read your shell's `PATH`.

Streamable HTTP, for clients that cannot start a process: `FENNEC_CUA_HTTP_TOKEN=<random> fennec-cua serve --http 127.0.0.1:8765` (stop the app's daemon first with `fennec-cua quit`), URL `http://127.0.0.1:8765/mcp`, header `Authorization: Bearer <token>`. This runs the daemon inside your terminal, so macOS attributes the permissions to the terminal app; prefer stdio.

## Shell-only agents

```sh
fennec-cua js [--session NAME] [--title TEXT] [--out DIR] [--timeout 5m] [CODE | -]
fennec-cua js-reset [--session NAME]
fennec-cua js-end [--session NAME]
```

- Code comes from the single argument, or from stdin when there is none or it is `-`.
- Calls with the same session name share one REPL runtime in the daemon: bindings, element-id baselines and browser tabs survive between invocations. The session is `--session`, else `$FENNEC_CUA_SESSION`, else `default`. Use one name per task; two agents must not share a name.
- A session with no call for two hours ends by itself (its runtime and unmarked agent tabs go); `js-end` ends it now; `js-reset` keeps the session and its tabs but drops the bindings.
- Output is the REPL's text: `Wall time: ... seconds`, `Output:`, then the output parts. Images are saved as files (`--out DIR`, else the system temporary directory) and printed as `[image saved: PATH]`.
- Exit status: 0, or 1 when the code threw (the message is the first output line after `Output:`) or the command failed.
- `fennec-cua do js '{"code":"..."}'` also works but opens a fresh runtime every time: nothing persists.

## Install the skill for an agent

```sh
fennec-cua skill install --claude   # ~/.claude/skills/fennec-cua
fennec-cua skill install --codex    # $CODEX_HOME/skills/fennec-cua (default ~/.codex)
fennec-cua skill install --dest DIR # DIR/fennec-cua
fennec-cua skill path               # where the app keeps it
```

## Daemon control

```sh
fennec-cua status    # JSON: version, permissions, input mode, stopped, screen size
fennec-cua stop      # the user's emergency stop: every call is refused
fennec-cua resume    # lift it (the user decides)
fennec-cua quit      # end the daemon; the next command or MCP call starts it again
```

The daemon keeps its sockets and `daemon.log` in `~/Library/Caches/fennec/cua/`, its settings (`config.toml`, saved flows) and the browser profile in `~/Library/Application Support/fennec/cua/`. Fennec.app, if installed, uses the same daemon directory: one daemon per user answers both, whichever started first; `fennec-cua doctor` warns when the running daemon is not this app's.

## Uninstall

```sh
fennec-cua uninstall           # stops the daemon, removes the app and the link; keeps settings and data
fennec-cua uninstall --purge   # also settings, flows, browser profile, daemon files, and resets the app's Accessibility and Screen Recording grants
```

`--purge` is refused while Fennec.app is installed, because it shares that data. The browser's cookie key stays in the login Keychain as "Fennec Safe Storage".
