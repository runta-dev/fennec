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
      contains(name) { return this.classes.has(name); }
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

const modeButtons = [
  makeNode('button', { sandboxMode: 'read-only' }),
  makeNode('button', { sandboxMode: 'workspace' }),
  makeNode('button', { sandboxMode: 'full' })
];
const modeGroup = makeNode('div');
modeGroup.querySelectorAll = () => modeButtons;

const filterButtons = [
  makeNode('button', { auditFilter: 'all' }),
  makeNode('button', { auditFilter: 'blocked' }),
  makeNode('button', { auditFilter: 'approved' })
];

const nodes = {
  '#sandbox-segments': modeGroup,
  '#sandbox-network': makeNode('button'),
  '#sandbox-roots-section': makeNode('div'),
  '#sandbox-roots-list': makeNode('div'),
  '#add-sandbox-root': makeNode('button'),
  '#clear-trust': makeNode('button'),
  '#audit-summary-badge': makeNode('span'),
  '#audit-clear': makeNode('button'),
  '#audit-count-all': makeNode('span'),
  '#audit-count-blocked': makeNode('span'),
  '#audit-count-approved': makeNode('span'),
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
    querySelectorAll: sel => (sel === '[data-audit-filter]' ? filterButtons : []),
    createElement: tag => makeNode(tag)
  },
  translate: s => s.replace(/^\[\[i18n:ui\.([a-z0-9_]+)\]\]$/, '$1'),
  send: msg => sent.push(msg),
  tab: name => tabs.push(name)
});

vm.runInContext(source, context);

// 1. Render initial preferences with workspace mode, 1 synced + 1 custom root, and 7-day summary.
context.window.renderSandboxPreferences({
  sandbox_mode: 'workspace',
  sandbox_network: false,
  sandbox_roots: ['/Users/alice/work/custom-repo'],
  effective_sandbox_roots: ['/Users/alice/work/custom-repo', '/Users/alice/work/synced-repo'],
  auto_access_granted: true,
  audit_summary: { total_7d: 6, today_count: 2, blocked_7d: 1, retention_days: 7 }
});
assert.equal(modeButtons[1].attrs['aria-checked'], 'true');
assert.equal(nodes['#sandbox-network'].attrs['aria-checked'], 'false');
assert.equal(nodes['#clear-trust'].disabled, false);
assert.equal(nodes['#clear-trust'].textContent, 'revoke_access');

// Revoking automatic access gives immediate in-place button feedback and disables repeat revoke
context.window.renderAutoAccessRevoked(true);
assert.equal(nodes['#clear-trust'].disabled, true);
assert.equal(nodes['#clear-trust'].classList.contains('is-revoked'), true);
if (pendingTimer) pendingTimer();
assert.equal(nodes['#clear-trust'].classList.contains('is-revoked'), false);
assert.equal(nodes['#clear-trust'].textContent, 'ask_each_time');
assert.equal(nodes['#sandbox-roots-list'].children.length, 2);
assert.equal(nodes['#sandbox-roots-list'].children[0].children[0].textContent, '~/work/custom-repo');
assert.equal(nodes['#audit-summary-badge'].textContent, '6 · 1 audit_summary_blocked');
assert.equal(nodes['#audit-summary-badge'].classList.contains('has-blocked'), true);

// 2. Switching to Full mode disables network toggle (showing true) and dims roots without removing rows.
modeButtons[2].onclick();
assert.equal(sent.pop(), 'settings:{"sandbox_mode":"full"}');
assert.equal(nodes['#sandbox-network'].disabled, true);
assert.equal(nodes['#sandbox-network'].attrs['aria-checked'], 'true');
assert.equal(nodes['#sandbox-roots-section'].classList.contains('is-inactive'), true);
assert.equal(nodes['#sandbox-roots-list'].children.length, 2, 'rows remain in place for zero layout shift');

// 3. Switching back to Workspace re-enables network toggle and picking/removing custom folders.
modeButtons[1].onclick();
assert.equal(sent.pop(), 'settings:{"sandbox_mode":"workspace"}');
assert.equal(nodes['#sandbox-network'].disabled, false);
assert.equal(nodes['#sandbox-network'].attrs['aria-checked'], 'false');
context.window.renderPickedSandboxRoot('/Users/alice/work/another');
assert.equal(sent.pop(), 'settings:{"sandbox_roots":["/Users/alice/work/another","/Users/alice/work/custom-repo"]}');

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
assert.equal(nodes['#audit-list'].children.length, 2);
assert.equal(nodes['#audit-clear'].disabled, false);

// Short blocked entry explains itself inline without raw sandbox-exec stderr dump
const blockedMeta = nodes['#audit-list'].children[0].children[0].children[1].children[1].children[0].textContent;
assert.ok(!blockedMeta.includes('sandbox-exec') && !blockedMeta.includes('ms'), 'no raw implementation details exposed');

// Long command row expands full redacted command cleanly without exit code or stderr dump
nodes['#audit-list'].children[1].children[0].onclick();
assert.equal(nodes['#audit-list'].children[1].children.length, 2, 'clicking long command row expands full command');
assert.ok(nodes['#audit-list'].children[1].children[1].children[0].textContent.includes('••••'), 'redacted token uses clean mask');

// Filter to blocked
filterButtons[1].onclick();
assert.equal(sent.pop(), 'audit-list:{"filter":"blocked","limit":100}');

// Clear log
nodes['#audit-clear'].onclick();
assert.equal(sent.pop(), 'audit-clear');

console.log('PASS sandbox segmented modes, custom/synced writable roots, and 7-day security audit timeline');
