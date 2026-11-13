# Troubleshooting

Start with `fennec-cua doctor`; its `fix:` lines name the next step.

| Symptom | Cause and fix |
|---|---|
| `cua.getApp(...)` takes long and then throws `<App> has no open window; open one ...` although the app shows a window; `getAXState` reads nothing; screenshots fail | Accessibility (or Screen Recording) is not granted to "Fennec CUA". Run `fennec-cua doctor` first; ask the user to grant them (`fennec-cua permissions`), then `fennec-cua quit`. `cua.listApps()` still works without the grants |
| `doctor`: `fail Accessibility denied` although the user switched something on | the switch is for another app (a terminal, Fennec). It must be "Fennec CUA" (`~/Applications/FennecCUA.app`). `fennec-cua permissions --request` lists the app in the pane |
| Screen Recording granted but still denied | macOS applies it to new processes: `fennec-cua quit`, then retry |
| App was reinstalled from a newer archive and grants stopped working | a build signed with a different certificate counts as a new app for macOS: switch it off and on again in both panes |
| `fennec-cua: command not found` | `~/.local/bin` is not on PATH: use `~/.local/bin/fennec-cua` or add it to PATH |
| "cannot be opened", "developer cannot be verified", "damaged" | download quarantine on an un-notarized build: `xattr -dr com.apple.quarantine <extracted folder>` and run `install` again |
| `the fennec-cua daemon did not answer ... within 20s` | run `fennec-cua serve` in a terminal to see the error, and read `~/Library/Caches/fennec/cua/daemon.log` |
| `doctor`: `warn daemon app ... is not this app's` | Fennec.app's daemon (or an older install's) is serving: `fennec-cua quit`, then any command starts this one |
| `the configuration does not load: ...` | fix or remove `~/Library/Application Support/fennec/cua/config.toml` |
| `<id> is an invalid element ID` / `no longer in the app` | ids are only valid against the latest state: `getAXState()` again |
| `getApp("Name")` throws | use the bundle id from `cua.listApps()`, or the `.app` path |
| An app is missing from `listApps()` | only running and recently used apps are listed; `cua.getApp("/Applications/X.app")` launches any app |
| `getAXState()` shows another window than expected | it reads the app's front window; `performSecondaryAction(<window id>, "Raise")` on the window you want, or close the sheet/dialog in front |
| A click "did nothing" | read the state: input may have landed unconfirmed. Prefer `setValue` or `performSecondaryAction`; for canvas content take `getScreenshot()` and click a point |
| `stopped: ...` on every call | the user ran `fennec-cua stop` (or Fennec's Stop). Do not resume yourself; ask the user |
| Confirm errors ("cannot show a confirm dialog") | `[safety] confirm` in config.toml is not `off`; the standalone app has no confirm dialog, so set it to `off` (your own confirmation rules still apply) |
| Calls fail with `not_allowed` | the app is outside `[safety] allow` in config.toml |
| Browser calls throw `No browser is available.` | the core archive, or `FENNEC_CUA_CHROMIUM=off`; use the full archive, or drive Safari as an app |
| A site shows you signed out | the bundled browser has its own profile: `fennec-cua browser import` (user consent) or `markHandoff()` |
| `js` call ended at 90 s | split the work; use `fennec.wait` for long waits |
| Shell session lost its bindings | it idled for two hours, or the daemon restarted (`quit`, reinstall, logout): rebuild the bindings (`var app = await cua.getApp(...)`) |
| The emergency hotkey does nothing | the standalone daemon does not register the kill hotkey yet; use `fennec-cua stop` |

Files: daemon log `~/Library/Caches/fennec/cua/daemon.log`; settings `~/Library/Application Support/fennec/cua/config.toml`; app `~/Applications/FennecCUA.app`; manifest with versions and pins `~/Applications/FennecCUA.app/Contents/Resources/MANIFEST.json`.
