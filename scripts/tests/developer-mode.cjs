const fs = require('node:fs'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync('native/macos/UI/developer.js', 'utf8');

function makeNode(tag = 'div') {
  const n = {
    tag,
    hidden: false,
    open: false,
    value: '',
    textContent: '',
    title: '',
    innerHTML: '',
    children: [],
    attrs: {},
    listeners: {},
    append(...nodes) { this.children.push(...nodes); },
    setAttribute(k, v) { this.attrs[k] = v; },
    getAttribute(k) { return this.attrs[k] ?? null; },
    addEventListener(k, fn) { this.listeners[k] = fn; },
    showModal() { this.open = true; },
    close() { this.open = false; },
    focus() {},
    select() {}
  };
  return n;
}

const brand = makeNode('div');
const badge = makeNode('button'); badge.hidden = true;
const dialogNodes = {
  '#developer-dialog-title': makeNode('strong'),
  '.developer-close': makeNode('button'),
  '#developer-env-label': makeNode('div'),
  '[data-env="production"]': makeNode('button'),
  '[data-env="integration"]': makeNode('button'),
  '[data-env="custom"]': makeNode('button'),
  '.developer-custom-form': makeNode('form'),
  '.developer-custom-input': makeNode('input'),
  '.developer-custom-apply': makeNode('button'),
  '#developer-api-value': makeNode('code'),
  '#developer-ssh-value': makeNode('code'),
  '#developer-nodelay': makeNode('button')
};
let createdDialog = null;
const sent = [];

const context = vm.createContext({
  window: {},
  Date,
  URL,
  document: {
    body: { append(d) { createdDialog = d; } },
    querySelector: s => (s === '.brand' ? brand : s === '#env-badge' ? badge : null),
    createElement: tag => {
      const n = makeNode(tag);
      if (tag === 'dialog') n.querySelector = s => dialogNodes[s];
      return n;
    }
  },
  translate: s => s,
  send: msg => sent.push(msg)
});

vm.runInContext(source, context);

// 1. Plain clicks without Ctrl never open the developer modal.
for (let i = 0; i < 6; i++) {
  brand.listeners.mousedown({ ctrlKey: false, preventDefault() {}, stopPropagation() {} });
}
assert.equal(createdDialog.open, false, 'plain clicks must not open developer modal');

// 2. Five Ctrl+clicks open the developer modal; badge starts hidden on production.
assert.equal(badge.hidden, true);
for (let i = 0; i < 5; i++) {
  brand.listeners.mousedown({ ctrlKey: true, preventDefault() {}, stopPropagation() {} });
}
assert.equal(createdDialog.open, true, '5 Ctrl+clicks open developer modal');

// 3. Selecting Integration switches API and SSH endpoints and reveals INT badge.
dialogNodes['[data-env="integration"]'].onclick();
assert.equal(sent.pop(), 'settings:{"environment":"integration"}');
assert.equal(dialogNodes['#developer-api-value'].textContent, 'https://api.runta.xyz');
assert.equal(dialogNodes['#developer-ssh-value'].textContent, 'ssh.runta.xyz');
assert.equal(badge.hidden, false);
assert.equal(badge.textContent, 'INT');

// 4. Toggling SSH TCP_NODELAY updates state and sends ssh_nodelay setting.
dialogNodes['#developer-nodelay'].onclick();
assert.equal(sent.pop(), 'settings:{"ssh_nodelay":false}');
assert.equal(dialogNodes['#developer-nodelay'].attrs['aria-checked'], 'false');

console.log('PASS developer modal Ctrl+5-click gesture, integration endpoint switch, and SSH TCP_NODELAY toggle');
