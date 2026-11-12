const fs = require('node:fs'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync('native/macos/UI/onboarding.js', 'utf8');

function makeNode(tag = 'div') {
  const classes = new Set();
  return {
    tag,
    hidden: false,
    disabled: false,
    textContent: '',
    innerHTML: '',
    title: '',
    style: {},
    children: [],
    listeners: {},
    classList: {
      add(c) { classes.add(c); },
      remove(c) { classes.delete(c); },
      toggle(c, on) { if (on) classes.add(c); else classes.delete(c); },
      contains(c) { return classes.has(c); }
    },
    append(...nodes) { this.children.push(...nodes); },
    addEventListener(k, fn) { this.listeners[k] = fn; }
  };
}

const nodes = {
  '#onboarding-signin': makeNode('div'),
  '#onboarding-setup': makeNode('div'),
  '#onboarding-logo-hit': makeNode('div'),
  '#onboarding-env-badge': makeNode('button'),
  '#onboarding-login-btn': makeNode('button'),
  '#onboarding-login-hint': makeNode('p'),
  '#onboarding-peek-stage': makeNode('div'),
  '#onboarding-bar-fill': makeNode('div'),
  '#onboarding-stage-text': makeNode('span'),
  '#onboarding-action-btn': makeNode('button')
};
let stageEl = null;
const bodyEl = makeNode('body');
const sent = [];

const context = vm.createContext({
  window: {
    renderSignedOut() {},
    renderAuthorized() {},
    renderLogin() {},
    renderPreparation() {},
    renderPreferences() {},
    renderDeveloperPreferences() {}
  },
  Date,
  setTimeout: fn => { fn(); return 1; },
  clearTimeout() {},
  setInterval: () => 2,
  clearInterval() {},
  document: {
    body: bodyEl,
    createElement: tag => {
      const n = makeNode(tag);
      n.querySelector = s => nodes[s] || null;
      n.querySelectorAll = s => (s === '#onboarding-dots span' ? dots : []);
      return n;
    }
  },
  translate: s => s,
  send: msg => sent.push(msg)
});
bodyEl.append = el => { stageEl = el; };

vm.runInContext(source, context);

// 1. Starts in clean full-screen sign-in splash when signed out.
assert.equal(stageEl.hidden, false, 'onboarding splash visible when signed out');
assert.equal(nodes['#onboarding-signin'].hidden, false);
assert.equal(nodes['#onboarding-setup'].hidden, true);
assert.equal(bodyEl.classList.contains('is-onboarding'), true);
nodes['#onboarding-login-btn'].onclick();
assert.equal(sent.pop(), 'login');

// 2. Signing in transitions into full-screen cloud setup with peeking mascot & progress.
context.window.renderAuthorized();
context.window.renderPreparation({ stage: 'connecting', status: '' });
assert.equal(stageEl.hidden, false);
assert.equal(nodes['#onboarding-signin'].hidden, true);
assert.equal(nodes['#onboarding-setup'].hidden, false);
assert.equal(nodes['#onboarding-bar-fill'].style.width, '62%');

// 3. Core workspace readiness (agents_installed) smoothly exits onboarding into main UI.
context.window.renderPreparation({ stage: 'agents_installed', status: 'ready' });
assert.equal(stageEl.hidden, true, 'onboarding exits once core cloud workspace is ready');
assert.equal(bodyEl.classList.contains('is-onboarding'), false);

console.log('PASS full-screen sign-in splash, peeking mascot setup stage, and fast workspace handoff');
