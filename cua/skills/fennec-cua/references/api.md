# The REPL and the `cua` API

The REPL's first call in a runtime prints this API as fennec-cua documents it; this page is the same contract, condensed. It matches OpenAI Codex's computer-use interface (`cua` from `@oai/cua`), so code written for Codex runs unchanged.

## Runtime

- One runtime per MCP connection (MCP) or per named session (`fennec-cua js --session NAME`). Top-level `var`, `let`, `const`, `function`, `class` persist until `js_reset`.
- Top-level `await` works. Globals: `cua`, `fennec`, `nodeRepl`, `console`, `setTimeout` and timers. No `require`, `process`, file system or network.
- Each call ends after 90 s with an error; the runtime stays usable.
- Output: text from `nodeRepl.write`, `console.*` and self-displaying calls; images from screenshots and `nodeRepl.emitImage`. A bare last expression is not printed. Output is capped (about 25k tokens) with a note when cut.
- An uncaught error ends the call: its message is the first output line, `isError` is set (the shell command exits 1), and bindings made before it stay.
- Every result starts with `Wall time: N.NNNN seconds` and `Output:`.

## Types

```ts
type Vec2 = [x: number, y: number];             // pixels of the app window's screenshot
type ObservationOptions = { emit?: boolean };     // emit: false = do not display
type StateOptions = ObservationOptions & { disableDiffing?: boolean };
type ClickOptions = { mouseButton?: "left"|"right"|"middle"|"l"|"r"|"m"; clickCount?: number; key?: string; durationMs?: number };
type Direction = "up"|"down"|"left"|"right"|"u"|"d"|"l"|"r";
type SelectTextOptions = { prefix?: string; suffix?: string; selectionType?: "text"|"cursor_before"|"cursor_after" };
type AppInfo = { displayName?: string; id: string; isRunning?: boolean; lastUsedDate?: number; useCount?: number };  // id = bundle id
```

## `cua`

| Call | Does |
|---|---|
| `cua.listApps(opts?)` | running and recently used apps as compact JSON `[{"displayName","id","isRunning","lastUsedDate","useCount"}]` |
| `cua.getState(opts?)` | `{ apps, browsers: [{ id, name, family, type, tabs }], errors? }`; `errors` explains partial failures (e.g. missing permissions) |
| `cua.getApp(target)` | `App` for a display name, `.app` path or bundle id; launches it in the background if needed (focus stays put) and prints its first state. An unresolved name throws, suggesting `listApps()` |
| `cua.computer.target` | `"mac"` |
| `cua.listBrowsers()`, `cua.getBrowser({ id })`, `cua.createBrowserTab(...)`, `cua.getTab(...)`, `cua.listTabs(...)` | browser tabs: [browser.md](browser.md) |

## `App`

| Method | Does |
|---|---|
| `getAXState(opts?)` | waits for the UI to settle, prints the front window and menu bar as state text; a diff after the first read through this binding |
| `getScreenshot(opts?)` | PNG of the window (no pointer), displayed as an image |
| `getAXStateAndScreenshot(opts?)` | both from one settle; returns `{ state, screenshot }` |
| `click(target, opts?)` | `target` is an element id or `[x, y]` in screenshot pixels; `clickCount: 2`, `mouseButton: "right"`, `key: "shift"` (held), `durationMs` |
| `drag(from, to)` | press at `from`, move, release at `to` (points) |
| `scroll(target, direction, distance?)` | element id or point; `distance` in pages (default 1) or `{ pixels: N }` |
| `selectText(id, text, opts?)` | select `text` in an editable element; `prefix`/`suffix` pick the occurrence; `selectionType` places the caret before/after instead |
| `setValue(id, value)` | set through accessibility: text fields, sliders, steppers, pop-ups, check boxes |
| `performSecondaryAction(id, name)` | one of the names in the element's `Secondary Actions` (`"Raise"`, `"Show Menu"`, `"Increment"`, ...); an unlisted name throws, listing the valid ones |
| `paste(text, { format? })` | puts text on the pasteboard, pastes into the focus, restores the previous pasteboard text |
| `pressKey(key, { durationMs? })` | xdotool names and chords |
| `typeText(text)` | types into the focused element (click the field first if needed) |

Input methods resolve to `undefined` and print nothing: read the state afterwards.

### Keys (xdotool syntax)

`a`, `A`, `1`, `Return`, `Tab`, `Escape`, `BackSpace`, `Delete`, `space`, `Up`/`Down`/`Left`/`Right`, `Home`, `End`, `Page_Up`, `Page_Down`, `F1`..`F12`, `KP_0`..`KP_9`. Modifiers joined with `+`: `super` (Command), `ctrl`, `alt` (Option), `shift`: `"super+c"`, `"super+shift+z"`, `"shift+Tab"`, `"ctrl+alt+Delete"`.

## State text

```
Window: "Orders", App: Orders.
0 standard window Orders, Secondary Actions: Raise
	1 container
		2 button (collapsed) Refresh
	3 switch (settable, boolean) Sync, Value: on
	8 text field (settable) Description: Search, Value: lamp, ID: search, Placeholder: Search orders
	12 button (disabled) Save, Help: Saves the file
13 menu bar
	14 Orders
	15 Edit

The focused UI element is 8 text field (settable) Description: Search, Value: lamp, ID: search, Placeholder: Search orders
```

- Header, then one element per line: tab indentation by depth, the id, the role words as macOS speaks them (`button`, `text field`, `row`, `pop up button`, `container` for groups; menu bar items have none), states in parentheses (`selected`/`selectable`, `disabled`, `expanded`/`collapsed`, `settable`, value kind `boolean`/`float`/`integer`), the name, then `Description`, `Help`, `URL`, `Value`, `ID`, `Placeholder`, `Secondary Actions`.
- Check boxes and radio buttons read `Value: 0` / `Value: 1`; switches `on`/`off`.
- The window subtree comes first, then the app's menu bar (top-level menu titles). Open a menu by clicking its title id, then read the state for its items.
- The last line names the focused element, or shows text the user selected.

Diff after the first read:

```
The following is a diff from the previous accessibility tree for Window: "Orders" with ~ and + representing changed and added elements, respectively. Removed elements are summarized by ID range.
Removed element IDs: 4
~		2 button (expanded) Refresh
+		18 text Saved
The focused UI element is 8 text field ...
```

No change: `There has been no change in the accessibility tree for Window: "Orders".` A different front window prints a full state with its own header.

## Errors you will see

| Message | Meaning |
|---|---|
| `Computer Use server error -10005: 17 is an invalid element ID` | the id is not in the latest state; read again |
| `element 17 is no longer in the app; call getAXState() again and use an id from it` | the element went away since the read |
| an `Unknown secondary action` style error listing names | use one of the listed `Secondary Actions` |
| `stopped: ...` | the user stopped fennec-cua; wait for them |
| mentions of `permission`, `permissions_pending` | Accessibility or Screen Recording missing: `fennec-cua doctor` |
| `not_allowed` | the app is outside the user's allowlist (`[safety] allow` in config.toml) |

## `nodeRepl`

- `nodeRepl.write(value)`: append text (objects rendered readably; use `JSON.stringify` for JSON).
- `nodeRepl.emitImage(image)`: append an image (`data:` URL, PNG/JPEG/WebP bytes, or `{ bytes, mimeType }`).

## fennec

fennec-cua's own capabilities, apart from `cua`:

```ts
fennec.status();                         // { version, permissions: {accessibility, screen_recording}, input, stopped, screen, ... }
fennec.wait(app, { window_opens: "Export", timeout_ms: 5000 });   // also window_gone, exists/gone/reads(+equals) with { select: 'button "Save"' }, still
fennec.act(app, steps, options?);        // multi-step plan, each step confirmed / unconfirmed / refused, stops at the first surprise
fennec.observe(request);                 // the raw outline with refs (e12), a find, a hit test, a capture
fennec.flows.list(query?); fennec.flows.show(name); fennec.flows.run(name, params?);
fennec.flows.save(name, { steps | from: "last", description }); fennec.flows.delete(name);
fennec.record.start(apps?); fennec.record.note(text); fennec.record.stop(params?);
fennec.model.ask(prompt, images?); fennec.model.decide(args); fennec.model.ground(args);   // only with a model endpoint in config.toml
fennec.run(code, params?);               // a module whose main(params) uses the run API
fennec.tools(); fennec.call(name, args); // installed plugin tools
fennec.browser.chromeProfiles(); fennec.browser.importFromChrome({ profile?, replace? });
```

`fennec.act` steps (one key each): `{ press: target }`, `{ click: target, button?, count?, mods? }`, `{ set: target, value }`, `{ type: "text", into?: target, secret?: true }`, `{ key: "cmd+s" }`, `{ menu: "File > Export > PDF" }`, `{ scroll: target, direction, amount }`, `{ launch: "TextEdit" }`, `{ wait: { window_opens: "Save" } }`, `{ close: "window title" }`. A target is `{ select: 'button "Save"' }`, `{ ref: "e12" }` (from `fennec.observe`) or `{ point: { x, y, space: "window" } }`. Mark passwords `secret: true`: they never appear in traces, errors or saved flows. For everyday work the `cua` methods are enough.
