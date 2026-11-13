// The REPL's globals: cua (the reference interface of docs/codex-parity.md),
// nodeRepl, and fennec (fennec-cua's own capabilities). Every call goes
// through the host's tools, so the daemon's safety controls apply to all
// of them. The host object stays inside this closure.
(function (host, input) {
  "use strict";

  // Browser calls go to the daemon's ego lite manager; every check and
  // decision is made there. Errors lose their code prefix so the model
  // reads the message alone, as in the reference.
  async function browser(op, args) {
    try {
      return await host.call("browser", Object.assign({}, args || {}, { op: op }));
    } catch (e) {
      if (e && typeof e.code === "string" && typeof e.message === "string" && e.message.indexOf(e.code + ": ") === 0) {
        const err = new Error(e.message.slice(e.code.length + 2));
        err.code = e.code;
        throw err;
      }
      throw e;
    }
  }

  function textMatcher(v) {
    if (v instanceof RegExp) {
      return { source: v.source, flags: v.flags };
    }
    if (v === undefined || v === null) {
      return undefined;
    }
    return { s: String(v) };
  }

  function quoteDesc(v) {
    return v instanceof RegExp ? String(v) : "'" + String(v) + "'";
  }

  function fnSource(fn) {
    if (typeof fn === "function") {
      return { src: String(fn), isFn: true };
    }
    if (typeof fn === "string") {
      return { src: fn, isFn: false };
    }
    throw new TypeError("evaluate takes a function or a string");
  }

  class Locator {
    constructor(tab, chain, desc) {
      this._tab = tab;
      this._chain = chain;
      this._desc = desc;
    }

    _with(step, desc) {
      return new Locator(this._tab, this._chain.concat([step]), this._desc ? this._desc + "." + desc : desc);
    }

    _call(method, args) {
      return browser("loc", { binding: this._tab._b, chain: this._chain, desc: this._desc, method: method, args: args || [] });
    }

    toString() { return this._desc; }

    locator(selector) { return this._with({ kind: "css", selector: String(selector) }, "locator(" + quoteDesc(selector) + ")"); }
    getByRole(role, o) {
      const opts = o || {};
      const step = { kind: "role", role: String(role), name: textMatcher(opts.name), exact: !!opts.exact };
      for (const k of ["checked", "selected", "expanded", "disabled", "pressed", "level"]) {
        if (opts[k] !== undefined) {
          step[k] = opts[k];
        }
      }
      return this._with(step, "getByRole(" + quoteDesc(role) + (opts.name !== undefined ? ", { name: " + quoteDesc(opts.name) + (opts.exact ? ", exact: true" : "") + " }" : "") + ")");
    }
    getByText(text, o) { return this._with({ kind: "text", text: textMatcher(text), exact: !!(o && o.exact) }, "getByText(" + quoteDesc(text) + ")"); }
    getByLabel(text, o) { return this._with({ kind: "label", text: textMatcher(text), exact: !!(o && o.exact) }, "getByLabel(" + quoteDesc(text) + ")"); }
    getByPlaceholder(text, o) { return this._with({ kind: "placeholder", text: textMatcher(text), exact: !!(o && o.exact) }, "getByPlaceholder(" + quoteDesc(text) + ")"); }
    getByAltText(text, o) { return this._with({ kind: "alt", text: textMatcher(text), exact: !!(o && o.exact) }, "getByAltText(" + quoteDesc(text) + ")"); }
    getByTitle(text, o) { return this._with({ kind: "title", text: textMatcher(text), exact: !!(o && o.exact) }, "getByTitle(" + quoteDesc(text) + ")"); }
    getByTestId(id) { return this._with({ kind: "testid", text: { s: String(id) } }, "getByTestId(" + quoteDesc(id) + ")"); }
    first() { return this._with({ kind: "nth", n: 0 }, "first()"); }
    last() { return this._with({ kind: "nth", n: -1 }, "last()"); }
    nth(i) { return this._with({ kind: "nth", n: Number(i) }, "nth(" + Number(i) + ")"); }
    filter(o) {
      const opts = o || {};
      const step = { kind: "filter", hasText: textMatcher(opts.hasText), hasNotText: textMatcher(opts.hasNotText) };
      if (opts.has instanceof Locator) {
        step.has = opts.has._chain;
      }
      if (opts.hasNot instanceof Locator) {
        step.hasNot = opts.hasNot._chain;
      }
      if (opts.visible !== undefined) {
        step.visible = !!opts.visible;
      }
      return this._with(step, "filter()");
    }
    and(l) { return this._with({ kind: "and", other: l._chain }, "and(" + l._desc + ")"); }
    or(l) { return this._with({ kind: "or", other: l._chain }, "or(" + l._desc + ")"); }

    async all() {
      const n = await this.count();
      const out = [];
      for (let i = 0; i < n; i++) {
        out.push(this.nth(i));
      }
      return out;
    }
    count() { return this._call("count"); }
    click(o) { return this._call("click", [o]).then(function () {}); }
    dblclick(o) { return this._call("dblclick", [o]).then(function () {}); }
    hover(o) { return this._call("hover", [o]).then(function () {}); }
    fill(value, o) { return this._call("fill", [String(value), o]).then(function () {}); }
    clear(o) { return this._call("clear", [o]).then(function () {}); }
    press(key, o) { return this._call("press", [String(key), o]).then(function () {}); }
    pressSequentially(text, o) { return this._call("pressSequentially", [String(text), o]).then(function () {}); }
    type(text, o) { return this._call("type", [String(text), o]).then(function () {}); }
    check(o) { return this._call("check", [o]).then(function () {}); }
    uncheck(o) { return this._call("uncheck", [o]).then(function () {}); }
    setChecked(v, o) { return this._call("setChecked", [!!v, o]).then(function () {}); }
    selectOption(v, o) { return this._call("selectOption", [v, o]); }
    focus(o) { return this._call("focus", [o]).then(function () {}); }
    scrollIntoViewIfNeeded(o) { return this._call("scrollIntoViewIfNeeded", [o]).then(function () {}); }
    innerText(o) { return this._call("innerText", [o]); }
    textContent(o) { return this._call("textContent", [o]); }
    inputValue(o) { return this._call("inputValue", [o]); }
    getAttribute(name, o) { return this._call("getAttribute", [String(name), o]); }
    isVisible() { return this._call("isVisible"); }
    isHidden() { return this._call("isHidden"); }
    isEnabled() { return this._call("isEnabled"); }
    isDisabled() { return this._call("isDisabled"); }
    isChecked() { return this._call("isChecked"); }
    isEditable() { return this._call("isEditable"); }
    boundingBox() { return this._call("boundingBox"); }
    allTextContents() { return this._call("allTextContents"); }
    allInnerTexts() { return this._call("allInnerTexts"); }
    waitFor(o) { return this._call("waitFor", [o]).then(function () {}); }
    async evaluate(fn, arg) {
      const f = fnSource(fn);
      if (!f.isFn) {
        throw new TypeError("a locator's evaluate takes a function of the element");
      }
      const res = await this._call("evaluate", [f.src, arg === undefined ? null : arg]);
      return res ? res.value : undefined;
    }
  }

  class Playwright {
    constructor(tab) { this._tab = tab; this._root = new Locator(tab, [], ""); }
    locator(s) { return this._root.locator(s); }
    getByRole(r, o) { return this._root.getByRole(r, o); }
    getByText(t, o) { return this._root.getByText(t, o); }
    getByLabel(t, o) { return this._root.getByLabel(t, o); }
    getByPlaceholder(t, o) { return this._root.getByPlaceholder(t, o); }
    getByAltText(t, o) { return this._root.getByAltText(t, o); }
    getByTitle(t, o) { return this._root.getByTitle(t, o); }
    getByTestId(id) { return this._root.getByTestId(id); }
    async domSnapshot() { return (await this._tab._call("domSnapshot")).text; }
    waitForTimeout(ms) { return sleep(Number(ms) || 0); }
    waitForLoadState(o) { return this._tab._call("waitForLoadState", [o === undefined ? null : o]).then(function () {}); }
    waitForURL(url, o) { return this._tab._call("waitForURL", [String(url), o]).then(function () {}); }
    async evaluate(fn, arg) {
      const f = fnSource(fn);
      const res = await this._tab._call("evaluate", [f.src, f.isFn, arg === undefined ? null : arg]);
      return res ? res.value : undefined;
    }
  }

  function checkIndex(i) {
    if (i !== null && !(Number.isInteger(i) && i >= 0)) {
      throw new Error("Browser input requires an element index from the latest AX snapshot, or null to use current focus.");
    }
  }

  class Tab {
    constructor(id, b) {
      Object.defineProperty(this, "id", { value: id, enumerable: true });
      Object.defineProperty(this, "_b", { value: b });
      Object.defineProperty(this, "playwright", { value: new Playwright(this), enumerable: false });
    }

    toJSON() { return { id: this.id }; }

    _call(method, args) {
      return browser("tab", { binding: this._b, method: method, args: args || [] });
    }

    async getAXState(options) {
      const opts = options || {};
      const res = await this._call("getAXState", [{ disableDiffing: !!opts.disableDiffing }]);
      if (opts.emit !== false) {
        host.write(res.text);
      }
      return res.text;
    }

    async getScreenshot(options) {
      const opts = options || {};
      const res = await this._call("getScreenshot", [{}]);
      if (opts.emit !== false) {
        await host.emitImage({ data: res.data, mime: res.mime });
      }
      return toUint8(res.data);
    }

    async screenshot(options) {
      const res = await this._call("screenshot", [options || {}]);
      return toUint8(res.data);
    }

    async getAXStateAndScreenshot(options) {
      const opts = options || {};
      const res = await this._call("getAXStateAndScreenshot", [{ disableDiffing: !!opts.disableDiffing }]);
      const out = { state: res.text };
      if (opts.emit !== false) {
        host.write(res.text);
      }
      if (res.data) {
        if (opts.emit !== false) {
          await host.emitImage({ data: res.data, mime: res.mime });
        }
        out.screenshot = toUint8(res.data);
      }
      return out;
    }

    async click(target, options) { await this._call("click", [target, options || {}]); }
    async drag(from, to) { await this._call("drag", [from, to]); }
    async scroll(target, direction, distance) { await this._call("scroll", [target, direction, distance === undefined ? null : distance]); }
    async selectText(elementIndex, text, options) { await this._call("selectText", [elementIndex, String(text), options || {}]); }
    async setValue(elementIndex, value) { await this._call("setValue", [elementIndex, value]); }
    async performSecondaryAction(elementIndex, action) { await this._call("performSecondaryAction", [elementIndex, String(action)]); }
    async typeText(elementIndex, text) {
      checkIndex(elementIndex);
      await this._call("typeText", [elementIndex, String(text)]);
    }
    async pressKey(elementIndex, key, options) {
      checkIndex(elementIndex);
      await this._call("pressKey", [elementIndex, key, options || {}]);
    }
    async paste(elementIndex, text, options) {
      checkIndex(elementIndex);
      await this._call("paste", [elementIndex, String(text), options || {}]);
    }
    async goto(url) { await this._call("goto", [String(url)]); }
    async back() { await this._call("back"); }
    async forward() { await this._call("forward"); }
    async reload() { await this._call("reload"); }
    async close() { await this._call("close"); }
    async title() { return this._call("title"); }
    async url() { return this._call("url"); }
    async markDeliverable() { await this._call("markDeliverable"); }
    async markHandoff() { await this._call("markHandoff"); }
  }

  // showTab displays what opening a tab shows: the browser documentation
  // the first time, then the tab's state.
  function showTab(res) {
    if (res.doc) {
      host.write(res.doc);
    }
    host.write(res.text);
    return new Tab(res.id, res.binding);
  }

  // Values from Go arrive as maps; these give their keys a fixed order.
  function tabInfo(t) {
    const out = { id: t.id };
    for (const k of ["providerTabId", "title", "url", "browserId"]) {
      if (t[k] !== undefined && t[k] !== "") {
        out[k] = t[k];
      }
    }
    return out;
  }

  function browserInfo(b) {
    const out = { id: b.id };
    for (const k of ["name", "family", "type"]) {
      if (b[k] !== undefined && b[k] !== "") {
        out[k] = b[k];
      }
    }
    return out;
  }

  async function browserStates() {
    try {
      const list = await browser("state", {});
      return { browsers: list.map(function (b) { return Object.assign(browserInfo(b), { tabs: (b.tabs || []).map(tabInfo) }); }) };
    } catch (e) {
      return { browsers: [], error: "Browsers: " + (e && e.message ? e.message : String(e)) };
    }
  }

  function isVec2(p) {
    return Array.isArray(p) && p.length === 2 && typeof p[0] === "number" && typeof p[1] === "number";
  }

  function point(p) {
    if (!isVec2(p)) {
      throw new TypeError("a point is [x, y] in the window screenshot's pixels");
    }
    return { point: { x: p[0], y: p[1], space: "window" } };
  }

  const buttons = { left: "left", l: "left", right: "right", r: "right", middle: "middle", m: "middle" };
  const directions = { up: "up", u: "up", down: "down", d: "down", left: "left", l: "left", right: "right", r: "right" };

  // stepFailure turns a failed act into an Error, or null when the step
  // was sent (confirmed or unconfirmed).
  function stepFailure(res, describe) {
    if (!res || res.ok) {
      return null;
    }
    const step = (res.steps || []).find(function (s) { return !s.ok; });
    if (step && step.status === "unconfirmed") {
      return null;
    }
    const e = res.error || (step && step.error) || { message: "the step did not complete" };
    let msg = e.message || "the step did not complete";
    if (e.code === "stale_ref" || (e.code === "target_not_found" && describe)) {
      msg = (describe || "the element") + " is no longer in the app; call getAXState() again and use an id from it";
    }
    if (e.hint) {
      msg += " (" + e.hint + ")";
    }
    const err = new Error(msg);
    if (e.code) {
      err.code = e.code;
    }
    return err;
  }

  function toUint8(b64) {
    return new Uint8Array(host.bytes(b64));
  }

  async function inventory() {
    return await host.call("apps", {});
  }

  function windowsOf(inv, a) {
    return (inv.windows || []).filter(function (w) {
      return (a.pid && w.pid === a.pid) || (w.app && a.name && w.app.toLowerCase() === a.name.toLowerCase());
    });
  }

  function appInfo(inv, a) {
    const info = { id: a.bundle_id || a.name, displayName: a.name };
    if (a.last_used) {
      info.lastUsedDate = a.last_used;
    }
    if (a.use_count) {
      info.useCount = a.use_count;
    }
    info.isRunning = !!a.running;
    const ws = windowsOf(inv, a);
    if (a.running) {
      info.windows = ws.map(function (w) {
        const out = { id: Number(String(w.ref).replace(/^w/, "")), app: w.app || a.name };
        if (w.title) {
          out.title = w.title;
        }
        return out;
      });
    }
    return info;
  }

  function resolveApp(apps, target) {
    const t = target.trim();
    const lower = t.toLowerCase();
    const isPath = t.startsWith("/") || lower.endsWith(".app");
    let found = apps.find(function (a) { return a.bundle_id && a.bundle_id.toLowerCase() === lower; });
    if (!found && isPath) {
      const base = t.replace(/\/+$/, "").split("/").pop().replace(/\.app$/i, "");
      found = apps.find(function (a) { return a.path && a.path.replace(/\/+$/, "") === t.replace(/\/+$/, ""); }) ||
        apps.find(function (a) { return a.name && a.name.toLowerCase() === base.toLowerCase(); });
      if (!found && t.startsWith("/")) {
        found = { name: base, path: t, running: false };
      }
    }
    if (!found) {
      found = apps.find(function (a) { return a.name && a.name.toLowerCase() === lower; });
    }
    return found || null;
  }

  function sleep(ms) {
    return new Promise(function (resolve) { setTimeout(resolve, ms); });
  }

  class App {
    constructor(a) {
      this._app = { name: a.name, pid: a.pid || 0, bundleId: a.bundle_id || "" };
      this._ax = host.axNew();
    }

    get name() { return this._app.name; }
    get bundleId() { return this._app.bundleId; }
    get pid() { return this._app.pid; }

    toJSON() {
      return { app: this._app.name, bundleId: this._app.bundleId || undefined, pid: this._app.pid || undefined };
    }

    async _window() {
      const res = await host.call("observe", { scope: "desktop" });
      const a = this._app;
      const ws = (res.windows || []).filter(function (w) {
        return (a.pid && w.pid === a.pid) || (w.app && w.app.toLowerCase() === a.name.toLowerCase());
      });
      if (ws.length === 0) {
        throw new Error(a.name + " has no open window; open one (for example with pressKey(\"super+n\")) or call cua.getApp(\"" + a.name + "\") again");
      }
      const w = ws.find(function (x) { return x.active; }) || ws[0];
      if (!a.pid && w.pid) {
        a.pid = w.pid;
      }
      return w;
    }

    async _settle() {
      try {
        await host.call("wait", { still: true, hold_ms: 150, timeout_ms: 1200 });
      } catch (e) {
        // A desktop without captures cannot judge stillness; read anyway.
      }
    }

    async getAXState(options) {
      const opts = options || {};
      const w = await this._window();
      await this._settle();
      const res = await host.call("observe", { window: w.ref, detail: true });
      const text = host.axRead(this._ax, res, !opts.disableDiffing);
      if (opts.emit !== false) {
        host.write(text);
      }
      return text;
    }

    async getScreenshot(options) {
      const opts = options || {};
      const w = await this._window();
      const res = await host.call("observe", { window: w.ref, image: "window" });
      if (!res.image || !res.image.data) {
        throw new Error("no screenshot of " + this._app.name + "'s window was taken");
      }
      if (opts.emit !== false) {
        await host.emitImage({ data: res.image.data, mime: res.image.mime });
      }
      return toUint8(res.image.data);
    }

    async getAXStateAndScreenshot(options) {
      const opts = options || {};
      const state = await this.getAXState(opts);
      const out = { state: state };
      try {
        out.screenshot = await this.getScreenshot({ emit: opts.emit });
      } catch (e) {
        // The state alone still answers.
      }
      return out;
    }

    _element(id) {
      if (typeof id !== "number" || !Number.isInteger(id)) {
        throw new TypeError("an element id is a whole number from the latest getAXState()");
      }
      const e = host.axElement(this._ax, id);
      if (!e) {
        throw new Error("element " + id + " is not in the latest state of " + this._app.name + "; call getAXState() again and use an id from it");
      }
      return e;
    }

    _target(t) {
      if (typeof t === "number") {
        return { ref: this._element(t).ref };
      }
      return point(t);
    }

    async _act(step, describe) {
      let win = host.axWindow(this._ax);
      if (!win) {
        win = (await this._window()).ref;
      }
      const res = await host.call("act", { window: win, steps: [step] });
      const err = stepFailure(res, describe);
      if (err) {
        throw err;
      }
      return undefined;
    }

    async click(target, options) {
      const opts = options || {};
      const t = this._target(target);
      const button = buttons[opts.mouseButton || "left"];
      if (!button) {
        throw new TypeError("mouseButton is left, right or middle (l, r, m)");
      }
      const count = opts.clickCount || 1;
      const mods = opts.key ? String(opts.key).split("+").filter(Boolean) : null;
      const desc = typeof target === "number" ? "element " + target : null;
      if (typeof target === "number" && button === "left" && count === 1 && !mods) {
        return this._act({ press: t }, desc);
      }
      const step = { click: t };
      if (button !== "left") {
        step.button = button;
      }
      if (count > 1) {
        step.count = count;
      }
      if (mods) {
        step.mods = mods;
      }
      return this._act(step, desc);
    }

    async drag(from, to) {
      return this._act({ drag: point(from), to: point(to) });
    }

    async scroll(target, direction, distance) {
      const dir = directions[direction];
      if (!dir) {
        throw new TypeError("direction is up, down, left or right (u, d, l, r)");
      }
      let amount = 5;
      if (typeof distance === "number") {
        amount = Math.max(1, Math.round(distance * 5));
      } else if (distance && typeof distance.pixels === "number") {
        amount = Math.max(1, Math.round(distance.pixels / 20));
      }
      const desc = typeof target === "number" ? "element " + target : null;
      return this._act({ scroll: this._target(target), direction: dir, amount: Math.min(amount, 50) }, desc);
    }

    async selectText(elementIndex, text, options) {
      const opts = options || {};
      const e = this._element(elementIndex);
      const value = e.value || "";
      const prefix = opts.prefix || "";
      const suffix = opts.suffix || "";
      const hits = [];
      for (let i = value.indexOf(text); i >= 0; i = value.indexOf(text, i + 1)) {
        if (prefix && value.slice(Math.max(0, i - prefix.length), i) !== prefix) {
          continue;
        }
        if (suffix && value.slice(i + text.length, i + text.length + suffix.length) !== suffix) {
          continue;
        }
        hits.push(i);
        if (text === "") {
          break;
        }
      }
      if (hits.length === 0) {
        throw new Error(JSON.stringify(text) + " is not in the value of element " + elementIndex + (prefix || suffix ? " with that prefix and suffix" : ""));
      }
      if (hits.length > 1 && !prefix && !suffix) {
        throw new Error(JSON.stringify(text) + " appears " + hits.length + " times in element " + elementIndex + "; pass prefix or suffix to pick one");
      }
      const at = hits[0];
      let range = [at, text.length];
      switch (opts.selectionType || "text") {
        case "text":
          break;
        case "cursor_before":
          range = [at, 0];
          break;
        case "cursor_after":
          range = [at + text.length, 0];
          break;
        default:
          throw new TypeError("selectionType is text, cursor_before or cursor_after");
      }
      return this._act({ select_text: { ref: e.ref }, range: range }, "element " + elementIndex);
    }

    async setValue(elementIndex, value) {
      const e = this._element(elementIndex);
      return this._act({ set: { ref: e.ref }, value: String(value) }, "element " + elementIndex);
    }

    async performSecondaryAction(elementIndex, action) {
      const e = this._element(elementIndex);
      const listed = e.actions || [];
      const want = String(action).toLowerCase();
      const name = listed.find(function (a) { return a.toLowerCase() === want; });
      if (!name) {
        throw new Error("element " + elementIndex + " has no secondary action " + JSON.stringify(String(action)) +
          (listed.length ? "; it lists: " + listed.join(", ") : "; it lists none"));
      }
      return this._act({ perform: { ref: e.ref }, action: name }, "element " + elementIndex);
    }

    async paste(text, options) {
      const opts = options || {};
      const format = opts.format || "text";
      if (["text", "md", "html"].indexOf(format) < 0) {
        throw new TypeError("format is text, md or html");
      }
      let win = host.axWindow(this._ax);
      if (!win) {
        win = (await this._window()).ref;
      }
      const res = await host.call("paste", { text: String(text), format: format, window: win });
      const err = stepFailure(res);
      if (err) {
        throw err;
      }
      return undefined;
    }

    async pressKey(key, options) {
      if (typeof key !== "string" || key.trim() === "") {
        throw new TypeError('pressKey takes a key such as "Return" or "super+c"');
      }
      return this._act({ key: key });
    }

    async typeText(text) {
      return this._act({ type: String(text) });
    }
  }

  const cua = {
    computer: Object.freeze({ target: "mac" }),

    async getState(options) {
      const opts = options || {};
      const inv = await inventory();
      const br = await browserStates();
      const state = { apps: (inv.apps || []).map(function (a) { return appInfo(inv, a); }), browsers: br.browsers };
      const errors = (inv.errors || []).concat(br.error ? [br.error] : []);
      if (errors.length) {
        state.errors = errors;
      }
      if (opts.emit !== false) {
        host.write(JSON.stringify(state, null, 2));
      }
      return state;
    },

    async listApps(options) {
      const opts = options || {};
      const inv = await inventory();
      const apps = (inv.apps || []).map(function (a) { return appInfo(inv, a); });
      if (opts.emit !== false) {
        host.write(JSON.stringify(apps, null, 2));
      }
      return apps;
    },

    async getApp(target) {
      if (typeof target !== "string" || target.trim() === "") {
        throw new TypeError("getApp takes an app's name, its .app path or its bundle id");
      }
      let inv = await inventory();
      let a = resolveApp(inv.apps || [], target);
      if (!a) {
        throw new Error("no app matches " + JSON.stringify(target) + "; retry with a bundle id from cua.listApps()");
      }
      if (!a.running || windowsOf(inv, a).length === 0) {
        const res = await host.call("act", { steps: [{ launch: a.name || a.bundle_id || a.path }] });
        const err = stepFailure(res);
        if (err) {
          throw err;
        }
        for (let i = 0; i < 60; i++) {
          inv = await inventory();
          const again = resolveApp(inv.apps || [], a.bundle_id || a.name || target) || a;
          if (again.running && windowsOf(inv, again).length > 0) {
            a = again;
            break;
          }
          await sleep(250);
        }
      }
      const app = new App(a);
      await app.getAXState();
      return app;
    },

    async listBrowsers(options) {
      const opts = options || {};
      const list = (await browser("browsers", {})).map(browserInfo);
      if (opts.emit !== false) {
        host.write(JSON.stringify(list));
      }
      return list;
    },

    async getBrowser(options) {
      const opts = options || {};
      let id = opts.id;
      if (opts.extensionInstanceId !== undefined) {
        throw new Error("The Chrome instance is unavailable.");
      }
      const res = await browser("browser", id === undefined ? {} : { id: String(id) });
      if (res.doc) {
        host.write(res.doc);
      }
      return Object.freeze({
        browserId: res.browserId,
        name: res.name,
        documentation: async function () { return await browser("guide", {}); },
        nameSession: async function (name) { await browser("nameSession", { name: String(name) }); },
      });
    },

    async createBrowserTab(browserId, url, options) {
      const opts = options || {};
      const args = { browserId: browserId === undefined ? null : browserId };
      if (url !== undefined && url !== null) {
        args.url = String(url);
      }
      if (opts.sessionName !== undefined) {
        args.sessionName = opts.sessionName;
      }
      if (opts.visible !== undefined) {
        args.visible = opts.visible;
      }
      return showTab(await browser("createTab", args));
    },

    async getTab(reference, options) {
      const opts = options || {};
      const args = { ref: reference === undefined ? null : reference };
      if (opts.browser !== undefined) {
        args.browser = String(opts.browser);
      }
      return showTab(await browser("getTab", args));
    },

    async listTabs(options) {
      const opts = options || {};
      const args = {};
      if (opts.browser !== undefined) {
        args.browser = String(opts.browser);
      }
      const res = await browser("listTabs", args);
      const tabs = (res.tabs || []).map(tabInfo);
      if (res.doc) {
        host.write(res.doc);
      }
      if (opts.emit !== false) {
        host.write(JSON.stringify(tabs));
      }
      return tabs;
    },
  };

  function windowOf(app) {
    if (app instanceof App) {
      return host.axWindow(app._ax) || undefined;
    }
    return typeof app === "string" && app !== "" ? app : undefined;
  }

  const fennec = {
    input: input,
    status: function () { return host.call("status", {}); },
    observe: function (request) { return host.call("observe", request || {}); },
    act: function (app, steps, options) {
      const req = Object.assign({}, options || {}, { steps: steps });
      const win = windowOf(app);
      if (win) {
        req.window = win;
      }
      return host.call("act", req);
    },
    wait: function (app, condition) {
      return host.call("wait", Object.assign({}, condition || {}));
    },
    run: function (code, params, options) {
      return host.call("run", Object.assign({}, options || {}, { code: code, params: params || {} }));
    },
    flows: Object.freeze({
      list: function (query) { return host.call("flow", query ? { op: "list", query: query } : { op: "list" }); },
      show: function (name) { return host.call("flow", { op: "show", name: name }); },
      save: function (name, options) { return host.call("flow", Object.assign({}, options || {}, { op: "save", name: name })); },
      run: function (name, params) { return host.call("flow", { op: "run", name: name, params: params || {} }); },
      delete: function (name) { return host.call("flow", { op: "delete", name: name }); },
    }),
    record: Object.freeze({
      start: function (apps) { return host.call("flow", { op: "record_start", apps: apps || [] }); },
      note: function (text) { return host.call("flow", { op: "record_note", text: text }); },
      stop: function (params) { return host.call("flow", params ? { op: "record_stop", params: params } : { op: "record_stop" }); },
    }),
    model: Object.freeze({
      ask: function (prompt, images) { return host.model("ask", { prompt: prompt, images: images }); },
      decide: function (args) { return host.model("decide", args); },
      ground: function (args) { return host.model("ground", args); },
    }),
    tools: function () { return host.call("extensions", {}); },
    call: function (name, args) { return host.call(name, args || {}); },
  };

  const nodeRepl = {
    write: function (value) { host.write(value); },
    emitImage: function (image) { return host.emitImage(image); },
  };

  globalThis.cua = Object.freeze(cua);
  globalThis.fennec = Object.freeze(fennec);
  globalThis.nodeRepl = Object.freeze(nodeRepl);
})
