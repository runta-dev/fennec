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
  '#onboarding-tagline': makeNode('p'),
  '#onboarding-login-btn': makeNode('button'),
  '#onboarding-login-hint': makeNode('p'),
  '#onboarding-story-title': makeNode('strong'),
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
      n.querySelectorAll = () => [];
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

// 4. With requestAnimationFrame active, pseudo-progress trickles smoothly while waiting,
// accelerates across real stage breakpoints, never regresses, and sprints to 100% on completion.
{
  let rafNow = 1000;
  let rafQueue = [];
  const rafNodes = {
    '#onboarding-signin': makeNode('div'),
    '#onboarding-setup': makeNode('div'),
    '#onboarding-logo-hit': makeNode('div'),
    '#onboarding-env-badge': makeNode('button'),
    '#onboarding-tagline': makeNode('p'),
    '#onboarding-login-btn': makeNode('button'),
    '#onboarding-login-hint': makeNode('p'),
    '#onboarding-always-on': makeNode('span'),
    '#onboarding-cloud-caption': makeNode('span'),
    '#onboarding-term-cmd': makeNode('span'),
    '#onboarding-story-title': makeNode('strong'),
    '#onboarding-peek-stage': makeNode('div'),
    '#onboarding-bar-fill': makeNode('div'),
    '#onboarding-stage-text': makeNode('span'),
    '#onboarding-action-btn': makeNode('button')
  };
  let rafStage = null;
  const rafBody = makeNode('body');
  rafBody.append = el => { rafStage = el; };
  const rafCtx = vm.createContext({
    window: {
      renderSignedOut() {},
      renderAuthorized() {},
      renderLogin() {},
      renderPreparation() {},
      renderPreferences() {},
      renderDeveloperPreferences() {}
    },
    Date: { now: () => rafNow },
    requestAnimationFrame: fn => { rafQueue.push(fn); return rafQueue.length; },
    cancelAnimationFrame() {},
    setTimeout: fn => { fn(); return 1; },
    clearTimeout() {},
    setInterval: () => 2,
    clearInterval() {},
    document: {
      hidden: false,
      body: rafBody,
      createElement: tag => {
        const n = makeNode(tag);
        n.querySelector = s => rafNodes[s] || null;
        n.querySelectorAll = () => [];
        return n;
      }
    },
    translate: s => s,
    send() {}
  });
  const stepFrames = (count, dtMs = 50) => {
    for (let i = 0; i < count; i++) {
      rafNow += dtMs;
      const batch = rafQueue.splice(0, rafQueue.length);
      batch.forEach(fn => fn());
    }
  };
  const parsePct = () => parseFloat(rafNodes['#onboarding-bar-fill'].style.width);

  vm.runInContext(source, rafCtx);
  rafCtx.window.renderAuthorized();
  rafCtx.window.renderPreparation({ stage: 'allocating', status: '' });
  const p0 = parsePct();
  stepFrames(10, 50); // 0.5s in allocating: catches up past 14% and trickles smoothly
  const p1 = parsePct();
  stepFrames(10, 50); // another 0.5s waiting in allocating: keeps trickling without stalling
  const p2 = parsePct();
  assert.ok(p1 > p0 && p2 > p1 && p2 < 40, `expected smooth trickle in allocating, got ${p0} -> ${p1} -> ${p2}`);

  // Jump to installing_session_service (floor 80%): should accelerate rapidly!
  rafCtx.window.renderPreparation({ stage: 'installing_session_service', status: '' });
  stepFrames(4, 50); // 0.2s burst
  const p3 = parsePct();
  assert.ok(p3 - p2 > 25, `expected rapid acceleration toward 80% floor, got ${p2} -> ${p3}`);

  // Complete: sprints all the way to 100% before dismissing onboarding!
  rafCtx.window.renderPreparation({ stage: 'agents_installed', status: 'ready' });
  stepFrames(12, 30);
  assert.equal(rafStage.hidden, true, 'onboarding dismisses after sprinting to 100%');
}

console.log('PASS full-screen sign-in splash, peeking mascot setup stage, and fast workspace handoff');
