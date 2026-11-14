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

// 1. A ready update shows the header pill and the Settings action.
context.window.renderUpdateState({ state: 'ready', current: '0.2.100', latest: '0.2.200', notes: '' });
assert.equal(pill.hidden, false, 'header pill visible when update is ready');
assert.equal(versionActions.children.length, 1);
assert.equal(versionActions.children[0].textContent, '[[i18n:ui.restart_to_update]]');

// 2. One click updates: no dialog, the install starts at once.
pill.onclick();
assert.equal(sent.pop(), 'update-install');
assert.equal(createdDialog, null, 'no confirmation dialog');
assert.equal(pillLabel.textContent, '[[i18n:ui.restarting]]');
pill.onclick();
assert.equal(sent.length, 0, 'a second click does not install twice');

// 3. A failed install lets the user try again from the pill.
context.window.renderUpdateInstallFailed();
assert.equal(pillLabel.textContent, '[[i18n:ui.update_failed]]');
pill.onclick();
assert.equal(sent.pop(), 'update-install');
context.window.renderUpdateInstallFailed();

// 4. An update not downloaded yet: one click downloads it, then installs
// as soon as it is ready, without a second click.
context.window.renderUpdateState({ state: 'found', current: '0.2.100', latest: '0.2.300', size: 33e6 });
assert.equal(versionActions.children[0].textContent, '[[i18n:ui.update]]');
versionActions.children[0].onclick();
assert.equal(sent.pop(), 'update-download');
context.window.renderUpdateState({ state: 'downloading', current: '0.2.100', latest: '0.2.300', done: 1, total: 2 });
assert.equal(sent.length, 0);
context.window.renderUpdateState({ state: 'ready', current: '0.2.100', latest: '0.2.300' });
assert.equal(sent.pop(), 'update-install', 'installs once the download is ready');

// 5. Without a click, a downloaded update waits for the user.
context.window.renderUpdateInstallFailed();
context.window.renderUpdateState({ state: 'ready', current: '0.2.100', latest: '0.2.400' });
assert.equal(sent.length, 0, 'no install without a click');

// 6. Toggling Automatic updates switch sends settings update.
autoSwitch.onclick();
assert.equal(sent.pop(), 'settings:{"auto_update":false}');
assert.equal(autoSwitch.attrs['aria-checked'], 'false');

console.log('PASS updater pill, one-click update without a dialog, and settings controls');
