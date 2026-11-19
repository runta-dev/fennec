// The Get started card (native/macos/UI/get-started.js) in each state the
// design (docs/get-started.md) names, with a minimal DOM.
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');

class El {
  constructor(tag) { this.tagName = tag; this.children = []; this.attrs = {}; this.hidden = false; this.disabled = false; this.className = ''; this.id = ''; }
  append(...nodes) { for (const n of nodes) this.children.push(typeof n === 'string' ? { text: n } : n); }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  insertBefore(node) { this.children.unshift(node); }
  set textContent(v) { this.children = [{ text: String(v) }]; }
  get textContent() { return this.children.map(c => (c.text !== undefined ? c.text : c.textContent)).join(''); }
  set innerHTML(_) { this.children = []; }
  get lastChild() { return this.children[this.children.length - 1]; }
  setAttribute(k, v) { this.attrs[k] = v; }
  click() { if (this.onclick && !this.disabled) this.onclick(); }
  all(pred, out = []) { for (const c of this.children) if (c instanceof El) { if (pred(c)) out.push(c); c.all(pred, out); } return out; }
  byClass(name) { return this.all(c => c.className.split(' ').includes(name)); }
}

function load() {
  const agents = new El('section'); const list = new El('div'); agents.append(list);
  const sent = []; const timers = []; const clock = { now: 1_000_000 };
  const ctx = {
    Date: { now: () => clock.now },
    document: { createElement: tag => new El(tag), querySelector: sel => ({ '#agents': agents, '#agent-list': list }[sel] || null) },
    send: action => sent.push(action),
    setTimeout: (fn) => { timers.push(fn); return timers.length; }, clearTimeout: () => {},
    authorized: true, detectedAgents: [], takeoverAgents: new Set(), activeTakeoverAgents: new Set(),
    takeoverChanging: false, takeoverSelectionKnown: true, routeResults: new Map(), lastSessionTerminals: [],
    fennecPreparationStage: '',
  };
  ctx.window = ctx;
  for (const name of ['renderAgents', 'renderTakeover', 'renderHerdrSessions', 'renderPreparation', 'renderRouteResults', 'renderAuthorized', 'renderSignedOut', 'renderPreferences']) ctx[name] = () => {};
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync('native/macos/UI/get-started.js', 'utf8'), ctx);
  const card = agents.children[0];
  return { ctx, card, sent, timers, clock, render: () => ctx.renderAgents(ctx.detectedAgents), prefs: v => ctx.renderPreferences(v) };
}
const codex = { id: 'codex', name: 'Codex CLI', icon: 'codex.svg', installed: true, cloudSupported: true };
const claude = { id: 'claude', name: 'Claude Code', icon: 'claude.svg', installed: true, cloudSupported: true };
const pi = { id: 'pi', name: 'Pi', icon: 'pi.svg', installed: true, cloudSupported: true };
const t = (message, fn) => { fn(); console.log('PASS ' + message); };

t('hidden until signed in and preferences are known', () => {
  const s = load(); s.render(); assert.equal(s.card.hidden, true);
  s.ctx.authorized = false; s.prefs({}); assert.equal(s.card.hidden, true);
});

t('step 1 waits for the cloud computer; chips are disabled with the reason', () => {
  const s = load(); s.ctx.detectedAgents = [codex]; s.prefs({});
  assert.equal(s.card.hidden, false);
  assert.equal(s.card.byClass('gs-count')[0].textContent, '0 / 3');
  assert.match(s.card.byClass('current')[0].textContent, /gs_cloud_ready/);
});

t('step 2 lists installed agents, Codex and Claude Code first, and a click selects one', () => {
  const s = load(); s.ctx.detectedAgents = [pi, claude, codex]; s.ctx.fennecPreparationStage = 'agents_installed'; s.prefs({});
  assert.equal(s.card.byClass('gs-count')[0].textContent, '1 / 3');
  const chips = s.card.byClass('gs-chip');
  assert.deepEqual(chips.map(c => c.textContent), ['Codex CLI', 'Claude Code', 'Pi']);
  chips[0].click(); assert.deepEqual(s.sent, ['toggle-takeover:codex']);
});

t('a chip without a local sign-in to copy offers sign-in', () => {
  const s = load(); let login = '';
  s.ctx.startAgentLogin = id => { login = id; };
  s.ctx.detectedAgents = [claude]; s.ctx.fennecPreparationStage = 'agents_installed';
  s.ctx.routeResults.set('claude', { status: 'credentials_required' }); s.prefs({});
  const chip = s.card.byClass('gs-chip')[0];
  assert.match(chip.textContent, /gs_sign_in/); chip.click();
  assert.equal(login, 'claude'); assert.deepEqual(s.sent, []);
});

t('no supported agent installed: install links instead of chips', () => {
  const s = load(); s.ctx.detectedAgents = [{ ...codex, installed: false }]; s.ctx.fennecPreparationStage = 'agents_installed'; s.prefs({});
  assert.equal(s.card.byClass('gs-chip').length, 0);
  assert.match(s.card.textContent, /gs_install_an_agent/);
  s.card.byClass('gs-link')[0].click();
  assert.match(s.sent[0], /^update-open-url:.*github\.com\/openai\/codex/);
});

function step3() {
  const s = load(); s.ctx.detectedAgents = [codex, claude]; s.ctx.fennecPreparationStage = 'agents_installed';
  s.ctx.takeoverAgents.add('codex'); s.ctx.activeTakeoverAgents.add('codex'); s.prefs({});
  return s;
}

t('step 3 shows the command and checks the terminal once, not on every render', () => {
  const s = step3();
  assert.equal(s.card.byClass('gs-count')[0].textContent, '2 / 3');
  assert.equal(s.card.byClass('gs-code')[0].textContent, 'codex');
  assert.match(s.card.textContent, /gs_checking_terminal/);
  s.render(); s.render();
  assert.deepEqual(s.sent.filter(a => a.startsWith('terminal-')), ['terminal-coverage']);
  s.card.byClass('gs-copy')[0].click(); assert.equal(s.sent.at(-1), 'copy-text:codex');
});

t('covered terminal shows nothing more', () => {
  const s = step3(); s.ctx.renderTerminalCoverage({ agents: { codex: { coverage: 'covered_by_function' } } });
  assert.equal(s.card.byClass('gs-problem').length, 0); assert.equal(s.card.byClass('gs-fix').length, 0);
});

t('uncovered terminal offers Fix and the line to add', () => {
  const s = step3(); s.ctx.renderTerminalCoverage({ agents: { codex: { coverage: 'not_covered' } }, snippet: "[ -r '/h/i.sh' ] && . '/h/i.sh'" });
  assert.match(s.card.byClass('gs-problem')[0].textContent, /^codex .*gs_still_runs_on_mac/);
  s.card.byClass('gs-fix')[0].click(); assert.equal(s.sent.at(-1), 'terminal-fix');
  assert.equal(s.card.byClass('gs-fix')[0].disabled, true);
  assert.ok(s.card.byClass('gs-code').some(c => c.textContent.includes('/h/i.sh')));
});

t("the user's alias is explained, never offered a Fix", () => {
  const s = step3(); s.ctx.renderTerminalCoverage({ agents: { codex: { coverage: 'shadowed_by_alias' } } });
  assert.match(s.card.byClass('gs-problem')[0].textContent, /gs_is_your_alias/);
  assert.equal(s.card.byClass('gs-fix').length, 0);
});

t('a session on the cloud computer finishes the card, which then leaves', () => {
  const s = step3(); s.ctx.lastSessionTerminals = [{ state: 'live', labels: { 'fennec.agent': 'codex' } }]; s.render();
  assert.equal(s.card.byClass('gs-count')[0].textContent, '3 / 3');
  assert.match(s.card.byClass('gs-finished')[0].textContent, /gs_detach_hint/);
  assert.equal(s.card.hidden, false);
  assert.equal(s.sent.at(-1), 'settings:{"get_started_done":true}', 'completion must be saved before the display timer');
  s.timers[0]();
  assert.equal(s.sent.at(-1), 'settings:{"get_started_done":true}');
  assert.equal(s.card.hidden, true);
});

t('a finished card stays complete when cloud selection temporarily disappears', () => {
  const s = load(); s.ctx.detectedAgents = [codex]; s.ctx.fennecPreparationStage = 'agents_installed';
  s.ctx.takeoverAgents.add('codex'); s.prefs({ get_started_done: true }); assert.equal(s.card.hidden, true);
  s.ctx.takeoverAgents.clear(); s.render();
  assert.ok(!s.sent.includes('settings:{"get_started_done":false}')); assert.equal(s.card.hidden, true);
  s.prefs({}); s.ctx.fennecPreparationStage = 'offline'; s.render(); assert.equal(s.card.hidden, true);
});

t('hiding is saved and kept', () => {
  const s = load(); s.ctx.detectedAgents = [codex]; s.prefs({});
  s.card.byClass('gs-close')[0].click();
  assert.equal(s.sent.at(-1), 'settings:{"get_started_hidden":true}'); assert.equal(s.card.hidden, true);
  s.prefs({ get_started_hidden: true }); s.render(); assert.equal(s.card.hidden, true);
});

t('an unchanged card is not rebuilt when the panel re-renders (no flashing)', () => {
  const s = step3(); s.ctx.renderTerminalCoverage({ agents: { codex: { coverage: 'covered_by_function' } } });
  const before = s.card.children[0];
  for (let i = 0; i < 5; i++) s.ctx.renderHerdrSessions([]);
  assert.equal(s.card.children[0], before, 'the card was rebuilt with nothing changed');
  s.ctx.lastSessionTerminals = [{ state: 'live', labels: { 'fennec.agent': 'codex' } }]; s.ctx.renderHerdrSessions([]);
  assert.notEqual(s.card.children[0], before, 'a change was not shown');
});

t('the close button uses the shared close button style', () => {
  const s = load(); s.ctx.detectedAgents = [codex]; s.prefs({});
  const close = s.card.byClass('gs-close')[0];
  assert.ok(close.className.split(' ').includes('ios-app-close') && close.className.split(' ').includes('icon'));
});

t('a moment without the session, the active agent or the cloud does not drop a step or rebuild', () => {
  const s = step3(); s.ctx.lastSessionTerminals = [{ state: 'live', labels: { 'fennec.agent': 'codex' } }]; s.render();
  const before = s.card.children[0];
  s.ctx.lastSessionTerminals = []; s.ctx.renderHerdrSessions([]);
  s.ctx.activeTakeoverAgents.clear(); s.ctx.renderTakeover();
  s.ctx.fennecPreparationStage = 'checking'; s.ctx.renderPreparation();
  assert.equal(s.card.byClass('gs-count')[0].textContent, '3 / 3');
  assert.equal(s.card.children[0], before, 'the card was rebuilt for a moment of missing state');
  s.timers[0]();
  assert.equal(s.sent.at(-1), 'settings:{"get_started_done":true}', 'finishing was cancelled');
});

t('completion is retained beyond the transient-state hold interval', () => {
  const s = step3(); s.ctx.lastSessionTerminals = [{ state: 'live', labels: { 'fennec.agent': 'codex' } }]; s.render();
  s.ctx.lastSessionTerminals = []; s.render();
  s.clock.now += 16_000; s.render();
  assert.equal(s.card.byClass('gs-count')[0].textContent, '3 / 3');
  assert.equal(s.sent.at(-1), 'settings:{"get_started_done":true}');
});

t('removing the agent from the cloud computer undoes step 2 at once', () => {
  const s = step3(); s.ctx.takeoverAgents.clear(); s.ctx.activeTakeoverAgents.clear(); s.ctx.takeoverAgents.add('claude'); s.render();
  assert.equal(s.card.byClass('gs-count')[0].textContent, '1 / 3');
});

t('a newer terminal check with the same result does not rebuild', () => {
  const s = step3(); s.ctx.renderTerminalCoverage({ checked_at: 1, agents: { codex: { coverage: 'not_covered' } } });
  const before = s.card.children[0];
  s.ctx.renderTerminalCoverage({ checked_at: 2, agents: { codex: { coverage: 'not_covered' } } });
  assert.equal(s.card.children[0], before);
});

t('preferences read before a save do not bring a hidden or finished card back', () => {
  const s = load(); s.ctx.detectedAgents = [codex]; s.prefs({});
  s.card.byClass('gs-close')[0].click(); s.prefs({}); assert.equal(s.card.hidden, true);
  s.prefs({ get_started_hidden: true }); s.prefs({}); assert.equal(s.card.hidden, true, 'a late snapshot cannot undo a confirmed hide');
});

t('the iPhone row opens the iPhone dialog', () => {
  const s = load(); let opened = false; s.ctx.detectedAgents = [codex];
  const button = { click: () => { opened = true; } };
  s.ctx.document.querySelector = sel => (sel === '#get-ios-app' ? button : null);
  s.prefs({}); s.card.byClass('gs-phone')[0].click(); assert.equal(opened, true);
});

t('a cloud computer prepared before shows the agents at once while it is checked again', () => {
  const s = load(); s.ctx.detectedAgents = [codex]; s.prefs({});
  s.ctx.renderPreparation({ stage: 'connecting', prepared_before: 'true' });
  assert.equal(s.card.byClass('gs-count')[0].textContent, '1 / 3');
  assert.equal(s.card.byClass('gs-chip').length, 1);
  s.ctx.fennecPreparationStage = 'failed'; s.ctx.renderPreparation({ stage: 'failed', prepared_before: 'true' });
  s.clock.now += 16_000; s.render();
  assert.equal(s.card.byClass('gs-count')[0].textContent, '0 / 3', 'a failed check must still undo step 1');
});
