# fennec-cua

fennec-cua is Fennec's computer-use (CUA) host: a Go program, macOS only, that lets local agents see and drive the desktop over MCP.

- **Capability baseline:** everything [loomhand/shuttle](https://github.com/loomhand/shuttle) v0.1.50 (`6d70274`, pinned in [`tests/UPSTREAM`](tests/UPSTREAM)) can do on macOS.
- **Own design:** one MCP surface, a persistent JavaScript REPL (`js`, `js_reset`) whose `cua` object matches OpenAI Codex's computer-use interface ([docs/codex-parity.md](docs/codex-parity.md)), over an engine of six tools (stable refs, verified actions, recoverable errors), all in an independent Go implementation. shuttle (AGPL-3.0-only) is a capability reference and a black-box comparison target; no shuttle code, tests or documentation text are copied into this tree.
- **Acceptance:** our own test suite proves parity with shuttle, capability by capability ([Acceptance](#5-acceptance-parity-with-shuttle)).

Windows, Linux and Android are out of scope.

Status: P0. Done: upstream inventory, the capability catalog (422 capabilities accounting for all 1025 macOS upstream items: 900 covered, 125 excluded with reasons), the protocol types in `internal/protocol`, an MCP client and the comparison harness core in `internal/parity`. Open: the fixture app and the VM runner. No runtime code yet.

```sh
go run ./cmd/cua-upstream inventory   # the pinned release's macOS behaviours
go run ./cmd/cua-upstream coverage    # does tests/capabilities account for all of them?
```

---

## 1. The baseline: what shuttle does on macOS

```
agent ─MCP─▶ shuttle mcp (thin proxy) ─desk wire─▶ shuttle serve (one daemon per user)
                                                      ├── AX accessibility tree (desk verbs)
                                                      ├── cua-driver (MIT): input, windows, launch, capture
                                                      ├── node: gui_plan / script_run / recording drafts
                                                      └── service plugins
```

| Part | Size | Notes |
|---|---|---|
| macOS-relevant Rust | ~32k lines | `desk.rs` 8.4k, `computer.rs` 5k, `ax.rs` 1.8k, daemon, recorder, MCP |
| Runtime TypeScript | ~2.2k lines | `gui_plan` executor (1.7k) is the core |
| Upstream macOS inventory | 1025 items | 285 Node tests, 496 Rust tests, 244 release notes (`cua-upstream inventory`) |

| Area | Capability |
|---|---|
| Observe | window list (sheets and owners, other Spaces, stale apps), waiting for window changes, AX tree snapshots and text outlines (2500 nodes, 64 levels), find by name, value reads (preview, full, tail), hit tests with cover detection, link URLs, web page URL and DOM id, the Chromium web-tree switch, screenshots (screen, rectangle, window), screen stillness with volatile-region filtering |
| Act | press (AX action or hit-checked click by role), element actions, keys with one key-name vocabulary, typing (terminals, password lines, focus checks), click/scroll/drag by screen or window-image coordinates, focus, close, launch (URLs handed to the running instance) |
| Input modes | foreground, background (no focus steal; unconfirmed results flagged), auto (background, foreground on refusal), typing through a locked screen |
| Vision mode | screenshot plus indexed elements, pixel or index actions that wait for stillness, scale mapping, region zoom, `verify_hit` clicks on text fields |
| Plans | `gui_plan` steps (app, menu, click, check, set, type, key, wait, expect, verify, close) that stop at the first surprise and escalate; flows in git with parameters and an index; recorded replay (crop NCC, then grounding, then the recorded point); web steps; locate |
| Scripts | `script_run`: a TS/JS module with the desk API, timeout, stop, cancel, capped logs, model usage |
| Models | decide (yes/no and choice with probabilities), ask, ground (0–1000 mapped onto an area); one OpenAI-compatible endpoint, bring your own key; 402 handling |
| Recording | listen-only CGEventTap with AX context and crops, password redaction, a parameterised plan draft |
| Safety | stop/resume, kill hotkey, menu-bar indicator, confirm mode, app allowlist that blacks out other apps in captures, same-user peer check, serial actions with parallel reads, run ownership, busy timeout, never resend a call that may have landed |
| Extensions | script plugins, long-lived service plugins |
| Transport | MCP over stdio and Streamable HTTP (token, Origin check, sessions, cancel) |

Two facts shape the design:

1. **Input, capture and window control live in cua-driver (MIT).** fennec-cua bundles the loomhand/cua fork (it alone has `verify_hit`) and does not reimplement that layer.
2. **macOS attributes Accessibility and Screen Recording grants to an app bundle.** The daemon and cua-driver run as helpers inside the signed Fennec.app, so one set of grants covers both.

---

## 2. Script runtime: pure Go

shuttle runs scripts on a bundled Node. fennec-cua transpiles with the [esbuild](https://github.com/evanw/esbuild) Go API and runs on [goja](https://github.com/dop251/goja), both pure Go, no cgo. The desk API is a set of Go functions that return promises on a Go event loop, with no socket hop.

A spike (go 1.24, goja `f493a2b`, esbuild v0.28.2) ran TypeScript with `async`/`await`, `Promise.all`, `for await`, `Array.prototype.at` and `Object.groupBy`. goja lacks async generators; esbuild lowers them with `Supported: {"async-generator": false, "for-await": false}`. Cold transpile plus run took about 11 ms (3.7 ms of it transpiling).

- **Gains:** no 100 MB Node bundle and no process start per run. Scripts run in a VM that holds only the capabilities we hand it: no file system, no child processes, no model keys. shuttle's `script_run` runs with the user's full rights; this is stricter.
- **Costs:** no `node:*` modules and no npm imports. Timeout and stop use `vm.Interrupt()`, which also breaks infinite loops. File or network access, where a script needs it, comes as explicit, policy-checked capabilities (`fs.read` within a directory, `fetch` behind an allowlist).

The plan executor is Go inside the daemon, not script.

---

## 3. Protocol: agent-native

### 3.0 The MCP surface: a JavaScript REPL

Agents see two tools, `js` {code, title, completion_summary} and `js_reset` {}, interface-compatible with Codex's `cua_repl` server; [docs/codex-parity.md](docs/codex-parity.md) is the contract (tool shapes, result framing, the `cua` API, the accessibility state text and its diff, and the CDX parity tests). Each MCP session has one persistent goja runtime: top-level await, bindings that survive between calls until `js_reset`, `nodeRepl.write`/`emitImage` for output, a 90-second limit per call, output capped at 100,000 characters, and the run sandbox (no `require`, `process`, file system or network). The first `js` call of a session returns the API documentation, so it stays out of the system prompt; the server's instructions are one line.

```js
// js call 1 (the result starts "Wall time: 0.4120 seconds\nOutput:", then the documentation)
var app = await cua.getApp("TextEdit")   // launches in the background if needed, prints the state:
// Window: "Untitled", App: TextEdit.
// 0 standard window Untitled, Secondary Actions: Raise
// 	1 text entry area (settable) Value: , Secondary Actions: Show Menu
// ...
// js call 2: act, then read the state in the same call
await app.click(1); await app.typeText("hello"); await app.getAXState()
// The following is a diff from the previous accessibility tree for Window: "Untitled" ...
// ~	1 text entry area (settable) Value: hello, Secondary Actions: Show Menu
```

Web pages go through the reference's browser API (`cua.listBrowsers`, `getBrowser`, `createBrowserTab`, `getTab`, `listTabs`, and tabs with element-index-first input, navigation, marks and a Playwright locator subset), served from the open-source Chromium fennec-cua bundles, driven over the DevTools protocol on a private pipe ([Browser](#36-browser-bundled-chromium), [docs/codex-parity.md](docs/codex-parity.md) section 8). Each session works in an agent window of its own with a visible agent cursor; the allowlist (as the app `Chromium`), confirmations, Stop and secret redaction apply to it. The user can import their Google Chrome sign-ins into it.

```js
var tab = await cua.createBrowserTab("chromium", "https://example.com", { sessionName: "🔎 Example" })
// Browser tab: 1, Title: "Example Domain", URL: "https://example.com/".
// 0 AXWebArea Example Domain, URL: example.com/
// 	1 heading Example Domain, Value: 1
// ...
await tab.click(4); await tab.typeText(null, "hello"); await tab.getAXState()   // a diff, as for apps
```

The `cua` object (`getState`, `listApps`, `getApp`, and the app methods `getAXState`, `getScreenshot`, `click`, `drag`, `scroll`, `selectText`, `setValue`, `performSecondaryAction`, `paste`, `pressKey`, `typeText`) is built on the engine tools below through the same host the `run` tool uses, so the allowlist, the desk turn (held for the whole call), Stop, confirmations and secret redaction apply to every call. fennec-cua's own capabilities sit on a separate `fennec` global and never shadow a reference name: `fennec.status`, `fennec.act` (plans with read-back), `fennec.wait`, `fennec.observe`, `fennec.flows`, `fennec.record`, `fennec.model`, `fennec.run`, `fennec.tools`/`fennec.call` (plugins and services).

The six engine tools of 3.2 are neither listed nor callable by name: an agent session that calls `act` gets "unknown tool". Only a script plugin's own session, which presents its run's token, calls them back by name as that run. `fennec-cua do TOOL` and the scenario harness reach them through `js` (`fennec.call`, wrapped by `mcpclient.EngineTool`), so they pass the same path agents do.

### 3.6 Browser: bundled Chromium

fennec-cua ships open-source Chromium (BSD-3-Clause with its third-party notices; never Google Chrome or Chrome for Testing) and drives it over CDP. ego lite, the earlier backend, is not redistributable and is gone.

- **Build.** A pinned [chromium-browser-snapshots](https://storage.googleapis.com/chromium-browser-snapshots/index.html) build per architecture: revision, commit, version, size and SHA-256 live in `internal/chromium/pin.go` (arm64 r1743750, x64 r1743759, both 160.0.8159.0). Updating is a deliberate pin bump followed by the live tests.
- **Runtime.** The daemon starts it on the first browser call with `--remote-debugging-pipe` (no TCP port), its own profile in `~/Library/Application Support/fennec/cua/Chromium` (0700), no first-run, default-browser or Keychain prompts (mock keychain), no startup window; it supervises it (restart on the next call after a crash, a crash-loop stop, idle shutdown after ten minutes with no page, shutdown with the daemon). Lookup: `Contents/Helpers/Chromium.app` beside `Contents/MacOS/fennec-cua`, `Chromium.app` beside the binary, or `FENNEC_CUA_CHROMIUM` (`off` disables); `FENNEC_CUA_CHROMIUM_HEADLESS=1` hides its windows.
- **Agent windows.** Each MCP session gets its own window, opened in the background; its tabs, their popups and tabs the user opens there are its tabs. A visible cursor with the session name and action is drawn in the page (outside the accessibility tree and the document text) and hidden for screenshots.
- **Import from Chrome.** `fennec-cua browser profiles`, `fennec-cua browser import [--profile NAME] [--replace]`, `fennec-cua browser status` (daemon control socket, owner only; for Fennec's UI) and `fennec.browser.importFromChrome()` in the REPL copy one Chrome profile's cookies into the managed profile: the database is copied and read in Go, values are decrypted with the "Chrome Safe Storage" key, whose Keychain prompt is the user's consent, and set with `Storage.setCookies`. The key is never cached; values are never logged or returned. Passwords are not imported ([docs/codex-parity.md](docs/codex-parity.md) section 8.6).
- **Packaging.** `go run ./cmd/cua-package -stage DIR` downloads (or reuses) the pinned archive, verifies size and SHA-256, extracts `Chromium.app`, records Chromium's `LICENSE` (from the pinned commit) and its `chrome://credits` page, signs the app inside out with the Fennec signing identity (hardened runtime; JIT for the renderer and GPU helpers; library validation off for a certificate without a team), verifies the signature, starts the signed browser once, builds and signs `fennec-cua`, and writes `MANIFEST.json`. It stops instead of waiting when signing would ask for the key, and never installs anything. Snapshot builds carry no generated credits, so the packager refuses them unless `-allow-missing-credits` marks the staging as development only.

```sh
go run ./cmd/cua-package -stage /tmp/cua-stage -allow-missing-credits   # development staging
FENNEC_CUA_CHROMIUM=/tmp/cua-stage/Chromium.app go test -tags chromiumlive ./internal/browser ./internal/engine
```

### 3.1 Principles

1. **Few tools, clear intent.** About 30 shuttle verbs plus `gui_plan` and `script_run` become six tools.
2. **Observe and act close the loop.** Every action returns what changed (windows, focus, values read back), so the agent does not need a separate observation.
3. **Short, stable refs.** Elements are `e12`, stable within a view version. The text outline is the default view; structured nodes are on request.
4. **Semantic targets first, pixels last.** One `target` field takes a ref, a selector (`button "Save"`) or a point.
5. **Uncertainty is explicit.** Each step is `confirmed`, `unconfirmed` (sent, effect unproven, `retry: false`) or `refused` (nothing sent).
6. **Errors say how to recover.** `{code, message, hint, observe?}` with a stable `code`, a next-step `hint` and, where useful, the current view.
7. **One shape for one step and many.** An action is a one-step plan; a plan is actions with `expect`.

### 3.2 Engine tools

Reached from the REPL (`fennec.*` and the `cua` methods built on them), and by name for internal callers.

| Tool | Does | Replaces in shuttle |
|---|---|---|
| `observe` | `scope`: `desktop` (windows), `window` (outline with refs), `element` (value, full text), `point` (hit test); `find` for semantic search; `image`: `none`, `window`, `screen`, `region`; `vision: true` for a screenshot with numbered elements | windows, unanswered, snapshot, find, read, at, shot, screen, screenshot, computer_state, look, computer_scale |
| `act` | one or more of `press`, `set`, `type`, `key`, `click`, `scroll`, `drag`, `menu`, `focus`, `close`, `launch`, `open_url`, each with an optional `expect`; stops at the first surprise and answers the trace and the current view | press, act, key, type, click, scroll, drag, focus, close, launch, web, computer_act, gui_plan |
| `wait` | until a window appears or goes, an element appears, a value matches, or the screen is still, with a timeout | wait, still, plan waits |
| `run` | a TS/JS module whose `main(params)` gets the `cua.*` API; answers result, logs, usage | script_run, plugins |
| `flow` | `save`, `list`, `show`, `run`, `delete` saved parameterised flows; `record` `start`, `note`, `stop` into a draft | flows, reindex, record_*, drafts |
| `status` | version, permission state (Accessibility, Screen Recording), input mode, stopped or not, policy in force | status and scattered permission diagnostics |

Model calls (decide, ground, ask) are not tools: `act` grounding, visual `expect` checks and `cua.model.*` in scripts use them inside the daemon. Stop, resume, the allowlist and confirm mode are reachable from the CLI and Fennec's UI only; an agent cannot turn them off.

### 3.3 Example

```jsonc
// observe: the front window's outline by default
{"tool": "observe"}
→ {"view": "v7", "window": {"ref": "w3", "app": "TextEdit", "title": "Untitled"},
   "outline": "w3 window \"Untitled\"\n  e1 textarea \"\" focused\n  e2 button \"Save\" …",
   "windows": [{"ref": "w3", "app": "TextEdit", "title": "Untitled", "active": true}]}

// act: several steps, each verified
{"tool": "act", "steps": [
  {"set": "textbox \"Name\"", "value": "Alice"},
  {"press": "button \"Save\"", "expect": {"window_gone": "Save As"}}
]}
→ {"ok": true, "view": "v8", "steps": [
     {"ok": true, "status": "confirmed", "evidence": "Name reads \"Alice\""},
     {"ok": true, "status": "confirmed", "changed": {"closed": ["Save As"]}}]}

// a stop
→ {"ok": false, "stopped_at": 1,
   "error": {"code": "target_not_found", "message": "no button \"Save\" in w3",
             "hint": "closest: button \"Save…\" (e9)", "observe": {"view": "v8", "outline": "…"}}}
```

### 3.4 Decisions the catalog forced

Writing the catalog surfaced behaviours the first protocol draft could not express. Each is settled in `internal/protocol`:

| Behaviour | Decision |
|---|---|
| Per-call delivery mode | a step's `input` may narrow the configured mode (auto to foreground); a wider request is refused before anything is sent |
| Typing safety | `into` (a selector, which can require the secure state), `any_focus` for apps without focus, `secret` for passwords (redacted everywhere; Return only when the password did not show) |
| Addressing an app | steps take `app`, `pid` and `window` besides targets |
| Check boxes and menu toggles | `set` and `menu` take `value` as the desired state, read back afterwards |
| Verify-only steps | a step with only `expect` is a check; `expect.looks` asks the vision model |
| Recorded replay | `recorded` (crop, scale, offset, point, window size, root, element fingerprint), `expect.front` for the window seen afterwards, `web` for page and DOM id |
| Stops | `stopped_at`, `error`, `remaining` (crops stripped, secrets redacted), `caveats` |
| Chains and automatic saving | passing plans extend a session chain and save as flows by default, as shuttle does, but visibly: `chain` and `saved_as` in the result; `flow save` with `from: "last"`; a plan after a stopped flow, in its window within 10 minutes, repairs that flow |
| Finding | `observe find` answers `resolved` (name or model, with the model calls); `flow list` takes a `query` |
| Vision | an image-space point names its `view` so a later capture's scale cannot move it; every image carries `origin` and `scale`; driver sessions belong to the MCP session and end with it |
| Web tree | `observe web: true` or any web-checked step holds the Chromium page tree on for the session; it is released when the session ends |
| Extensions | service plugins answer one reply per call over MCP; byte streaming is not supported. Script plugins may be external programs: they are installed by the user, not written by the agent |
| Stricter than shuttle | off-screen points are refused, a value preview says when it was cut |
| The REPL is the only MCP surface | code as action: one `js` call reads a state, acts on several elements and reads again, where the tool surface needed a model turn per step. Our macOSWorld bench showed fennec-cua's six-tool surface spending about 17 turns and 530k tokens per task with a 25% tool error rate, mostly malformed `act` steps; a REPL call is ordinary JavaScript against a small typed API. The API is Codex's `cua_repl` interface rather than our own, because models from several vendors are post-trained on it ([docs/codex-parity.md](docs/codex-parity.md)) |
| The REPL's accessibility text | numbered elements in pre-order with role words macOS speaks, a diff after the first read through a binding, ids stable for unchanged elements; the engine reads role descriptions, identifiers, help, actions and settable values only for it (`observe detail`) |
| Steps the REPL needed | `perform` (a named accessibility action, refused when the element does not list it) and `select_text` (a character range through `AXSelectedTextRange`) joined the step vocabulary; `paste` saves and restores the pasteboard's text around Command-V sent through `act` |

Excluded on purpose (each recorded in the catalog): Node process-group signalling and a script cleanup hook (goja's interrupt replaces them), shuttle's private desk wire and its framing, Linux, Windows and Android branches that run on every platform, installers and release bookkeeping.

### 3.5 Internals

- `fennec-cua mcp` bridges stdio to the daemon's unix socket. The daemon speaks MCP JSON-RPC itself on a long-lived, multiplexed connection with cancellation; there is no second private wire.
- Scheduling keeps shuttle's guarantees: actions run one at a time, reads run alongside, an `act` plan or a `run` owns the desk until it ends, a busy wait times out, a call that may have landed is never resent, and a dropped connection cancels its call.
- Background input, the allowlist and confirm mode are one policy layer that every entry point (MCP, scripts, CLI) passes.

---

## 4. Implementation

```
cua/
  go.mod                      module fennec/cua (its own module)
  cmd/fennec-cua/             mcp | serve | stop | resume | status | do <tool> <json> | browser profiles|import|status
  cmd/cua-upstream/           upstream inventory and catalog coverage
  cmd/cua-package/            stage fennec-cua and the pinned, signed Chromium.app for the Fennec bundle
  internal/mcpserver/         MCP JSON-RPC: stdio bridge, unix socket, Streamable HTTP
  internal/daemon/            sessions, scheduling, cancellation, peer check (getpeereid)
  internal/policy/            stop/resume, allowlist with capture blackout, confirm, input modes
  internal/ax/        (cgo)   batched AX attribute reads, observers, CGWindowList, Spaces, web-tree switch
  internal/view/              view versions, refs, outline rendering, selector parsing and matching
  internal/driver/            cua-driver client and sessions
  internal/actions/           press, set, type, key, click, scroll, drag, menu, focus, close, launch, read-back
  internal/vision/            scaling, encoding, element numbering, coordinate mapping, stillness, NCC matching
  internal/cdp/               DevTools protocol client over a NUL-framed pipe (sessions, events)
  internal/chromium/          the pinned Chromium build, lookup, launch arguments, supervision
  internal/browser/           the REPL's browser API over CDP: tabs, state text and diff, locators, keys, agent cursor, Chrome import
  internal/chromeimport/ (cgo) Chrome profiles, a read-only SQLite reader, cookie decryption, the Keychain read
  internal/models/            OpenAI-compatible chat: decide, ground, ask, usage, 402
  internal/plan/              multi-step executor, expect, trace, escalation
  internal/flows/             flow store (git), parameters, recording drafts
  internal/record/    (cgo)   listen-only CGEventTap
  internal/script/            esbuild + goja, cua.* bindings, interrupt, capped logs; the js REPL (persistent runtime, cua/fennec/nodeRepl, AX state text and diff)
  internal/extensions/        service plugins
  internal/hotkey/    (cgo)   Carbon global hotkey
  internal/upstream/          upstream inventory
  internal/capability/        capability catalog and coverage check
  testapp/                    Swift/AppKit fixture app for scenario tests
  tests/capabilities/         the capability catalog, one YAML file per domain
  tests/UPSTREAM              the pinned shuttle release
```

| Concern | Choice |
|---|---|
| Concurrency and cancellation | goroutines and `context`, carried from an MCP cancel to driver calls and `vm.Interrupt()` |
| AX, CoreFoundation, AppKit | cgo over the C APIs with one retain/release discipline; AppKit only for the little main-thread UI |
| Images | `image/png`, `image/jpeg`, `golang.org/x/image/draw`; WebP only if a spike shows it pays |
| JSON | `encoding/json`; Go types are the schema source and generate MCP `inputSchema` |
| Indicator and confirm dialog | Fennec's Swift UI through its local control interface |

| Phase | Scope | Exit |
|---|---|---|
| P0 | capability catalog, fixture app, comparison harness, protocol schema | `cua-upstream coverage` passes; schema reviewed |
| P1 | daemon, MCP, scheduling, policy, cua-driver client, `status`, capture | transport and scheduling tests pass |
| P2 | `observe` | observe capabilities pass and match shuttle |
| P3 | `act`, `wait`, input modes | act capabilities pass and match shuttle |
| P4 | vision mode, models, recorded replay | their capabilities pass |
| P5 | `run`, `flow`, recording | their capabilities pass |
| P6 | hotkey, indicator, confirm, extensions, Fennec.app packaging | full catalog, E2E parity, manual safety sign-off |

---

## 5. Acceptance: parity with shuttle

The REPL surface has its own contract and tests: [docs/codex-parity.md](docs/codex-parity.md) section 9 lists CDX-01 to CDX-23; the unit parts run on fakes in `internal/engine/cdx_test.go`, the live parts (marked TODO there) in the VM.

### 5.1 The catalog is the contract

Each entry of `tests/capabilities/*.yaml` (`CAP-XXX-001`) states a behaviour in our own words, the surface that provides it, how it is compared (`oracle`: the same scenario runs against shuttle too; `spec`: asserted from the description), its fennec-cua tests (`planned:<name>` until written) and the upstream inventory ids it accounts for. An upstream item fennec-cua deliberately does not reproduce goes under `excluded` with a reason, such as being folded into the new protocol or superseded by the script sandbox.

`cua-upstream coverage` fails unless every macOS item of the pinned release (test names and release notes) is covered or excluded, and every reference is a known macOS item.

### 5.2 Test layers, all written here

| Layer | What | Where |
|---|---|---|
| T1 unit | fake AX backend and fake driver behind Go interfaces: selectors, matching, refs, stillness, coordinates, scheduling, the plan executor, script bindings; upstream edge cases (duplicate titles, stale windows, sheet owners, terminals without echo, read-back mismatches) rewritten for our protocol | any Mac; never touches the desktop |
| T2 scenario | the `testapp/` fixture app: forms, sheets, menu toggles, popups, slow dialogs, live-reordering lists, password fields, a web view, canvas buttons, animated regions; Go tests drive fennec-cua over MCP and assert against the state the app reports on a local socket | macOS VM |
| T3 comparison | each scenario through two adapters, fennec-cua and shuttle v0.1.50, compared on outcome (success or failure class, final app state, no stray input), not on fields; every `oracle` capability must agree | same VM, same cua-driver |
| T4 real apps | Calculator, TextEdit, Finder, System Settings, Safari, Chrome (web tree), Terminal (background typing, password lines), through both adapters | VM |
| T5 end to end | 30–50 agent tasks (forms, menus, browser sign-in, record and replay, scripts), the same agent and model against each backend, N runs each | VM, nightly |
| T6 performance and safety | p50/p95 of observe and act, `run` cold start; stop during a run, hotkey, allowlist blackout, confirm, peer check | VM plus a manual checklist |

### 5.3 Exit criteria

- T1 and T2 green; T3 and T4 agree with shuttle on every `oracle` scenario. A disagreement is accepted only where fennec-cua is safer or more correct (shuttle sends stray input, fennec-cua refuses), recorded in the catalog.
- T5: per task, a success rate no lower than shuttle's and no more tool calls on average.
- T6: latency no worse than shuttle's; every safety item passes.
- Per the repository's AGENTS.md, final evidence comes from the fennec-cua installed in Fennec.app, not from builds or unit tests alone.

### 5.4 Following upstream

`tests/UPSTREAM` names the release we measure against. For each new shuttle release, bump the pin, run `cua-upstream coverage`, and turn every new unaccounted item into catalog work; T3 and T4 move to the new release too.

---

## 6. Development

Unit tests (`go test ./...`) use fakes and never touch the desktop. Anything that sends input runs in a tart macOS VM, never on a developer's own desktop: tests click, type and steal focus hundreds of times, need a clean, repeatable state, and run shuttle and fennec-cua side by side.

tart is a development dependency:

```sh
# from https://github.com/cirruslabs/tart/releases (not in Homebrew any more)
go run ./cmd/cua-vm create            # clone the VM; the first run downloads the image
go run ./cmd/cua-vm up                # boot it headless
go run ./cmd/cua-vm fixture           # build CUAFixture and install it with its grants
go run ./cmd/cua-vm test -tags axlive ./internal/desk -- -test.run TestLive
go run ./cmd/cua-vm down
```

`cua-vm test` cross-builds test binaries on the host, copies them into the VM and runs them in its desktop session, where the accessibility API reaches the windows. Grants (`cua-vm grant`) edit the VM's TCC database, which needs SIP disabled in the VM.

Read-only checks against the host's own desktop are allowed (`go test -tags axlive ./internal/ax ./internal/desk`): they list windows and read trees, and send nothing.

The live scenario suite (`go run ./cmd/cua-vm test -tags scenario ./internal/scenario -- -test.timeout 3h`) runs every catalog scenario against fennec-cua and shuttle in the VM.

### Benchmark

`cmd/cua-bench` runs the macOSWorld tasks listed in `bench/macosworld.txt` against cua-driver, shuttle and fennec-cua (through its REPL surface), one fresh tart clone per trial, all driven by the same host-side agent loop and model:

```sh
go run ./cmd/cua-bench golden                                   # build the golden image once
go run ./cmd/cua-bench golden -refresh-fennec                    # after a fennec-cua change: replace only its binary
go run ./cmd/cua-bench run -macosworld PATH -trials 3 -concurrency 2 -results bench-results
go run ./cmd/cua-bench web -results bench-results                # live results page on 127.0.0.1:8090
go run ./cmd/cua-bench export -results bench-results -out DIR    # static page + data.json
```

The results page (`bench/index.html`, embedded in the binary) shows the ranking, per-app and per-task scores, and the live feed; `web` also serves each run's transcript and final screenshot. macOSWorld is CC BY-NC 4.0: task texts and grading commands stay in the local checkout and never reach the page or the repository.

## 7. Third-party components

| Component | License | Use |
|---|---|---|
| cua-driver (loomhand/cua fork) | MIT | input, windows, capture; pinned by sha256 |
| goja | MIT | script engine |
| esbuild | MIT | TypeScript transpiling |
| gopkg.in/yaml.v3 | MIT, Apache-2.0 | catalog files |
| golang.org/x/crypto (ssh) | BSD-3-Clause | the test VM tool |
| tart | FSL-1.1-ALv2 (internal use permitted; Apache-2.0 after two years) | development only: the test VM, never shipped |
| Chromium (chromium-browser-snapshots, pinned in `internal/chromium`) | BSD-3-Clause, plus the third-party licences in its `chrome://credits` | the agent browser, shipped as `Contents/Helpers/Chromium.app` with `licenses/Chromium-LICENSE.txt` and `Chromium-credits.html` |

The CDP client is fennec-cua's own (no chromedp/cdproto dependency). The repository's `THIRD_PARTY_NOTICES.md` lists Chromium; update it with every pin bump.
