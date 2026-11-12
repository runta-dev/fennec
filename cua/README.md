# fennec-cua

fennec-cua is Fennec's computer-use (CUA) host: a Go program, macOS only, that lets local agents see and drive the desktop over MCP.

- **Capability baseline:** everything [loomhand/shuttle](https://github.com/loomhand/shuttle) v0.1.50 (`6d70274`, pinned in [`tests/UPSTREAM`](tests/UPSTREAM)) can do on macOS.
- **Own design:** an agent-native protocol (six tools, stable refs, verified actions, recoverable errors) and an independent Go implementation. shuttle (AGPL-3.0-only) is a capability reference and a black-box comparison target; no shuttle code, tests or documentation text are copied into this tree.
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

### 3.1 Principles

1. **Few tools, clear intent.** About 30 shuttle verbs plus `gui_plan` and `script_run` become six tools.
2. **Observe and act close the loop.** Every action returns what changed (windows, focus, values read back), so the agent does not need a separate observation.
3. **Short, stable refs.** Elements are `e12`, stable within a view version. The text outline is the default view; structured nodes are on request.
4. **Semantic targets first, pixels last.** One `target` field takes a ref, a selector (`button "Save"`) or a point.
5. **Uncertainty is explicit.** Each step is `confirmed`, `unconfirmed` (sent, effect unproven, `retry: false`) or `refused` (nothing sent).
6. **Errors say how to recover.** `{code, message, hint, observe?}` with a stable `code`, a next-step `hint` and, where useful, the current view.
7. **One shape for one step and many.** An action is a one-step plan; a plan is actions with `expect`.

### 3.2 Tools

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
  cmd/fennec-cua/             mcp | serve | stop | resume | status | do <tool> <json>
  cmd/cua-upstream/           upstream inventory and catalog coverage
  internal/mcpserver/         MCP JSON-RPC: stdio bridge, unix socket, Streamable HTTP
  internal/daemon/            sessions, scheduling, cancellation, peer check (getpeereid)
  internal/policy/            stop/resume, allowlist with capture blackout, confirm, input modes
  internal/ax/        (cgo)   batched AX attribute reads, observers, CGWindowList, Spaces, web-tree switch
  internal/view/              view versions, refs, outline rendering, selector parsing and matching
  internal/driver/            cua-driver client and sessions
  internal/actions/           press, set, type, key, click, scroll, drag, menu, focus, close, launch, read-back
  internal/vision/            scaling, encoding, element numbering, coordinate mapping, stillness, NCC matching
  internal/models/            OpenAI-compatible chat: decide, ground, ask, usage, 402
  internal/plan/              multi-step executor, expect, trace, escalation
  internal/flows/             flow store (git), parameters, recording drafts
  internal/record/    (cgo)   listen-only CGEventTap
  internal/script/            esbuild + goja, cua.* bindings, interrupt, capped logs
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

## 6. Third-party components

| Component | License | Use |
|---|---|---|
| cua-driver (loomhand/cua fork) | MIT | input, windows, capture; pinned by sha256 |
| goja | MIT | script engine |
| esbuild | MIT | TypeScript transpiling |
| gopkg.in/yaml.v3 | MIT, Apache-2.0 | catalog files |

Update the repository's `THIRD_PARTY_NOTICES.md` before the first release.
