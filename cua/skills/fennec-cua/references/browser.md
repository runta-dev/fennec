# Browser tabs (the bundled Chromium)

The full archive ships its own Chromium (open-source, not Google Chrome) for agents. It runs with its own profile (`~/Library/Application Support/fennec/cua/Chromium`), separate from the user's browsers, starts on the first browser call and stops after ten idle minutes. Each REPL session gets a window of its own, opened in the background. The core archive has no browser: browser calls throw `No browser is available.`; drive Safari as an app then (`cua.getApp("Safari")`).

## Calls

```ts
cua.listBrowsers();                                  // [{"id":"chromium","name":"Chromium","family":"chromium","type":"cdp"}]
cua.getBrowser({ id: "chromium" });                  // shows the browser documentation (first time); "chrome" and "browser" also select it
cua.createBrowserTab("chromium", url?, { sessionName?: string, visible?: boolean });  // opens, waits for load, prints the full state
cua.getTab(id | { url }, { browser: "chromium" });   // a tab of this session again
cua.listTabs({ browser: "chromium" });               // this session's tabs: [{ id, title, url, browserId }]
```

A URL without a scheme gets `https://`. `sessionName` labels the agent cursor drawn in the page (for example `"Prices"`).

## Tab

A tab has the app methods (`getAXState`, `getScreenshot`, `getAXStateAndScreenshot`, `click`, `drag`, `scroll`, `selectText`, `setValue`, `performSecondaryAction`) plus:

```ts
tab.typeText(elementIndex | null, text);     // the keyboard methods take an element id first (it gets focus); null = current focus
tab.pressKey(elementIndex | null, key, { durationMs? });
tab.paste(elementIndex | null, text, { format?: "text" | "md" | "html" });   // a paste event; the clipboard is untouched
tab.goto(url); tab.back(); tab.forward(); tab.reload(); tab.close();
tab.title(); tab.url(); tab.id;
tab.screenshot({ fullPage?: boolean });     // bytes, not displayed
tab.markDeliverable(); tab.markHandoff();
tab.playwright;                              // locators, below
```

State text starts with `Browser tab: 1, Title: "...", URL: "...".`, then `0 AXWebArea <title>, URL: ...` and one numbered line per element (`text field (settable) Email, ID: email`, `link Description: Sign up, Value: example.test/sign-up`, `pop up button (collapsed, settable)` for `<select>` with its options under a `menu`). Diffs work as for apps; a navigation prints the full tree again. `[x, y]` points are pixels of the tab's latest screenshot. Iframe content is not read.

Do not `goto` the URL a tab already shows (that reloads it); use `reload()` when you mean it.

## Playwright locators

```ts
tab.playwright.getByRole("button", { name: "Search" }).click();
tab.playwright.getByLabel("Email").fill("a@example.com");
tab.playwright.getByPlaceholder("Search").press("Enter");
tab.playwright.locator("css=.result a").first().innerText();
await tab.playwright.domSnapshot();          // role tree to write locators against
await tab.playwright.waitForURL("**/done", { timeoutMs: 10000 });
```

Supported: `getByRole` (with `name`, `exact`, `checked`, `selected`, `expanded`, `disabled`, `pressed`, `level`), `getByText`, `getByLabel`, `getByPlaceholder`, `getByAltText`, `getByTitle`, `getByTestId`, `locator(css | "text=..." | "xpath=...")`, chaining, `first`, `last`, `nth`, `filter`, `and`, `or`; actions `click`, `dblclick`, `hover`, `fill`, `clear`, `press`, `pressSequentially`, `type`, `check`, `uncheck`, `setChecked`, `selectOption`, `focus`, `scrollIntoViewIfNeeded` (strict: one element, waiting up to `timeoutMs`, default 5000); reads `count`, `all`, `innerText`, `textContent`, `inputValue`, `getAttribute`, `isVisible`, `isHidden`, `isEnabled`, `isDisabled`, `isChecked`, `isEditable`, `boundingBox`, `allTextContents`, `allInnerTexts`, `waitFor`, `evaluate`. Strings match case-insensitive substrings unless `exact: true`; RegExps run as RE2 (no look-arounds).

## Keeping tabs

- Over MCP, a host that sends turn ends closes the agent tabs not marked in that turn.
- When the session ends (MCP connection closed, `fennec-cua js-end`, or two idle hours for a shell session), unmarked agent tabs close.
- `markDeliverable()`: the tab is the result (a document, a submitted form). `markHandoff()`: the user must act (sign-in, payment, CAPTCHA); it comes to the front for them.
- `js_reset` keeps tabs and their ids; get them back with `cua.getTab(id)`.

## Sign-ins

The profile starts signed out. When a site needs the user's account, suggest importing their Google Chrome sign-ins (`fennec.browser.importFromChrome()` from the REPL or `fennec-cua browser import` from a shell; macOS asks the user for Keychain access), or `markHandoff()` and let the user sign in. Never type the user's password yourself unless they gave it to you for exactly that.
