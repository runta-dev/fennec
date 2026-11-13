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
| Scope | macOS native apps (`CUA_REPL_ENABLED_SURFACES=computer`) and browser tabs, which fennec-cua serves from its bundled Chromium (section 8). The ChatGPT-app browsers are out of scope (see "Not supported") |
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
| Errors | An uncaught error ends the call. Its message alone (no `Error:` prefix, no name, no stack) is the first part after the framing, before the first-use documentation and whatever the call wrote (section 5.3). The tool result has `isError` set. Bindings made before the error stay. fennec-cua's own failures (timeout, Stop, reset, configuration) keep an `Error:` prefix. |
| Time | Each call has a wall-clock limit; when it is exceeded the call ends with an error and the runtime stays usable. Stop (the user's kill switch) cancels a call in flight. |
| Output cap | Text output is capped (the reference caps the tool at 25k tokens); longer output is cut with a note saying how much was dropped. |

### Result framing

Every `js` result starts with a text part:

```
Wall time: 0.5033 seconds
Output:
```

followed by the error of a failed call, the first-use documentation on a
fresh runtime, then the call's output as text parts, with images as MCP image
content in the order they were emitted. Four decimals on the wall time (801 of
801 recorded results; the line `Output:` always follows). The recorded results
sometimes join consecutive displays in one text part with no separator
(`…Raise[{"displayName":…`) and sometimes split them; fennec-cua keeps one
part per display.

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
type BrowserInfo = { id: string; name?: string; family?: string; type?: "iab" | "extension" | "cdp" | "mcpapps" };
type TabInfo = { id: string; providerTabId?: string; title?: string; url?: string; browserId: string };
type State = { apps: AppInfo[]; browsers: (BrowserInfo & { tabs: TabInfo[] })[]; errors?: string[] };
```

`AppInfo.id` is the bundle identifier.

### 3.2 `cua`

| Member | Behaviour |
|---|---|
| `cua.getState(options?: ObservationOptions): Promise<State>` | Inventory of apps (running and recently used) and the browsers with their tabs (section 8; `[]` when the browser surface is off). Displays the result as compact JSON unless `emit: false`, as recorded (13 getState results, 327 app entries): top-level keys `apps`, `browsers`, then `errors` when any; each app `{"displayName","id","isRunning","lastUsedDate","useCount"}` in that (alphabetical) order, `lastUsedDate` a number of seconds since 2001-01-01 UTC rounded down to the day, and no `windows` key on macOS (0 of 327). Partial failures go in `errors` (`"Browsers: …"` for the browser part). |
| `cua.getApp(target: string): Promise<App>` | `target` is a display name, a full `.app` path or a bundle id. Launches the app in the background if it is not running (does not steal focus). Displays the app's initial accessibility state. If a display name does not resolve, the error says to retry with a bundle id from `listApps()`. |
| `cua.listApps(options?: ObservationOptions): Promise<AppInfo[]>` | Same app inventory as `getState().apps`; displays it as compact JSON unless `emit: false`. |
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
`cua.getApp`, `cua.listApps`, `cua.createBrowserTab`, `cua.getTab`,
`cua.listBrowsers` and `cua.listTabs` display their own result;
`cua.getBrowser` displays the browser documentation. Writing their return
value again duplicates it. `{ emit: false }` turns the display off for the
observation and discovery methods; the browser documentation still shows
the first time.

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

The rules below were confirmed against the recorded Codex sessions on this
machine: 11 sessions with 802 `cua_repl` calls, holding 137 full native
states (4,638 element lines), 127 native diffs (3,099 `~`/`+` lines), 32
native no-change reads, 66 full tab states (19,660 lines) and 37 tab diffs.
Counts in parentheses are occurrences in that corpus. The examples here are
synthetic.

### 5.1 Full state

```
Window: "Orders", App: Orders.
0 standard window Orders, Secondary Actions: Raise
	1 container
		2 button (collapsed) Refresh
	3 switch (settable, boolean) Sync, Value: on
	4 scroll bar (settable, float) 0.25
	5 row (selectable, expanded) Description: folder, Value: Inbox, Secondary Actions: Collapse
	6 (settable, unknown) Mode
		7 radio button Fast, Value: 1
	8 text field (settable) Description: Search, Help: Find orders, Value: lamp, ID: search, Placeholder: Search orders
	9 HTML content Description: Orders, URL: shop.example.test/orders
		10 link Help center, Value: help.example.test/start
		11 text Line one
Line two
	12 text field (settable) URL: file:///Users/test/Report.pdf, Value: Report.pdf, Secondary Actions: Open Finder item
13 menu bar
	14 Orders
	15 Edit

The focused UI element is 8 text field (settable) Description: Search, Help: Find orders, Value: lamp, ID: search, Placeholder: Search orders
```

Rules:

- Header `Window: "<title>", App: <app name>.` The title may be empty
  (`Window: ""`, common for panel apps). A title longer than 63 characters is
  cut to its first and last 31 characters around `…` (1 occurrence; the
  exact threshold is between 56 and 149 characters). The title is not
  escaped.
- One element per line: tab indentation by depth, the id, then the role
  words. The role words are the element's `AXRoleDescription` ("standard
  window", "button", "text field", "row", "scroll area", "HTML content",
  "system dialog", "pop up button", "menu button", "value indicator",
  "increment arrow button", "column header", ..., as macOS speaks them),
  with two overrides kept as a table (`axRoleWords` in
  `internal/script/axstate.go`): `AXGroup` reads `container` (832 lines;
  `group` never appears), and a menu bar item shows no role words. An
  element without a role description shows none either (`8 (settable,
  unknown) Mode` for a radio group, 18).
- States in parentheses, in this order: `selected` or `selectable` (an
  element that has `AXSelected` false), `disabled`, `expanded` or
  `collapsed` (an element that has `AXExpanded` false), `settable` (the value
  is settable), then the kind of a settable non-string value: `boolean`
  (153), `float` (91), `integer` (5) or `unknown` (18). Combinations seen:
  `(selectable, expanded)`, `(disabled, settable, boolean)`, `(selected,
  settable, boolean)`, `(disabled, settable, float)`. There is no `checked`
  state: a check box or radio button shows `Value: 0` / `Value: 1`.
- Then the name (`AXTitle`). An element named only by its description shows
  it as `Description: …` instead. Static text shows its value as its name.
  An unnamed element without a description whose value is a number shows the
  number bare in the name's place (`scroll bar (settable, float) 0`, 86).
- Then the labelled fields, joined with `, ` (after the name) or a space
  (without one): `Description`, `Help`, `URL`, `Value`, `ID`
  (`AXIdentifier`), `Placeholder`, `Secondary Actions`. Order evidence:
  `URL, Value, Secondary Actions` (312), `Description, URL` (119),
  `Description, ID` (108), `Value, ID` (69), `Help, Value` (43),
  `Description, Help, ID` (18), `Value, Placeholder` (9); a few apps report
  attributes in another order (`Placeholder` before `Value`, 2).
- Values: a boolean reads `on` / `off` (switches and Chrome's tabs); numbers
  in full precision (`0.9552117263843648`); an empty value is left out.
  `URL:` is the element's `AXURL` with `http://` / `https://` dropped and
  other schemes kept (`file:///…`); a link's address is its `Value`, without
  a `URL` field.
- No field is truncated by the renderer (names up to 390 characters seen);
  ellipses in the corpus come from the apps themselves. Line breaks inside
  names and values are printed as they are, so one element can span several
  lines.
- Ids are assigned in pre-order from 0 and are contiguous: the window
  subtree first, then the app's menu bar, whose items are the top-level menu
  titles with no role words. The Apple menu is never listed (0 of 73 menu
  bars).
- An unnamed, actionless group shows as `container` while something shows
  inside it, even a single child (61 single-child containers); a group with
  nothing inside is left out (no childless unnamed container in 496).
- A blank line, then `The focused UI element is <id> <line text>`. When no
  element has the focus the line is left out (7 reads). When the focused
  element has selected text, the closing reads instead:

  ````
  Selected text: ```
  <the selected text>
  ```

  Note: Pay special attention to the content selected by the user. ...
  ````

  (the note's full wording is in `selectionNote`, 5 reads).

### 5.2 Diff

After the first full state of a binding, `getAXState()` returns:

```
The following is a diff from the previous accessibility tree for Window: "Orders" with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.
Removed element IDs: 4
~0 standard window Orders (2), Secondary Actions: Raise
~		2 button (expanded) Refresh
+		18 text Saved
The focused UI element is 10 text field (settable) Description: Search, Value: lamp
```

Rules: ids of unchanged elements are stable within a binding; new elements get
ids above every id used so far; each changed or added line is its marker
followed directly by the element's depth in tabs (`~0 …` for the window, 15
`~` and 14 `+` lines at depth 0; never a separating tab); the `Removed
element IDs:` line (ranges as `12-20, 64`) is omitted when nothing was
removed; the focused line follows the changes with no blank line (85 of 85
diffs with a focused line). A window change (another window comes to the
front) yields a full state with the new header. When nothing changed:

```
There has been no change in the accessibility tree for Window: "Orders".
The focused UI element is 10 text field (settable) Description: Search, Value: lamp
```

(32 reads; the closing line follows directly, or the selected text).

### 5.3 Errors

An uncaught error is shown as its message alone, without `Error:` or the
error's name and without a stack (`tab is not defined`, `cua.computer.launch_app
is not a function`), as the first output part after the framing, before the
first-use documentation and before anything the call wrote (8 calls show the
order). An element id the latest state does not hold reads
`Computer Use server error -10005: <id> is an invalid element ID`.

### 5.4 Unconfirmed

- When the reference adds other windows of the app (a popover, a second
  system dialog) as further roots of one state (14 states), and when it
  leaves out the menu bar (27 states).
- The `Selected:` closing that lists selected rows (`Selected:` then
  `\t<id> <line>` and the note, 7 reads): which element's selection it lists.
- Why a few titles that contain `: ` print after the labelled fields
  (`tab (settable, boolean) Value: off, <title>`, 13 lines), and why some
  long URLs read `…`.
- Error texts other than the invalid id (stale element, unknown secondary
  action, app not found): the corpus holds only `noWindowsAvailable`,
  `cgWindowNotFound`, `timeoutReached` server errors and an ambiguous bundle
  id message.

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

## 8. Browser surface (bundled Chromium)

The reference's browser API is served from the open-source Chromium that
fennec-cua bundles (`internal/chromium`; BSD-3-Clause, never Google Chrome
or Chrome for Testing), driven over the DevTools protocol by a small Go
client (`internal/cdp`) on `--remote-debugging-pipe`: NUL-framed JSON on
fds 3 and 4, no TCP port, flattened target sessions. Every decision (the
tab registry, element ids, the state text, locators, key names, safety
checks) is made in Go (`internal/browser`).

The daemon runs one browser for every MCP session, started on the first
browser call (about 0.4 s cold, headless) with its own profile
(`~/Library/Application Support/fennec/cua/Chromium`, mode 0700, never a
Chrome profile; `FENNEC_CUA_CHROMIUM_PROFILE` overrides), no first-run or
default-browser prompts, no startup window, the mock keychain (the browser
itself never raises a Keychain prompt) and no Google API keys. It is
supervised: an exit is noticed, the next call starts it again (three exits
within a minute stop the restarts), a leftover of the same bundled binary
holding the profile is stopped, it shuts down after ten minutes with no page
and no command, and with the daemon. The app is found as
`Fennec.app/Contents/Helpers/Chromium.app` beside
`Contents/MacOS/fennec-cua`, as `Chromium.app` beside the binary in a
staging directory, or through `FENNEC_CUA_CHROMIUM` (an app or executable;
`off` disables the surface); `FENNEC_CUA_CHROMIUM_HEADLESS=1` runs it
without windows. Without it the browser calls throw `No browser is
available.`, without a browser surface configured `browser surfaces are not
enabled; use cua.getApp("<browser>")`, and `getState().browsers` is `[]`.

Each session works in an agent window of its own, opened in the background
by its first tab; later tabs open in it. Its tabs are the ones it created,
pages they opened (popups, `target=_blank`) and tabs the user opened in its
window; other sessions' windows are never listed or touched. CDP has no tab
groups, so windows are the unit of ownership. A visible agent cursor is
drawn in the page for every pointer action: a fixed, click-through,
`aria-hidden` element appended to `<html>` whose label (the session name and
the action, such as `🔎 Shop · click button Save`) is CSS content, so it
adds no node to the accessibility tree and no text to the document; it is
hidden while screenshots are taken. Navigation, a reload and history wait
for the document's `load` lifecycle event (up to 30 s); reading the
accessibility tree takes 2-25 ms.

"Import from Chrome" brings the user's sign-ins into this profile (section
8.6).

### 8.1 Mapping

| Reference API | Bundled Chromium |
|---|---|
| `cua.listBrowsers()` | one browser `{"id":"chromium","name":"Chromium","family":"chromium","type":"cdp"}`, displayed as compact JSON |
| `cua.getBrowser({ id })` | ids `chromium`, `chrome`, `browser` select it (models default to `chrome`); another id is an error naming `chromium`. Displays the browser documentation (`internal/browser/guide.md`, fennec-cua's own text) the first time in a runtime and returns `{ browserId, name, documentation(), nameSession(name) }` |
| `cua.createBrowserTab(id, url?, { sessionName, visible })` | `Target.createTarget` (`about:blank`, in the background; `newWindow` for the session's first tab), `Target.attachToTarget` with `flatten`, `Page.enable` and lifecycle events, then `Page.navigate` (`https://` added when the URL has no scheme, `about:blank` when omitted; a failed navigation is Chromium's error, such as `net::ERR_NAME_NOT_RESOLVED at https://…`). `sessionName` (or `nameSession`) labels the agent cursor. `visible` must be boolean and changes nothing. Displays the documentation (first time) and the full state |
| `cua.getTab(id \| { url }, { browser })` | a tab of the session by id (or target id), or by exact URL with an explicit browser; a page a session tab opened, or a tab the user opened in the session's window, is attached on first use. Errors as the reference: `getTab requires a tab reference`, `getTab requires an absolute URL.`, `getTab({ url }) requires an explicit browser.`, `Tab not found in browser chromium.`, `Multiple tabs match the reference in browser chromium: [...]`. `chrome://newtab` shows the tab's metadata instead of its state |
| `cua.listTabs({ browser })`, `getState().browsers[].tabs` | the session's tabs (`Target.getTargets`, pages only); `{ id, title, url, browserId }`, ids are `"1"`, `"2"`, ... per session and survive `js_reset` |
| `tab.getAXState()` / `getAXStateAndScreenshot()` | `Accessibility.getFullAXTree` and `DOM.getDocument` (for `ID:` fields), rendered as section 8.2 |
| `tab.getScreenshot()` / `tab.screenshot()` | `Page.captureScreenshot` (PNG at CSS-pixel size: a clip scaled by 1 / `devicePixelRatio`; `fullPage` captures beyond the viewport); the ratio of the viewport width to the PNG width maps later `[x, y]` points to CSS pixels |
| `tab.click(id \| [x, y], { mouseButton, clickCount, key, durationMs })` | `DOM.scrollIntoViewIfNeeded` and `DOM.getContentQuads` give the element centre; the agent cursor moves there; `Input.dispatchMouseEvent` moves, then presses and releases `clickCount` times (each press carrying its count and the held modifiers); `key` is held with key events across the clicks; `durationMs` (0-30000) holds each press |
| `tab.drag(from, to)` | move, press, move in 12 steps, release |
| `tab.scroll(id \| [x, y], direction, pages = 1 \| { pixels })` | move over the target, then a `mouseWheel` event by 0.9 viewport heights (or widths) per page; it waits up to 0.4 s for the page to move |
| `tab.typeText` / `pressKey` / `paste` (element index or `null` first) | `DOM.focus` on the element (a click when it takes no focus), then `Input.dispatchKeyEvent` per character of the US layout (others, and text typed while a modifier is held, through `Input.insertText`) / key events for the chord (xdotool names mapped to Playwright's: `Return` Enter, `BackSpace` Backspace, `super` Meta, `KP_0` Numpad0, ...; macOS editing commands such as `selectAll` for Command-A or `deleteBackward` for Backspace ride on the key event, as Chromium's text system would add them; `durationMs` holds the keys) / a `paste` `ClipboardEvent` with `text/plain` (and `text/html` for `html`) dispatched at the focused element, falling back to inserted text when the page does not take it (`md` pastes the source as text). The user's clipboard is never read or changed. A non-integer index fails before input with the reference's message |
| `tab.setValue(id, value)` | in-page: the native value setter plus `input`/`change` (and key) events for inputs and text areas, the matching option of a `<select>`, the checked state of a check box or radio, the text of a content-editable element |
| `tab.selectText(id, text, { prefix, suffix, selectionType })` | the occurrence is found in Go on the element's value or text; `setSelectionRange` or a DOM range selects it |
| `tab.performSecondaryAction(id, action)` | the listed `Expand` / `Collapse` toggle the element with a click; an unlisted name is an error listing the element's actions |
| `tab.goto` / `reload` / `back` / `forward` / `close` | `Page.navigate` / `Page.reload` (waiting for the new document's `load`); `back`/`forward` through `Page.getNavigationHistory` and `Page.navigateToHistoryEntry`; `close` is `Target.closeTarget` for agent tabs only (a user's tab is refused, never closed) |
| `tab.markDeliverable()` / `markHandoff()` | marks for the turn. `markHandoff` also brings the tab to the front of its window (`Target.activateTarget`) and labels the agent cursor `waiting for you`; the user acts in the tab, and the agent's next call simply continues |
| turn end (`turn_ended`) | unmarked agent tabs close and the marks clear, as the reference's turn cleanup |
| session end | unmarked agent tabs close; marked tabs stay open for the user (detached); user tabs are never closed |
| `js_reset` | drops the runtime's tab bindings and shows the documentation again on the next browser call; the browser, the tabs and their ids stay |
| `tab.title()`, `tab.url()`, `tab.id` | `Target.getTargetInfo`; the id is the session's tab id |
| `tab.playwright` | section 8.3; `waitForLoadState` reads `document.readyState` (and the `networkIdle` lifecycle event), `waitForURL` takes an exact URL or a glob (`**`, `*`, `{a,b}`) and then waits for `load` |
| browser restart | a crash ends the call in flight with `Chromium stopped; the action may have been sent…`; the session's tabs are gone with it (`the tab was closed; …`), and the next `createBrowserTab` starts the browser and opens a new window |

### 8.2 Tab state text

Learned from recorded Codex sessions that drove Chrome tabs (66 full tab
states with 19,660 element lines, 37 tab diffs; counts in parentheses). The
example is synthetic:

```
Browser tab: 1, Title: "Sign in", URL: "https://example.test/login".
0 AXWebArea Sign in, URL: example.test/login
	1 container
		2 heading Sign in, Value: 1
			3 text Sign in
		4 text field (settable) Email, Value: a@b.c, ID: email
		5 pop up button (collapsed, settable) Description: Language, Value: English, ID: lang, Secondary Actions: Expand
			6 menu
				7 (selected) English
				8 Deutsch
		9 tab (selected, settable, boolean) Billing, Value: 1
		10 button Description: Close dialog
		11 link Description: Sign up, Value: example.test/sign-up

The focused UI element is 4 text field (settable) Email, Value: a@b.c, ID: email
```

- The header `Browser tab: <id>, Title: "<title>", URL: "<url>".` starts every
  read, diffs and no-change reads included (103 headers). The root is
  `<id> AXWebArea <title>, URL: <url>`.
- URLs in lines drop `http://` and `https://`; long `data:` URLs are cut to
  their media type.
- Role words are macOS's for Chromium's roles: `text`, `button`, `link`,
  `heading` (`Value:` is its level), `image`, `text field`, `text entry area`
  (multi-line), `search text field`, `combo box`, `pop up button` (a native
  `<select>` or a button with a pop-up), `toggle button`, `checkbox` / `radio
  button` / `switch` (`Value:` 0, 1 or 2 for mixed), `stepper`, `slider`,
  `progress indicator`, `level indicator`, `content list`, `list` (list box),
  `table`, `row`, `cell`, `tab group`, `tab`, `outline`, `menu`, `menu bar`,
  `menu item`, `toolbar`, `dialog`, `AXListMarker`; every other role
  (generic, landmarks, paragraphs, list items, sections) reads `container`.
- States in parentheses, in the native order: `selected` / `selectable`,
  `disabled`, `expanded` / `collapsed`, `settable`, the value kind:
  `pop up button (collapsed)` (308), `button (disabled, collapsed)`,
  `row (selectable, collapsed)` (9), `combo box (collapsed, settable)` (7),
  `combo box (expanded, settable)` (3). `Expand` / `Collapse` appear as
  `Secondary Actions`.
- A tab with a selection state is a settable boolean whose value is 1 when
  chosen: `tab (selectable, settable, boolean) …, Value: 0` (39) and
  `tab (selected, settable, boolean) …, Value: 1` (15); a tab without one
  shows no states (15).
- A native `<select>` is `pop up button (collapsed, settable)` and its
  options show under a `menu` child as lines without role words: `(selected)
  <option>` for the chosen one, the bare option text for the others.
- A name given by an attribute (`aria-label`, `title`) shows as
  `Description:` on controls; names from content or a label show as the
  name. A link that holds only text shows as one line, `link Description:
  <text>, Value: <url>` (727); a link without a name shows its URL.
- Fields after the name, in order: `Description`, `Value`, `Help` (the
  accessible description), `ID` (the DOM id), `URL` (root only), a link's
  `Value`, `Secondary Actions`: `Description, Value` (724), `Description,
  ID, Secondary Actions` (284), `Value, ID` (75), `Description, ID, Value`
  (22, links).
- Line breaks inside names and values are printed as they are.
- Ignored nodes, inline text boxes, line breaks, whitespace-only text and the
  inner editor of text fields do not show. An unnamed container shows while
  something shows inside it, a single child included (132 single-child
  containers); one with nothing inside is left out (none in the corpus).
- Ids are keyed by DOM node: stable within a binding, fresh ones above every
  id used. A new document starts the numbering again: from 0 for the first
  document a binding reads, from 1 for every later one (the recorded root is
  `1 AXWebArea` on every full state after a navigation within a binding).
- Diff: the header line, then `The following is a diff from the previous
  accessibility tree with ~ and + representing changed and added elements,
  respectively. Removed elements are summarized by ID range.`, `Removed
  element IDs: …` when any, `~` / `+` lines as the marker followed directly
  by the depth's tabs, then the focused line with no blank line before it
  (29 of 29 diffs of that shape). A changed URL, a new document or changes
  to more than half of the lines give the full state (ids kept).
- No change: the header, then `There has been no change in the
  accessibility tree.` and the focused line. The recorded sessions hold no
  unchanged tab read; this wording follows the native one (section 5.2)
  without the window part, as the tab diff line does.
- The tab's first display (`createBrowserTab`, `getTab`) leaves no baseline:
  the binding's first `getAXState()` is full, as recorded.
- `playwright.domSnapshot()` renders the same tree as Playwright's aria
  snapshot (`- role "name" [attrs]: text`, `/url`, `/placeholder`), the
  format the recorded sessions show.

### 8.3 Playwright subset

Locators are resolved in Go against the accessibility tree and the
document, then acted on through the same element operations as element ids
(centre plus `mouse.click`, focus plus `keyboard.*`). Supported:
`getByRole(role, { name, exact, checked, selected, expanded, disabled,
pressed, level })`, `getByText`, `getByLabel`, `getByPlaceholder`,
`getByAltText`, `getByTitle` (string matching is case-insensitive and
whitespace-normalised unless `exact`; a RegExp runs as Go's RE2, so
look-arounds and back-references are refused), `getByTestId`,
`locator(css | "text=…" | "xpath=…")`, chaining, `first`, `last`, `nth`,
`filter({ hasText, hasNotText, has, hasNot, visible })`, `and`, `or`; actions
`click`, `dblclick`, `hover`, `fill`, `clear`, `press`, `pressSequentially`,
`type`, `check`, `uncheck`, `setChecked`, `selectOption`, `focus`,
`scrollIntoViewIfNeeded` (one element, strict, waiting up to `timeoutMs`,
default 5000); reads `count`, `all`, `innerText`, `textContent`,
`inputValue`, `getAttribute`, `isVisible`, `isHidden`, `isEnabled`,
`isDisabled`, `isChecked`, `isEditable`, `boundingBox`, `allTextContents`,
`allInnerTexts`, `waitFor({ state })`, `evaluate`; page-level
`domSnapshot`, `waitForTimeout`, `waitForLoadState`, `waitForURL`,
`evaluate`. The reference runs `evaluate` in a read-only scope; CDP has no
such scope, so here the code runs in the page as it is, and confirm mode
`every` asks before it. Other members are absent (a call is a `TypeError`), and
Playwright's internal selectors and `>>` chains are refused with an error.

### 8.4 Safety

Browser input is CDP input into the agent's own tabs, not OS input, so it
does not take the desk turn. Everything else applies: the app allowlist
admits the browser as the app `Chromium` (otherwise every browser call is
refused and the inventory lists no browser); every input (click, drag,
scroll, typing, keys, paste, values, selections, secondary actions,
navigation, locator actions) passes the confirm gate as the equivalent
step, with the tab's title as the target; Stop cancels the call in flight
(the late reply is dropped) and every later one; text typed or set into a
password field is marked secret (dots in the confirm question) and typed
text never appears in errors; Chromium itself masks password values in the
accessibility tree. A session never lists or drives another session's
window; it attaches only to pages its tabs opened and tabs the user opened
in its window, and never closes a user's tab. The browser runs as the
user, in its own profile, without automation flags (`navigator.webdriver`
stays false); its DevTools pipe is private to the daemon (no port).

### 8.5 Open points

- Third-party notices: chromium-browser-snapshots builds are made without
  `generate_about_credits`, so their `chrome://credits` is a placeholder.
  `cua-package` refuses to stage such a build unless told it is for
  development (`-allow-missing-credits`, recorded in `MANIFEST.json`).
  Distribution needs a Chromium build with generated credits (our own
  official-config build, or a maintained BSD prebuilt that ships them).
- The managed Chromium in the tart image for the bench; the
  `chromiumlive` tests run on a developer Mac (headless).
- Snapshot builds are untested trunk builds and do not update themselves:
  bump the pin (revision, commit, size, SHA-256 in `internal/chromium`)
  deliberately and rerun the live tests.
- Several sessions at once: a later tab opens in the browser's last active
  window, which is the session's own window unless the user or another
  session focused another one since.
- Unconfirmed tab rules: which check boxes read `(settable, integer)` (41
  do, 33 do not; native `<input>` versus ARIA check boxes is the likely
  split) and the bare value of an unnamed one; whether the numbering also
  returns to 0 at a turn's end (one recorded binding did); the no-change
  wording (section 8.2); why the focused line sometimes shows the root's URL
  as `…`; the reference error text for a stale tab element id.
- Iframe content is not read (the tree is the main frame's).

### 8.6 Import from Chrome

The ChatGPT desktop app imports a Chrome (or Atlas) profile's cookies,
passwords and, on request, history and extensions into its built-in
browser, natively and after the user picks a profile; the Keychain prompt
for the Safe Storage key is the consent. fennec-cua imports **cookies** from
one Google Chrome profile into the managed Chromium, the part that keeps
agent tabs signed in:

- Profiles come from Chrome's `Local State` (`profile.info_cache`: name,
  account e-mail, last used); the default is the last used one.
- `Network/Cookies` (and its write-ahead log) is copied to a private
  temporary directory and read by a small read-only SQLite reader in Go, so
  a running Chrome is not disturbed. Values are decrypted with the
  "Chrome Safe Storage" Keychain password (read through Security.framework,
  so macOS asks whether fennec-cua may use it): PBKDF2-HMAC-SHA1, salt
  `saltysalt`, 1003 rounds, AES-128-CBC with a 16-space IV; databases at
  meta version 24 or later carry SHA-256 of the host before the value, which
  is checked and dropped. The key lives only for the import; no value is
  logged, stored outside the profile or returned.
- Cookies go into the running browser with `Storage.setCookies` (domain
  cookies by `domain`, host-only ones by `url`; SameSite, priority, source
  scheme and port kept; expired ones skipped; partitioned (CHIPS) cookies
  left out). Running it again refreshes them; `replace` clears the managed
  cookies first. The answer is counts only (found, imported, expired,
  partitioned, undecryptable, failed), also kept in `chrome-import.json`
  beside the profile.
- Entry points: `fennec-cua browser profiles | import [--profile NAME]
  [--replace] | status` (the daemon's control socket, owner only; Fennec's
  UI calls the same operations), and `fennec.browser.chromeProfiles()` /
  `fennec.browser.importFromChrome({ profile, replace })` in the REPL, which
  also passes the confirm gate. The browser guide tells the agent to suggest
  the import (or `markHandoff()`) when a site needs a sign-in it cannot do.
- Passwords are not imported: Chromium's password store has no DevTools
  interface, writing its database directly would put every saved password
  into a profile the agent drives, and the profile's own encryption uses the
  mock keychain. Signed-in sessions (cookies) cover the agent's need; a
  password the user wants to use stays with the user (hand-off).

## 9. Not supported

| Reference API | fennec-cua |
|---|---|
| `typeCharacters`, `listWindows`, `getApp({ windowId })`, `screenshotId` | Linux/Windows only in the reference; absent here as there. |
| `"iab"`, `"dot"`, `"mcpapps"` browsers | ChatGPT-app surfaces; not available outside it. |
| `getTab({ mention })`, `getBrowser({ extensionInstanceId })` | tab mentions and extension instances are ChatGPT-app features; both are errors. |
| `agent.browsers`, `browser.tabs`, `browser.user.claimTab`, `browser.history`, `tab.capabilities`, `tab.clipboard`, `tab.content`, `tab.dev`, `getJsDialog`, `playwright.frameLocator`, `waitForEvent`, `expectNavigation` | the reference's wider browser runtime; not provided. A tab outside the session's window and its tabs' popups is never taken. |

## 10. Parity tests

Each row is a test that must exist and pass. Unit tests run on fakes; `live`
tests run in the tart VM against CUAFixture and built-in apps; `chromiumlive`
tests (`FENNEC_CUA_CHROMIUM=/path/to/Chromium.app go test -tags chromiumlive
./internal/browser ./internal/engine`) run the pinned Chromium headless on a
temporary profile against pages they serve on 127.0.0.1, with a synthetic
Chrome profile and a test key for the import; they never read the user's
Chrome or Keychain.

| ID | Checks | Kind |
|---|---|---|
| CDX-01 | `tools/list` is exactly `js`, `js_reset`; schemas as section 1 | unit |
| CDX-02 | result framing `Wall time: N.NNNN seconds\nOutput:` | unit |
| CDX-03 | bindings persist across calls (`var`, `let`, `const`, `function`, `class`); top-level await | unit |
| CDX-04 | `js_reset` drops bindings and re-shows the documentation; apps untouched | unit |
| CDX-05 | error output: the message alone, first after the framing and before the documentation and what was written; `isError`; session still usable | unit |
| CDX-06 | `completion_summary`-only call answers `Completion summary received.` | unit |
| CDX-07 | first-use documentation shown once per session, covering section 4's topics | unit |
| CDX-08 | `nodeRepl.write` / `emitImage` (data URL, bytes, `{bytes, mimeType}`); `file:` refused | unit |
| CDX-09 | self-displaying calls display once; `emit: false` suppresses | unit |
| CDX-10 | full state format (section 5.1) on a fixed fake tree, byte for byte; the confirmed rules each on a synthetic tree (`internal/script/axstate_test.go`: role words, state order and value kinds, on/off, bare numbers, field order, URLs, raw line breaks, containers, Apple menu, long titles, focus and selected text) | unit |
| CDX-11 | diff format (section 5.2): removed ranges, `~` and `+` followed by the depth's tabs, stable and fresh ids, focused line with no blank line | unit |
| CDX-12 | `disableDiffing`, window change gives a full state, the no-change line followed by the focused line | unit |
| CDX-13 | `click(id)`, `click([x, y])` in screenshot pixels, `clickCount`, `mouseButton`, `key` | unit + live |
| CDX-14 | `setValue`, `selectText` (prefix/suffix/selectionType), `performSecondaryAction` with an unlisted name refused | unit + live |
| CDX-15 | `pressKey` xdotool syntax table, `typeText`, `paste` restoring the pasteboard | unit + live |
| CDX-16 | `scroll` by element and point, pages and `{ pixels }`; `drag` | unit + live |
| CDX-17 | `getApp` by name, path and bundle id; background launch keeps focus; unresolved name hint | unit + live |
| CDX-18 | `getState` / `listApps` as compact JSON with the reference's keys and order; `browsers: []` with the browser surface off | unit + live |
| CDX-19 | with the browser surface off, the browser APIs throw the documented error | unit |
| CDX-20 | an id not in the latest state reads `Computer Use server error -10005: <id> is an invalid element ID`; a stale element is an error asking for a fresh state | unit |
| CDX-21 | safety: allowlist, Stop mid-call, confirmation, secret redaction, wrong-window refusal through `cua` calls | unit + live |
| CDX-22 | sandbox: no `require`, `process`, file system | unit |
| CDX-23 | the macOSWorld bench runs fennec-cua through this surface (`cmd/cua-bench`) | live |
| CDX-24 | browser discovery: `listBrowsers`, `getBrowser` (documentation once per runtime), `getState().browsers` with tabs, `listTabs` as compact JSON | unit (`internal/engine`, `internal/browser`) |
| CDX-25 | `createBrowserTab` / `getTab` display documentation then state; reference error texts; `js_reset` keeps tabs and ids; turn end passed on | unit + chromiumlive |
| CDX-26 | tab state text (section 8.2) byte for byte on synthetic trees: header, roles, fields, link collapse, select options, tab values, containers, focused line; diff with removed ranges, `~`/`+` depth, focused line without a blank line, no-change line, full state on URL or document change (numbering from 1 after the first document) or a large change; `domSnapshot` | unit + chromiumlive |
| CDX-27 | tab input: element-index-first `typeText`/`pressKey`/`paste` (bad index refused before input), click centre and options, points scaled from screenshot pixels, `selectText`, `setValue`, `performSecondaryAction`, history, xdotool to Playwright keys | unit + chromiumlive |
| CDX-28 | Playwright subset: role/text/label/test-id/CSS resolution, RegExp, `nth`/`filter`, strict mode, timeouts, `fill`/`check`/`selectOption`, `evaluate` | unit + chromiumlive |
| CDX-29 | transport: CDP pipe framing (NUL-delimited, sessions, events, large replies), timeout, Stop (cancel), launch arguments, supervision (restart after a crash, crash-loop stop, idle shutdown, a stale profile owner), crash with the tabs gone and a new window after the restart | unit + chromiumlive |
| CDX-30 | browser safety: allowlist, confirm gate per input (declined sends nothing), secret marking and redaction, user tabs never closed, other sessions' windows never listed, hand-off brings the tab forward | unit + chromiumlive |
| CDX-31 | marks: turn end closes unmarked agent tabs; session end closes unmarked agent tabs and leaves marked ones open | unit + chromiumlive |
| CDX-32 | import from Chrome: `Local State` profiles, cookie decryption at meta versions 23 and 24 (host hash checked), write-ahead log and overflow pages, expired and partitioned cookies, a wrong key, values never in errors, logs, answers or the status file; `Storage.setCookies` batches with a per-cookie fallback, `replace`, the confirm gate; a live import reaching a 127.0.0.1 page | unit (`internal/chromeimport`, `internal/browser`) + chromiumlive |

## 11. Updating

When the reference build changes: diff the shipped type declarations and docs
of `@oai/cua` and `@oai/cua-repl` against the version in section "Reference",
and compare fresh recorded outputs against section 5. Update this document
first, then the implementation and the CDX tests.
