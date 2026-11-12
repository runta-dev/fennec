const fs = require('node:fs'), vm = require('node:vm'), assert = require('node:assert/strict');
const source = fs.readFileSync('native/macos/UI/sandbox-audit.js', 'utf8');

function makeNode(tag = 'div', dataset = {}) {
  return {
    tag,
    dataset: { ...dataset },
    disabled: false,
    hidden: false,
    tabIndex: 0,
    className: '',
    textContent: '',
    title: '',
    innerHTML: '',
    children: [],
    attrs: {},
    classList: {
      classes: new Set(),
      toggle(name, force) {
        if (force === undefined) {
          if (this.classes.has(name)) this.classes.delete(name);
          else this.classes.add(name);
        } else if (force) this.classes.add(name);
        else this.classes.delete(name);
      },
      contains(name) { return this.classes.has(name); },
      add(name) { this.classes.add(name); }
    },
    append(...nodes) { this.children.push(...nodes); },
    replaceChildren(...nodes) { this.children = [...nodes]; },
    setAttribute(k, v) { this.attrs[k] = v; },
    getAttribute(k) { return this.attrs[k] ?? null; },
    removeAttribute(k) { delete this.attrs[k]; },
    querySelector() { return makeNode('span'); },
    focus() {}
  };
}

const modeSelect = makeNode('select');
modeSelect.value = 'auto';

const filterSelect = makeNode('select');
filterSelect.value = 'all';
filterSelect.options = ['all', 'blocked', 'approved'].map(value => ({ value, dataset: {} }));
const auditHead = makeNode('div');
auditHead.classList.add('is-empty');

const nodes = {
  '#permission-mode': modeSelect,
  '#audit-head': auditHead,
  '#audit-filter': filterSelect,
  '#audit-list': makeNode('div')
};

const sent = [];
const tabs = [];

let pendingTimer = null;
const context = vm.createContext({
  window: {},
  Date,
  setTimeout: fn => { pendingTimer = fn; return 1; },
  clearTimeout: () => { pendingTimer = null; },
  document: {
    querySelector: sel => nodes[sel] || null,
    querySelectorAll: () => [],
    createElement: tag => makeNode(tag)
  },
  translate: s => s.replace(/^\[\[i18n:ui\.([a-z0-9_]+)\]\]$/, '$1'),
  send: msg => sent.push(msg),
  tab: name => tabs.push(name)
});

vm.runInContext(source, context);

// 1. Render initial preferences with Approve for me and a 7-day summary.
context.window.renderSandboxPreferences({
  permission_mode: 'auto',
  audit_summary: { total_7d: 6, today_count: 2, blocked_7d: 1, retention_days: 7 }
});
assert.equal(modeSelect.value, 'auto');
assert.deepEqual(filterSelect.options.map(option => option.dataset.account), ['6', '1', '5']);
assert.equal(auditHead.classList.contains('is-empty'), false, 'filter appears once there is activity');

// 2. Switching to Full access sends one explicit permission choice.
modeSelect.value = 'full';
modeSelect.onchange();
assert.equal(sent.pop(), 'settings:{"permission_mode":"full"}');

// 3. Switching back to Approve for me.
modeSelect.value = 'auto';
modeSelect.onchange();
assert.equal(sent.pop(), 'settings:{"permission_mode":"auto"}');

// 4. Selecting the Governance tab loads 7-day audit entries, expands detail, filters, and clears.
context.window.onGovernanceTabShown();
assert.equal(sent.pop(), 'audit-list:{"filter":"all","limit":100}');
context.window.openSecurityAuditView();
assert.equal(tabs.pop(), 'governance');

context.window.renderAuditLog({
  summary: { total_7d: 2, today_count: 2, blocked_7d: 1, retention_days: 7 },
  entries: [
    {
      id: 'e1',
      timestamp_ms: Date.now() - 15000,
      kind: 'exec',
      command: 'cat ~/.ssh/id_ed25519',
      workdir: '/Users/alice/work/synced-repo',
      sandbox_mode: 'workspace',
      network_allowed: false,
      decision: 'sandbox_blocked',
      blocked: true,
      exit_code: 1,
      duration_ms: 8,
      detail: 'sandbox-exec: Operation not permitted'
    },
    {
      id: 'e2',
      timestamp_ms: Date.now() - 30000,
      kind: 'exec',
      command: 'OPENAI_API_KEY=[REDACTED] go test -race -count=1 ./internal/sandbox ./internal/audit',
      workdir: '/Users/alice/work/synced-repo',
      sandbox_mode: 'workspace',
      network_allowed: false,
      decision: 'auto_sandbox',
      blocked: false,
      exit_code: 0,
      duration_ms: 120
    }
  ]
});
assert.equal(nodes['#audit-list'].children.length, 3, 'two entries plus the Clear activity row');
const clearRow = nodes['#audit-list'].children[2];
assert.equal(clearRow.className, 'audit-clear-row');

// Short blocked entry explains itself inline without raw sandbox-exec stderr dump
const blockedMeta = nodes['#audit-list'].children[0].children[0].children[1].children[1].children[0].textContent;
assert.ok(!blockedMeta.includes('sandbox-exec') && !blockedMeta.includes('ms'), 'no raw implementation details exposed');

// Long command row expands full redacted command cleanly without exit code or stderr dump
nodes['#audit-list'].children[1].children[0].onclick();
assert.equal(nodes['#audit-list'].children[1].children.length, 2, 'clicking long command row expands full command');
assert.ok(nodes['#audit-list'].children[1].children[1].children[0].textContent.includes('••••'), 'redacted token uses clean mask');

// Filter to blocked
filterSelect.value = 'blocked';
filterSelect.onchange();
assert.equal(sent.pop(), 'audit-list:{"filter":"blocked","limit":100}');

// Clear log
clearRow.onclick();
assert.equal(sent.pop(), 'audit-clear');

// Empty activity is a single quiet row with no Clear action, and the filter hides on the unfiltered view.
context.window.renderAuditLog({ summary: { total_7d: 0, blocked_7d: 0, retention_days: 7 }, entries: [] });
assert.equal(nodes['#audit-list'].children.length, 1);
assert.equal(nodes['#audit-list'].children[0].className, 'audit-empty-row');
assert.equal(nodes['#audit-list'].children[0].textContent, 'activity_empty_blocked', 'blocked filter keeps its own empty copy');
filterSelect.value = 'all';
filterSelect.onchange();
context.window.renderAuditLog({ summary: { total_7d: 0, blocked_7d: 0, retention_days: 7 }, entries: [] });
assert.equal(nodes['#audit-list'].children[0].textContent, 'activity_empty');
assert.equal(auditHead.classList.contains('is-empty'), true);

console.log('PASS permission select, custom/synced writable roots, and 7-day security audit timeline');

// Legacy state remains visible, while new selections use the three permission presets.
context.window.renderSandboxPreferences({ sandbox_mode: 'read-only' });
assert.equal(modeSelect.value, 'ask');
modeSelect.value = 'auto';
modeSelect.onchange();
assert.equal(sent.pop(), 'settings:{"permission_mode":"auto"}');
