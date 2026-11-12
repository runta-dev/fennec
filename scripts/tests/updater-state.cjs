const fs = require('node:fs'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync('native/macos/UI/updater.js', 'utf8');

function makeNode(tag = 'div') {
  return {
    tag,
    hidden: false,
    disabled: false,
    open: false,
    textContent: '',
    title: '',
    innerHTML: '',
    children: [],
    attrs: {},
    events: {},
    classes: new Set(),
    classList: {
      add(c) { this.owner.classes.add(c); },
      remove(c) { this.owner.classes.delete(c); },
      toggle(c, on) { if (on) this.owner.classes.add(c); else this.owner.classes.delete(c); },
      contains(c) { return this.owner.classes.has(c); }
    },
    append(...nodes) { this.children.push(...nodes); },
    replaceChildren(...nodes) { this.children = [...nodes]; },
    setAttribute(k, v) { this.attrs[k] = v; },
    getAttribute(k) { return this.attrs[k] ?? null; },
    addEventListener(k, fn) { this.events[k] = fn; },
    showModal() { this.open = true; },
    close() { this.open = false; },
    focus() {}
  };
}
function el(tag) { const n = makeNode(tag); n.classList.owner = n; return n; }

const pill = el('button'); pill.hidden = true;
const pillLabel = el('span');
const hideBtn = el('button');
const autoSwitch = el('button');
const versionBox = el('div');
const versionNumber = el('span');
const versionSub = el('span');
const versionActions = el('div');
const dialogNodes = {
  '#update-dialog-title': el('strong'),
  '.update-dialog-version': el('span'),
  '.update-dialog-notes': el('div'),
  '.update-dialog-hint': el('p'),
  '.update-dialog-cancel': el('button'),
  '.update-dialog-confirm': el('button')
};
let createdDialog = null;
const bySelector = {
  '#update-pill': pill,
  '#update-pill-label': pillLabel,
  '#update-hide': hideBtn,
  '#auto-update': autoSwitch,
  '.settings-version': versionBox,
  '.settings-version-number': versionNumber,
  '.settings-version-sub': versionSub,
  '.settings-version-actions': versionActions
};
const sent = [];
const context = vm.createContext({
  window: {},
  document: {
    body: { append(d) { createdDialog = d; } },
    querySelector: s => bySelector[s] || null,
    createTextNode: text => ({ tag: '#text', textContent: text }),
    createElement: tag => {
      const n = el(tag);
      if (tag === 'dialog') n.querySelector = s => dialogNodes[s];
      return n;
    }
  },
  translate: s => s,
  send: msg => sent.push(msg)
});

vm.runInContext(source, context);

// 1. Ready update displays header pill and Settings Restart to update action.
context.window.renderUpdateState({ state: 'ready', current: '0.2.100', latest: '0.2.200', notes: '### Features\n- Fast updates via `fen update` and [Changelog](https://github.com/runta-dev/fennec/releases).' });
assert.equal(pill.hidden, false, 'header pill visible when update is ready');
assert.equal(versionActions.children.length, 1);
assert.equal(versionActions.children[0].textContent, '[[i18n:ui.restart_to_update]]');

// 2. Clicking pill opens confirmation dialog with parsed release notes; confirming triggers update-install.
pill.onclick();
assert.equal(createdDialog.open, true);
assert.equal(dialogNodes['.update-dialog-notes'].children.length, 2);
const bulletItem = dialogNodes['.update-dialog-notes'].children[1].children[0];
assert.equal(bulletItem.children.some(c => c.tag === 'code' && c.textContent === 'fen update'), true, 'inline code rendered safely');
const linkBtn = bulletItem.children.find(c => c.tag === 'button' && c.textContent === 'Changelog');
assert.ok(linkBtn, 'inline HTTPS release link rendered');
linkBtn.onclick();
assert.equal(sent.pop(), 'update-open-url:{"url":"https://github.com/runta-dev/fennec/releases"}');
dialogNodes['.update-dialog-confirm'].onclick();
assert.equal(sent.pop(), 'update-install');

// 3. Hiding pill skips only the current version while leaving Settings action intact.
context.window.renderUpdateInstallFailed();
createdDialog.close();
hideBtn.onclick({ stopPropagation() {} });
assert.equal(pill.hidden, true, 'pill hidden after clicking x');
assert.equal(sent.pop(), 'settings:{"update_skip":"0.2.200"}');
assert.equal(versionActions.children[0].textContent, '[[i18n:ui.restart_to_update]]', 'Settings still retains Restart to update');

// 4. A newer version re-exposes the header pill automatically.
context.window.renderUpdateState({ state: 'ready', current: '0.2.100', latest: '0.2.300', notes: '' });
assert.equal(pill.hidden, false, 'newer version shows pill again');

// 5. Toggling Automatic updates switch sends settings update.
autoSwitch.onclick();
assert.equal(sent.pop(), 'settings:{"auto_update":false}');
assert.equal(autoSwitch.attrs['aria-checked'], 'false');

console.log('PASS updater pill, skip-until-next-version, release-notes dialog, and settings controls');
