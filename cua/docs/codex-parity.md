# Codex cua_repl parity

fennec-cua's only MCP surface is a persistent JavaScript REPL that is
interface-compatible with OpenAI Codex's computer-use server (`cua_repl`, the
`unified-computer-use` plugin of the ChatGPT app). Models from several vendors
are post-trained on that interface: the tool names, the `cua` object, the
method signatures, the element numbering and the text of the accessibility
state. A model that meets the same interface uses it correctly; a model that
meets a look-alike guesses and misuses it. This document is the contract.
Anything a model can observe through the tools must match it.

## Reference

| Item | Value |
|---|---|
| Reference build | ChatGPT.app 26.1106.51428, `unified-computer-use` plugin 26.1106.51428 |
| Packages | `@oai/cua` 0.2.6 (the `cua` API, "tinysky-alt"), `@oai/cua-repl` 0.1.3 (the MCP server) |
| Scope | macOS native apps (`CUA_REPL_ENABLED_SURFACES=computer`). Browser surfaces are out of scope (see "Not supported") |
| Evidence | the packages' shipped type declarations and docs, and recorded Codex sessions on this machine (tool calls and their outputs) |

Only the interface is matched. fennec-cua's implementation, its own
documentation text and its tests are written independently; no Codex code or
documentation is copied into this repository. When the reference build
changes, re-check every row below against the new build and record the new
version here.

## 1. MCP tools

`tools/list` returns exactly these tools, and nothing else:

| Tool | Arguments | Behaviour |
|---|---|---|
| `js` | `code: string`, `title: string`, `completion_summary: string` (all optional in the schema; a call carries `code` + `title`, or `completion_summary` alone) | Runs `code` in the session's persistent runtime. `title` is a short present-progressive label for a person watching ("Checking the form"); it never changes behaviour. A call with only `completion_summary` records a three-to-six word past-tense headline of the finished UI task and returns `Completion summary received.` |
| `js_reset` | none | Discards the runtime and every binding. Apps, windows and their state are untouched. The next `js` call starts a fresh runtime and shows the first-use documentation again. |

`turn_ended` exists in the reference as a host-to-server notification that the
model's turn is over; it is not model-facing. fennec-cua accepts it (it ends
any input hold and resets per-turn state) but leaves it out of the tool list
the model sees, matching the reference's `enabled_tools`/`omit_tools_from`.

Server `instructions`: one short line saying UI automation goes through the
`cua` API in the REPL. The full API documentation is not in the instructions or
the tool description; it is returned in the result of the first `js` call of a
session (section 4).

## 2. Runtime

| Property | Required behaviour |
|---|---|
| Persistence | One runtime per MCP session. Top-level `var`, `let`, `const`, `function` and `class` bindings made in one call are visible in the next (`var app = await cua.getApp("Notes")` then `await app.click(3)` in a later call). |
| Top-level await | Allowed in every call. |
| Last value | Output comes from `nodeRepl.write`, `nodeRepl.emitImage`, `console.*` and the self-displaying APIs (section 3.5). Recorded sessions show no echo of a last expression's value (`var x = await cua.getApp(…)` prints only the app's state); whether a bare expression such as `1 + 1` is echoed is unverified. |
| Globals | `cua`, `nodeRepl`, `console`, `setTimeout`, timers. No `require`, `process`, file system, environment or network access. |
| Errors | An uncaught error ends the call; the output is whatever was written before it followed by `Error: <message>` (and the error's extra lines). The tool result has `isError` set. Bindings made before the error stay. |
| Time | Each call has a wall-clock limit; when it is exceeded the call ends with an error and the runtime stays usable. Stop (the user's kill switch) cancels a call in flight. |
| Output cap | Text output is capped (the reference caps the tool at 25k tokens); longer output is cut with a note saying how much was dropped. |

### Result framing

Every `js` result starts with a text part:

```
Wall time: 0.5033 seconds
Output:
```

followed by the call's output as text parts, with images as MCP image content
in the order they were emitted. Four decimals on the wall time.

## 3. The `cua` API (macOS)

Signatures in TypeScript notation. Every name, parameter order, option name
and default below is part of the contract.

### 3.1 Types

```ts
type Vec2 = [x: number, y: number];          // screenshot pixel coordinates of the app's window image
type ObservationOptions = { emit?: boolean };  // emit: false suppresses the automatic display
type StateOptions = ObservationOptions & { disableDiffing?: boolean };
type StateAndScreenshot = { state: string; screenshot?: Uint8Array };
type PasteOptions = { format?: "text" | "md" | "html" };
type MouseButton = "left" | "right" | "middle" | "l" | "r" | "m";
type ClickOptions = { mouseButton?: MouseButton; clickCount?: number; key?: string; durationMs?: number };
type PressKeyOptions = { durationMs?: number };
type Direction = "up" | "down" | "left" | "right" | "u" | "d" | "l" | "r";
type SelectionType = "text" | "cursor_before" | "cursor_after";
type SelectTextOptions = { prefix?: string; suffix?: string; selectionType?: SelectionType };
type WindowInfo = { id: number; app: string; title?: string };
type AppInfo = { id: string; displayName?: string; lastUsedDate?: string; useCount?: number; isRunning?: boolean; windows?: WindowInfo[] };
type State = { apps: AppInfo[]; browsers: []; errors?: string[] };
```

`AppInfo.id` is the bundle identifier.

### 3.2 `cua`

| Member | Behaviour |
|---|---|
| `cua.getState(options?: ObservationOptions): Promise<State>` | Inventory of apps (running and recently used) with their windows. Displays the result as JSON unless `emit: false`. `browsers` is always `[]`. Partial failures go in `errors`. |
| `cua.getApp(target: string): Promise<App>` | `target` is a display name, a full `.app` path or a bundle id. Launches the app in the background if it is not running (does not steal focus). Displays the app's initial accessibility state. If a display name does not resolve, the error says to retry with a bundle id from `listApps()`. |
| `cua.listApps(options?: ObservationOptions): Promise<AppInfo[]>` | Same app inventory as `getState().apps`; displays it unless `emit: false`. |
| `cua.computer.target` | `"mac"`. |
| `cua.computer.launch_app?({ app })` | Not provided on macOS (the reference provides it on Linux); `getApp` launches. |

### 3.3 `App`

| Method | Behaviour |
|---|---|
| `getAXState(options?: StateOptions): Promise<string>` | Waits for the UI to settle, reads the app's front window and its menu bar, and returns the state text (section 5). After the first full state of a binding it returns a diff unless `disableDiffing: true`. Displays the text unless `emit: false`. |
| `getScreenshot(options?: ObservationOptions): Promise<Uint8Array>` | PNG of the app's window, without the pointer. Displays it as an image unless `emit: false`. |
| `getAXStateAndScreenshot(options?: StateOptions): Promise<StateAndScreenshot>` | Both, from one settle. |
| `click(target: number \| Vec2, options?: ClickOptions)` | A number is an element id from the latest state; a pair is a point in the window screenshot's pixel space. `clickCount` 2 double-clicks; `mouseButton` "right" right-clicks; `key` holds a modifier (e.g. "shift") across the clicks; `durationMs` holds each press. |
| `drag(from: Vec2, to: Vec2)` | Press at `from`, move, release at `to`. |
| `scroll(target: number \| Vec2, direction: Direction, distance?: number \| { pixels: number })` | Scrolls the element or point; a number distance is pages (default 1). |
| `selectText(elementIndex, text, options?)` | Selects `text` inside the editable element; `prefix`/`suffix` disambiguate; `selectionType` selects the text or places the caret before/after it. |
| `setValue(elementIndex, value: string)` | Sets the element's value through accessibility (text fields, sliders, steppers, pop-ups that accept it). |
| `performSecondaryAction(elementIndex, action: string)` | Performs an accessibility action listed for that element under `Secondary Actions` (e.g. "Raise", "Cancel", "Increment", "Show Menu"). An unlisted name is an error naming the listed ones. |
| `paste(text, options?: PasteOptions)` | Puts `text` on the pasteboard, pastes into the focused element, then restores the previous pasteboard. |
| `pressKey(key: string, options?)` | xdotool-style keys and chords: "a", "Return", "Tab", "Escape", "BackSpace", "Up", "Page_Down", "space", "KP_0", "super+c", "shift+Tab", "ctrl+alt+Delete". `super` is Command. |
| `typeText(text: string)` | Types into the focused element. |

Every input method resolves to `undefined` and does not display anything.
Element ids refer to the latest state read through the binding; an id that is
no longer present is an error telling the model to read the state again.

### 3.4 `nodeRepl`

| Member | Behaviour |
|---|---|
| `nodeRepl.write(value)` | Appends text output; non-strings are rendered (objects as readable text; use `JSON.stringify` for JSON). |
| `nodeRepl.emitImage(image)` | Appends an image: a `data:` URL, a `file:` URL (refused: no file system), PNG/JPEG/WebP bytes, or `{ bytes, mimeType }`. |

### 3.5 Self-displaying calls

`getAXState`, `getScreenshot`, `getAXStateAndScreenshot`, `cua.getState`,
`cua.getApp`, `cua.listApps` display their own result. Writing their return
value again duplicates it. `{ emit: false }` turns the display off for the
observation and discovery methods.

## 4. First-use documentation

The first `js` call of a session (and the first after `js_reset`) prepends the
API documentation to its output, then the call's own output. The
documentation covers, in this order: what the tool is for and when to prefer
other tools; the API (section 3, as TypeScript); how to open an app; the
working loop (act, then read the state in the same call; trust the internal
settle instead of sleeping; prefer element ids over coordinates; stop once the
result is visible); output rules (section 3.4, 3.5); key syntax and per-method
notes; and the confirmation policy (section 7). The wording is fennec-cua's
own.

## 5. Accessibility state text

### 5.1 Full state

```
Window: "Runta CUA Fixture", App: Runta CUA Fixture.
0 standard window Runta CUA Fixture, Secondary Actions: Raise
	1 text Runta CUA Fixture
	2 button Start trial (evaluator)
	3 text Evaluator: press Start when ready
	4 close button
	5 zoom button (disabled)
	6 minimize button
8 menu bar
	9 Runta CUA Fixture

The focused UI element is 0 standard window Runta CUA Fixture, Secondary Actions: Raise
```

Rules:

- Header `Window: "<title>", App: <app name>.` (title may be empty: `Window: ""`).
- One element per line: tab indentation by depth, the element id, then the
  role description in lower-case words as macOS speaks it ("standard window",
  "button", "text field", "pop up button", "scroll area", "row", "cell",
  "menu bar", "text entry area", "close button", "HTML content").
- Then, when present and in this order: states in parentheses
  (`(selected)`, `(disabled)`, `(settable)`, `(selectable)`, `(expanded)`,
  `(collapsed)`, `(checked)`), the name (title), `Description: …`, `ID: …`
  (accessibility identifier), `Help: …`, `URL: …`, `Value: …`,
  `Secondary Actions: A, B`. Fields after the name are joined with `, `.
- Ids are assigned in pre-order from 0; the window subtree comes first, the
  app's menu bar after it (top-level menu titles only while menus are closed;
  an open menu's items appear as children).
- Unnamed, actionless layout groups are elided and their children lifted.
- A blank line, then `The focused UI element is <that element's line text>`.

### 5.2 Diff

After the first full state of a binding, `getAXState()` returns:

```
The following is a diff from the previous accessibility tree for Window: "Runta CUA Fixture" with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.
Removed element IDs: 2-3, 7
~	1 text Runta CUA Fixture Ready Runtime name
+	10 text field (settable) Description: Runtime name, Value: old-runtime
+	11 text Region
The focused UI element is 10 text field (settable) Description: Runtime name, Value: runta-27611
```

Rules: ids of unchanged elements are stable within a binding; new elements get
ids above every id used so far; `~` lines show the element's whole new line;
`+` lines keep their depth indentation after the marker; the `Removed element
IDs:` line is omitted when nothing was removed; a window change (another
window comes to the front) yields a full state with the new header instead of
a diff; when nothing changed the text says so in one line (the reference's
exact wording for this case has not been observed in recorded sessions yet;
verify it before freezing CDX-12).

## 6. fennec-cua capabilities beyond the reference

fennec-cua's other capabilities stay reachable from the REPL, but never under
a name the reference uses and never by changing a reference method's
behaviour. They live on one separate global, `fennec`, documented in its own
section after the reference API in the first-use documentation:

| Namespace | Capability |
|---|---|
| `fennec.status()` | permissions, input mode, daemon and driver health |
| `fennec.flows` | saved flows: list, run with params, save from steps |
| `fennec.record` | record and replay a demonstration |
| `fennec.model` | ask / decide / ground with the configured vision model |
| `fennec.wait(app, condition)` | wait for a window, element or value with a timeout |
| `fennec.act(app, steps)` | the multi-step plan executor with read-back verification (`confirmed` / `unconfirmed` / `refused` per step) |

Safety is the same whichever entry point is used: the allowlist, the desk
turn, Stop, confirmation of risky steps, secret redaction, the hit check before
a click, and refusing input into the wrong window apply to every `cua` and
`fennec` call.

## 7. Confirmations

The reference ships a confirmation policy in its first-use documentation
(hand-off required; always confirm at action time; pre-approval works; no
confirmation needed). fennec-cua's documentation states an equivalent policy in
its own words, and the daemon enforces its configured confirmations
independently of what the model decides.

## 8. Browser surface (planned: ego lite)

Phase 1 ships the native-app surface only: the browser APIs throw
`Error: browser surfaces are not enabled; use cua.getApp("<browser>")`,
matching the reference's behaviour when a surface is disabled, and
`getState().browsers` is `[]`.

Phase 2 implements the reference's browser API on top of ego lite
(`com.citrolabs.ego.lite`, a Chromium browser built for agents), through its
public SDK only: `ego-browser nodejs` runs a bridge script in ego's own Node
runtime, and the script connects back to a unix socket the daemon listens on
(the runtime does not forward stdin). One bridge per REPL session. Measured on
ego lite 0.6.2.9: the bridge connects in about 120 ms; a navigation takes about
170 ms; a snapshot or an input action takes 2-25 ms. ego's internal wire
protocol is undocumented and versioned with the app, so it is not used.

| Reference API | ego lite |
|---|---|
| `cua.listBrowsers()`, `cua.getBrowser({ id })` | one browser, id `"ego"`; `"chrome"` resolves to it as well, since models default to that id |
| `cua.createBrowserTab(id, url, { sessionName, visible })` | `taskSpace(sessionName)`, then its page `p1` (or `newPage()`) and `goto(url)`; agent tabs live in their own task space, apart from the user's tabs |
| `cua.getTab(id \| { url }, { browser })`, `cua.listTabs()` | `task.tabs()`, `adopt()` for an unmanaged tab, `listTaskSpaces()` |
| `tab.getAXState()` | `page.cdp("Accessibility.getFullAXTree")` rendered in the section 5 format (numeric ids on every line, role, name, value, URL), with the same diff rules. ego's own `snapshot()` numbers only interactive nodes and omits labels, so it is not used for this |
| `tab.getScreenshot()` | `page.screenshot()` |
| `tab.click(id \| [x, y], options)` | element centre from the DOM box model, then `page.mouse.click(x, y, { label })`, which keeps ego's visible agent cursor and its automation masking |
| `tab.typeText` / `pressKey` / `paste` (element index or `null` first) | focus the element, then `keyboard.type` / `press` (xdotool keys mapped to Playwright names) / `paste` |
| `tab.scroll`, `drag`, `setValue`, `selectText` | `mouse.wheel`, `mouse.down/move/up`, `fill`, a DOM selection via `evaluate` |
| `tab.goto` / `back` / `forward` / `reload` / `close` | the page methods of the same names (`back`/`forward` through history) |
| `tab.markHandoff()` | `task.handOff()`; the session resumes the same space afterwards |
| `tab.playwright` locators (`getByRole`, `getByText`, `locator`, `domSnapshot`) | a documented subset mapped to ego's `loc=role:`, `text=` and CSS selectors |

Open points before phase 2: ego lite in the tart image for live tests and the
bench (licence and install), routing every browser action through the same
safety checks (allowlist, confirmations, secret redaction, Stop), and the
exact text of the reference's tab state, to be checked against recorded Codex
sessions that drove Chrome.

## 9. Not supported

| Reference API | fennec-cua |
|---|---|
| `typeCharacters`, `listWindows`, `getApp({ windowId })`, `screenshotId` | Linux/Windows only in the reference; absent here as there. |
| `"iab"`, `"dot"`, `"mcpapps"` browsers | ChatGPT-app surfaces; not available outside it. |

## 10. Parity tests

Each row is a test that must exist and pass. Unit tests run on fakes; `live`
tests run in the tart VM against CUAFixture and built-in apps.

| ID | Checks | Kind |
|---|---|---|
| CDX-01 | `tools/list` is exactly `js`, `js_reset`; schemas as section 1 | unit |
| CDX-02 | result framing `Wall time: N.NNNN seconds\nOutput:` | unit |
| CDX-03 | bindings persist across calls (`var`, `let`, `const`, `function`, `class`); top-level await | unit |
| CDX-04 | `js_reset` drops bindings and re-shows the documentation; apps untouched | unit |
| CDX-05 | error output `Error: …`, `isError`, session still usable | unit |
| CDX-06 | `completion_summary`-only call answers `Completion summary received.` | unit |
| CDX-07 | first-use documentation shown once per session, covering section 4's topics | unit |
| CDX-08 | `nodeRepl.write` / `emitImage` (data URL, bytes, `{bytes, mimeType}`); `file:` refused | unit |
| CDX-09 | self-displaying calls display once; `emit: false` suppresses | unit |
| CDX-10 | full state format (section 5.1) on a fixed fake tree, byte for byte | unit |
| CDX-11 | diff format (section 5.2): removed ranges, `~`, `+`, stable and fresh ids, focused line | unit |
| CDX-12 | `disableDiffing`, window change gives a full state, no-change line | unit |
| CDX-13 | `click(id)`, `click([x, y])` in screenshot pixels, `clickCount`, `mouseButton`, `key` | unit + live |
| CDX-14 | `setValue`, `selectText` (prefix/suffix/selectionType), `performSecondaryAction` with an unlisted name refused | unit + live |
| CDX-15 | `pressKey` xdotool syntax table, `typeText`, `paste` restoring the pasteboard | unit + live |
| CDX-16 | `scroll` by element and point, pages and `{ pixels }`; `drag` | unit + live |
| CDX-17 | `getApp` by name, path and bundle id; background launch keeps focus; unresolved name hint | unit + live |
| CDX-18 | `getState` / `listApps` shapes; `browsers: []` | unit + live |
| CDX-19 | browser APIs throw the documented error | unit |
| CDX-20 | stale element id is an error asking for a fresh state | unit |
| CDX-21 | safety: allowlist, Stop mid-call, confirmation, secret redaction, wrong-window refusal through `cua` calls | unit + live |
| CDX-22 | sandbox: no `require`, `process`, file system | unit |
| CDX-23 | the macOSWorld bench runs fennec-cua through this surface (`cmd/cua-bench`) | live |

## 11. Updating

When the reference build changes: diff the shipped type declarations and docs
of `@oai/cua` and `@oai/cua-repl` against the version in section "Reference",
and compare fresh recorded outputs against section 5. Update this document
first, then the implementation and the CDX tests.
