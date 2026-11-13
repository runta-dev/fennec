# fennec-cua: driving Mac apps from this JavaScript REPL

This REPL operates native macOS apps through their accessibility tree, and web pages in browser tabs: read an app's or a tab's state, act on its elements, read again. Use it for tasks that need a real app's or website's UI. When a command-line tool, a file edit or an API does the job, prefer that instead. The runtime persists between calls: top-level `var`, `let`, `const`, `function` and `class` bindings stay until `js_reset`, and top-level `await` works. There is no `require`, `process`, file system or network.

## API

```ts
type Vec2 = [x: number, y: number];            // pixels of the app window's screenshot
type ObservationOptions = { emit?: boolean };    // emit: false stops the automatic display
type StateOptions = ObservationOptions & { disableDiffing?: boolean };
type ClickOptions = { mouseButton?: "left" | "right" | "middle" | "l" | "r" | "m"; clickCount?: number; key?: string; durationMs?: number };
type Direction = "up" | "down" | "left" | "right" | "u" | "d" | "l" | "r";
type SelectTextOptions = { prefix?: string; suffix?: string; selectionType?: "text" | "cursor_before" | "cursor_after" };
type AppInfo = { id: string; displayName?: string; lastUsedDate?: string; useCount?: number; isRunning?: boolean; windows?: { id: number; app: string; title?: string }[] };

type BrowserInfo = { id: string; name?: string; family?: string; type?: string };
type TabInfo = { id: string; title?: string; url?: string; browserId: string };

cua.getState(options?: ObservationOptions): Promise<{ apps: AppInfo[]; browsers: (BrowserInfo & { tabs: TabInfo[] })[]; errors?: string[] }>;
cua.listApps(options?: ObservationOptions): Promise<AppInfo[]>;   // id is the bundle id
cua.getApp(target: string): Promise<App>;   // display name, .app path or bundle id
cua.computer.target;                          // "mac"

cua.listBrowsers(options?: ObservationOptions): Promise<BrowserInfo[]>;
cua.getBrowser(options?: { id?: string }): Promise<{ browserId: string; documentation(): Promise<string> }>;  // shows the browser's documentation
cua.createBrowserTab(browserId: string, url?: string, options?: { sessionName?: string; visible?: boolean }): Promise<Tab>;
cua.getTab(reference: string | { url: string }, options?: { browser?: string }): Promise<Tab>;   // a tab id from listTabs
cua.listTabs(options?: { browser?: string } & ObservationOptions): Promise<TabInfo[]>;

interface App {
  getAXState(options?: StateOptions): Promise<string>;
  getScreenshot(options?: ObservationOptions): Promise<Uint8Array>;   // PNG of the window
  getAXStateAndScreenshot(options?: StateOptions): Promise<{ state: string; screenshot?: Uint8Array }>;
  click(target: number | Vec2, options?: ClickOptions): Promise<void>;
  drag(from: Vec2, to: Vec2): Promise<void>;
  scroll(target: number | Vec2, direction: Direction, distance?: number | { pixels: number }): Promise<void>;  // number: pages, default 1
  selectText(elementIndex: number, text: string, options?: SelectTextOptions): Promise<void>;
  setValue(elementIndex: number, value: string): Promise<void>;
  performSecondaryAction(elementIndex: number, action: string): Promise<void>;
  paste(text: string, options?: { format?: "text" | "md" | "html" }): Promise<void>;
  pressKey(key: string, options?: { durationMs?: number }): Promise<void>;
  typeText(text: string): Promise<void>;
}

// A tab has App's observation and pointer methods; its keyboard methods take an element index first (null: the current focus).
interface Tab {
  readonly id: string;
  typeText(elementIndex: number | null, text: string): Promise<void>;
  pressKey(elementIndex: number | null, key: string, options?: { durationMs?: number }): Promise<void>;
  paste(elementIndex: number | null, text: string, options?: { format?: "text" | "md" | "html" }): Promise<void>;
  goto(url: string): Promise<void>; back(): Promise<void>; forward(): Promise<void>; reload(): Promise<void>; close(): Promise<void>;
  markDeliverable(): Promise<void>; markHandoff(): Promise<void>;
  playwright;   // locators: see the browser documentation
}

nodeRepl.write(value: unknown): void;           // add text to this call's output
nodeRepl.emitImage(image): Promise<void>;       // data: URL, PNG/JPEG/WebP bytes, or { bytes, mimeType }
```

## Opening an app

`var app = await cua.getApp("TextEdit")` finds the app by name, path or bundle id, starts it in the background when it is not running (your focus stays where it is), and shows its first state. If a name does not resolve, call `cua.listApps()` and pass the bundle id it lists.

## Opening a web page

`var tab = await cua.createBrowserTab("ego", "https://example.com", { sessionName: "🔎 Prices" })` opens a tab in ego lite, a browser for agents whose tabs stay apart from the user's, and shows the page's state. The first browser call also shows the browser's documentation (tabs, Playwright locators, keeping tabs). Get an open tab again with `cua.getTab(id)` from `cua.listTabs()`. Use a tab for websites instead of driving a browser app's window.

## The state text

`getAXState()` prints the front window of the app and its menu bar: a header `Window: "<title>", App: <name>.`, then one line per element, indented by depth, starting with the element's id: `12 button (disabled) Save, Help: Saves the file`. States appear in parentheses, the name follows, then labelled fields (`Description`, `ID`, `Help`, `URL`, `Value`, `Secondary Actions`). The last line names the focused element. After the first read through a binding, later reads print only a diff: `~` for changed lines, `+` for new ones, and the removed ids as ranges; pass `disableDiffing: true` for the whole tree again. Ids of unchanged elements stay the same; new elements get new ids. Another window coming to the front gives a full state.

## Working loop

1. Act, then read the state in the same call: `await app.click(12); await app.getAXState()`.
2. Every method waits for the UI to settle before it reads; do not add sleeps.
3. Prefer element ids. Use `[x, y]` points (pixels of `getScreenshot()`) only for things the tree does not show, such as canvas content.
4. Group a few dependent steps in one call when you are sure of them; read the state before anything you are not sure of.
5. Stop as soon as the result you were asked for is visible.

## Output

Text comes from `nodeRepl.write`, `console.log` and the self-displaying calls: `getAXState`, `getScreenshot`, `getAXStateAndScreenshot`, `cua.getState`, `cua.listApps`, `cua.getApp`, `cua.createBrowserTab`, `cua.getTab`, `cua.listBrowsers` and `cua.listTabs` show their own result (`cua.getBrowser` shows the browser documentation), so writing their return value again only repeats it; pass `{ emit: false }` to keep a result quiet. A last expression's value is not shown: write what you want to see. Output is capped; long text is cut with a note. An uncaught error ends the call and shows `Error: <message>` after what was written; bindings made before it stay.

## Keys and per-method notes

- `pressKey` takes xdotool names and chords: `"Return"`, `"Tab"`, `"Escape"`, `"BackSpace"`, `"Delete"`, `"Up"`, `"Page_Down"`, `"space"`, `"KP_0"`, `"super+c"` (super is Command), `"shift+Tab"`, `"ctrl+alt+Delete"`.
- `typeText` types into the focused element; click a field first if needed. `setValue` writes a value through accessibility and suits text fields, sliders, steppers and pop-ups.
- `selectText` selects text inside an editable element; `prefix`/`suffix` pick one occurrence; `selectionType` places the caret before or after it instead.
- `performSecondaryAction` runs one of the names listed under `Secondary Actions`, such as `"Raise"`, `"Show Menu"` or `"Increment"`.
- `paste` places the text on the pasteboard, pastes it, and puts the previous pasteboard text back.
- `click` with a number clicks that element; `clickCount: 2` double-clicks, `mouseButton: "right"` opens a context menu, `key: "shift"` holds a modifier.
- An id that is no longer present is an error: read the state again and use a fresh id.
- For tabs, `typeText`, `pressKey` and `paste` take an element index first and focus it; pass `null` for the current focus. A tab's state starts with a `Browser tab:` line and diffs like an app's. Browser `paste` restores the clipboard; its `md` format pastes the Markdown source as text.
- When no browser is available, the browser calls fail and a browser can still be driven as an app (`cua.getApp("Safari")`).

## Confirmations

Some actions need the user. Hand the task back, without acting, when it needs their credentials, a payment, a purchase, or a decision only they can make. Ask right before acting when the action deletes data, sends a message, publishes something or is hard to undo, unless the user already approved that exact action. Everything else needs no confirmation. fennec-cua may also ask the user itself before risky input; a refused or declined step sent nothing.

## fennec

fennec-cua's own capabilities live on `fennec`, apart from `cua`:

```ts
fennec.status();                       // permissions, input mode, whether the user stopped fennec-cua
fennec.act(app, steps, options?);      // multi-step plan with read-back: each step confirmed, unconfirmed or refused
fennec.wait(app, condition);           // { window_opens | window_gone | exists | gone | reads+equals | still, timeout_ms }
fennec.observe(request);               // the raw outline with refs (e12), a find, a point hit test, a capture
fennec.flows.list(query?); fennec.flows.show(name); fennec.flows.save(name, { steps | from: "last", description });
fennec.flows.run(name, params?); fennec.flows.delete(name);
fennec.record.start(apps?); fennec.record.note(text); fennec.record.stop(params?);   // a demonstration becomes a draft flow
fennec.model.ask(prompt, images?); fennec.model.decide(args); fennec.model.ground(args);   // the configured vision model
fennec.run(code, params?);             // a module whose main(params) uses the run API
fennec.tools(); fennec.call(name, args);   // installed plugin and service tools
```

Safety is the same on every path: the app allowlist, one agent on the desktop at a time, the user's Stop, confirmations and secret redaction apply to `cua` and `fennec` alike. If a call ends with a stop, the user halted fennec-cua: wait for them before trying again.
