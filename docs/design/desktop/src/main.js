// Fennec desktop window mockup: Agents (cloud TUIs) + Projects (request groups, forum style).
const I = {
  search: '<path d="m11 11 3 3"/><circle cx="7" cy="7" r="4.5"/>',
  sidebar: '<rect x="2" y="3" width="12" height="10" rx="2"/><path d="M6 3v10"/>',
  plus: '<path d="M8 3v10M3 8h10"/>',
  gear: '<g transform="scale(.6667)" stroke-width="2.25"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></g>',
  chat: '<path d="M3 4.5A1.5 1.5 0 0 1 4.5 3h7A1.5 1.5 0 0 1 13 4.5v5a1.5 1.5 0 0 1-1.5 1.5H7l-3 2.2V11h0a1 1 0 0 1-1-1Z"/>',
  close: '<path d="m4 4 8 8M12 4l-8 8"/>',
  term: '<rect x="2" y="3" width="12" height="10" rx="2"/><path d="m5 7 2 1.5L5 10M8.5 10H11"/>',
  sort: '<path d="M5 3v10M2.5 10.5 5 13l2.5-2.5M11 13V3M8.5 5.5 11 3l2.5 2.5"/>',
  image: '<rect x="2" y="3" width="12" height="10" rx="2"/><circle cx="6" cy="6.5" r="1.2"/><path d="m3 12 3.5-3.5L9 11l2-2 3 3"/>',
  link: '<path d="M7 9a2.5 2.5 0 0 0 3.5 0l2-2A2.5 2.5 0 0 0 9 3.5l-.7.7M9 7a2.5 2.5 0 0 0-3.5 0l-2 2A2.5 2.5 0 0 0 7 12.5l.7-.7"/>',
  list: '<path d="M6 4h8M6 8h8M6 12h8"/><circle cx="2.8" cy="4" r=".6"/><circle cx="2.8" cy="8" r=".6"/><circle cx="2.8" cy="12" r=".6"/>',
  olist: '<path d="M6.5 4H14M6.5 8H14M6.5 12H14M2.5 3h1v3M2.3 9.2c.4-.6 1.7-.5 1.7.3 0 .8-1.7 1.4-1.7 2.5H4"/>',
  quote: '<path d="M3 4v8M6 5h7M6 8h7M6 11h5"/>',
  code: '<path d="m5.5 4.5-3 3.5 3 3.5M10.5 4.5l3 3.5-3 3.5"/>',
  check: '<path d="m3.5 8.5 3 3 6-7"/>',
  back: '<path d="M10 3.5 5.5 8l4.5 4.5"/>',
  book: '<path d="M3 3.5h4a1.5 1.5 0 0 1 1.5 1.5v8A1.5 1.5 0 0 0 7 11.5H3Zm10 0H9.5A1.5 1.5 0 0 0 8 5v8a1.5 1.5 0 0 1 1.5-1.5H13Z"/>',
  layers: '<path d="m8 2.5 5.5 3L8 8.5l-5.5-3Z"/><path d="m2.5 8.5 5.5 3 5.5-3M2.5 11l5.5 3 5.5-3"/>',
  bot: '<rect x="3" y="5" width="10" height="8" rx="2"/><path d="M8 2.5V5M6 9h.01M10 9h.01"/>',
  sliders: '<path d="M3 5h6M12 5h1M3 11h1M7 11h6"/><circle cx="10.5" cy="5" r="1.5"/><circle cx="5.5" cy="11" r="1.5"/>',
  trash: '<path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5"/>',
};
const icon = (n, cls = '') => `<svg class="i ${cls}" viewBox="0 0 16 16" aria-hidden="true">${I[n]}</svg>`;
const AGENT = { claude: ['/claudecode-color.svg', 'Claude Code'], codex: ['/codex-color.svg', 'Codex'], kimi: ['/kimi-color.svg', 'Kimi Code'] };
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
// Tags are per project (Settings > General); new projects start with these.
const DEFAULT_TAGS = [['bug', 'Bug'], ['feature', 'Feature'], ['discussion', 'Discussion']];
const tagsOf = (p) => p.tags;
const tagLabel = (p, id) => (p.tags.find(([k]) => k === id) || [id, id])[1];
const MODELS = { claude: ['Opus 5.5', 'Sonnet 5.5', 'Haiku 5.5'], codex: ['GPT-5', 'GPT-5 mini'], kimi: ['K2'] };
const TRUST = { unverified: 'Unverified', machine: 'Machine-confirmed', human: 'Human-reviewed' };
const trustOf = (c) => !c.verified?.length ? 'unverified' : c.verified.some((v) => v.by.startsWith('human:')) ? 'human' : 'machine';
const RSTATUS = { you: 'Needs you', queued: 'Queued', working: 'Working', resolved: 'Resolved' };
const PEOPLE = { shiqi: ['S', '#f38d25'], ian: ['I', '#6f8fd9'], mei: ['M', '#5fb08a'], akash: ['A', '#b07ad9'] };

const workers = (...list) => list.map(([agent, model, on = true]) => ({ agent, model, on }));
// The project's knowledge bundle: OKF v0.2 concepts the agents write while
// working on requests (markdown + YAML frontmatter, in the repo).
const FENNEC_KNOWLEDGE = [
  { id: 'architecture/mac-relay', type: 'Architecture', title: 'Mac relay', description: 'How agents on the cloud computer run local commands through the Mac.', tags: ['relay', 'ssh'], status: 'stable',
    generated: { by: 'claude-code/2.1', at: '2026-11-18T09:12Z' }, verified: [{ by: 'human:shiqi', at: '2026-11-18T10:02Z' }],
    sources: ['internal/localssh/', 'docs/mac-relay-health.md'],
    body: '<h4>Overview</h4><p>Agents run on the cloud computer; local commands run through the paired Mac’s SSH relay. A separate resident byte stream checks the connection, with a 5 s timeout.</p><h4>Gotchas</h4><ul><li>Rebuilding the SSH master changes the host key; clear the old known_hosts entry.</li><li>Reverse forwarding allows at most 16 listeners.</li></ul>' },
  { id: 'runbooks/attach-handshake-eof', type: 'Playbook', title: 'fen attach stuck at handshake: EOF', description: 'Troubleshooting steps learned from request FEN-40.', tags: ['ssh', 'attach'], status: 'draft',
    generated: { by: 'claude-code/2.1', at: '2026-11-19T10:41Z' }, sources: ['request #134'],
    body: '<ol><li>Check the relay: <code>fen doctor relay</code></li><li>If the host key does not match, run <code>ssh-keygen -R &lt;mac&gt;</code></li><li>Attach 20 times in a row to confirm it no longer happens.</li></ol>' },
  { id: 'decisions/push-via-backend', type: 'Decision', title: 'iOS push goes through the Runta backend', description: 'The APNs key is not kept on every cloud computer.', tags: ['ios', 'push'], status: 'stable',
    generated: { by: 'codex/0.170', at: '2026-11-18T14:20Z' }, verified: [{ by: 'codex/0.170', at: '2026-11-18T14:30Z' }], sources: ['request #121'],
    body: '<p>A .p8 on every runtime would be exposed to agent code, so the Runta backend sends pushes; cloud computers only report session state.</p>' },
  { id: 'glossary/session', type: 'Reference', title: 'Session', description: 'An agent’s long-lived terminal on the cloud computer.', tags: ['glossary'], status: 'stable',
    generated: { by: 'claude-code/2.1', at: '2026-11-17T08:00Z' }, verified: [{ by: 'human:mei', at: '2026-11-17T09:00Z' }], sources: ['README.md'],
    body: '<p>A terminal managed by Herdr. <code>fen attach &lt;name&gt;</code> reconnects to it; closing the laptop does not end it.</p>' },
];
const projects = [
  { id: 'fennec', org: 'runta', key: 'FEN', next: 38, name: 'Fennec', color: '#f38d25', desc: 'Mac menu bar app, fen CLI and iOS client', repo: 'runta-dev/fennec',
    tags: [...DEFAULT_TAGS], queue: 3, workers: workers(['claude', 'Opus 5.5'], ['codex', 'GPT-5'], ['kimi', 'K2', false]), knowledge: FENNEC_KNOWLEDGE },
  { id: 'website', org: 'runta', key: 'WEB', next: 12, name: 'runta.com', color: '#6f8fd9', desc: 'Runta website and docs', repo: 'runta-dev/website',
    tags: [...DEFAULT_TAGS], queue: 3, workers: workers(['claude', 'Sonnet 5.5']), knowledge: [] },
  { id: 'stone-age', org: 'personal', key: 'STA', next: 5, name: 'Stone Age', color: '#5fb08a', desc: 'TapTap mini game', repo: 'shiqimei/games',
    tags: [...DEFAULT_TAGS], queue: 3, workers: workers(['claude', 'Opus 5.5']), knowledge: [] },
];

let seq = 140;
const requests = [
  { id: 136, project: 'fennec', tag: 'bug', status: 'you', title: 'Queues page overflows sideways in iPhone Safari', author: 'shiqi', ago: '3m', agent: 'claude', msgs: [
    { who: 'shiqi', at: '11:02', html: '<p>On iPhone the Queues page scrolls sideways and the last column is cut off. Chrome is fine; only Safari has the problem.</p>', shot: true },
    { sys: 'Claude Code', agent: 'a1' },
    { who: 'agent', agent: 'claude', at: '11:14', html: '<p>Fixed: narrow screens no longer scroll sideways. Checked in Safari and Chrome:</p>', att: [['link', 'preview-136.fen.run/queues']] },
  ]},
  { id: 135, project: 'fennec', tag: 'feature', status: 'working', title: 'Let people +1 other people’s requests', author: 'mei', ago: '12m', agent: 'codex', votes: 4, msgs: [
    { who: 'mei', at: '10:51', html: '<p>I’d like to +1 requests other people post, and sort the list by +1s so what everyone wants gets worked on first.</p>' },
    { who: 'shiqi', at: '10:55', html: '<p>+1. It would also help if the agent’s reply showed who +1’d it.</p>' },
    { sys: 'Codex', agent: 'a2' },
  ]},
  { id: 134, project: 'fennec', tag: 'bug', status: 'you', title: 'fen attach sometimes hangs at handshake', author: 'shiqi', ago: '26m', agent: 'claude', msgs: [
    { who: 'shiqi', at: '10:30', html: '<p><code>fen attach cone-leaf</code> sometimes hangs at <code>ssh: handshake failed: EOF</code>; it takes three retries to connect.</p>' },
    { sys: 'Claude Code', agent: 'a3' },
    { approve: 'ssh-keygen -R shiqis-macbook-pro.local' },
  ]},
  { id: 133, project: 'fennec', tag: 'discussion', status: 'queued', title: 'How long do sessions survive after the cloud computer shuts down?', author: 'akash', ago: '1h', msgs: [
    { who: 'akash', at: '09:40', html: '<p>If I don’t use it for a week, are my sessions and worktrees still on the cloud computer? Is anything cleaned up automatically?</p>' },
  ]},
  { id: 131, project: 'fennec', tag: 'feature', status: 'resolved', title: 'Magpie toggle label in Settings is too long', author: 'shiqi', ago: 'Yesterday', agent: 'claude', msgs: [
    { who: 'shiqi', at: 'Yesterday', html: '<p>"Use Magpie to choose cloud models for supported agents" wraps onto three lines in Chinese.</p>' },
    { who: 'agent', agent: 'claude', at: 'Yesterday', html: '<p>Changed it to "Choose models with Magpie" in both languages; merged in PR #418.</p>' },
    { sys: 'Resolved' },
  ]},
  { id: 129, project: 'website', tag: 'bug', status: 'working', title: 'iPhone demo’s black frame disappears in dark mode', author: 'shiqi', ago: '40m', agent: 'claude', msgs: [
    { who: 'shiqi', at: '10:10', html: '<p>In dark mode the home page iPhone demo’s frame blends into the background.</p>' },
    { sys: 'Claude Code', agent: 'a4' },
  ]},
  { id: 120, project: 'stone-age', tag: 'bug', status: 'queued', title: 'Saves lost after upgrading to 1.5', author: 'ian', ago: '2h', msgs: [
    { who: 'ian', at: '08:22', html: '<p>After upgrading from 1.4 to 1.5 the save in localStorage was wiped and my tribe went back to level 1.</p>' },
  ]},
];

const agents = [
  { id: 'a3', kind: 'claude', title: 'fen attach handshake', state: 'blocked', age: '26m', from: 134 },
  { id: 'a2', kind: 'codex', title: 'Sort requests by +1', state: 'working', age: '9m', from: 135 },
  { id: 'a4', kind: 'claude', title: 'iPhone demo dark frame', state: 'working', age: '38m', from: 129 },
  { id: 'a1', kind: 'claude', title: 'Queues overflow in Safari', state: 'idle', age: '3m', from: 136 },
  { id: 'a5', kind: 'claude', title: 'Website social card icon', state: 'idle', age: '1h' },
  { id: 'a6', kind: 'kimi', title: 'Weekly report data', state: 'idle', age: '3h' },
];

// Requests are numbered per project, oldest first: FEN-38, FEN-39, …
for (const p of projects) for (const r of requests.filter((x) => x.project === p.id).sort((a, b) => a.id - b.id)) r.num = p.next++;
const projOf = (r) => projects.find((p) => p.id === r.project);
const refOf = (r) => `${projOf(r).key}-${r.num}`;
const keyFrom = (name) => (name.match(/[A-Za-z0-9]/g) || []).join('').slice(0, 3).toUpperCase() || 'P' + (projects.length + 1);
let view = { type: 'project', id: 'fennec' }, openId = 136, composing = false, filter = 'all', scope = 'open', newProject = false;
let newSession = null; // { agent, project } while the New session sheet is open
let knowledgeId = null, knowledgeFilter = 'all';
// The open dropdown (self-drawn, like the Mac panel's choice menu).
let menu = null; // { key, options: [[value, label, icon?]], value, x, y, w }
const chevron = '<svg class="i" viewBox="0 0 16 16"><path d="m4.5 6.5 3.5 3.5 3.5-3.5"/></svg>';
// A dropdown trigger; its menu opens below it and sets `key` (see choose()).
function choice(key, value, options, { placeholder = '', disabled = false } = {}) {
  const current = options.find(([v]) => v === value);
  return `<button type="button" class="choice-trigger ${menu?.key === key ? 'open' : ''}" data-choice="${esc(key)}" ${disabled ? 'disabled' : ''}
    data-options='${esc(JSON.stringify(options))}' data-value="${esc(value ?? '')}">
    ${current?.[2] ? `<img src="${current[2]}" alt="">` : ''}<span class="${current ? '' : 'ph'}">${esc(current ? current[1] : placeholder)}</span>${chevron}</button>`;
}
// Organizations the user belongs to (Runta /v2/organizations). Projects belong to one.
const ORGS = [['runta', 'Runta', '#f38d25'], ['personal', 'Personal', '#8a8f98']];
let org = 'runta', query = '';
const matches = (...texts) => !query || texts.some((t) => t && t.toLowerCase().includes(query.toLowerCase()));
const orgMark = (c, name) => `<span class="proj-mark" style="--c:${c}">${esc(name[0])}</span>`;
function orgSwitch() {
  const [, name, c] = ORGS.find(([k]) => k === org);
  return `<button type="button" class="org-switch ${menu?.key === 'org' ? 'open' : ''}" data-choice="org" data-value="${org}" title="Switch organization"
    data-options='${esc(JSON.stringify([...ORGS.map(([k, n, c]) => [k, n, null, c]), ['new', 'New organization…', null, 'add']]))}'>${orgMark(c, name)}<b>${esc(name)}</b>${chevron}</button>`;
}
function choiceMenu() {
  return `<div class="choice-menu" style="left:${menu.x}px;top:${menu.y}px;min-width:${menu.w}px">${menu.options.map(([v, label, img, c]) => `
    ${c === 'add' ? '<div class="menu-sep"></div>' : ''}<button type="button" data-choose="${esc(v)}" class="${v === menu.value ? 'on' : ''} ${c === 'add' ? 'menu-action' : ''}">${c === 'add' ? icon('plus') : c ? orgMark(c, label) : img ? `<img src="${img}" alt="">` : ''}<span>${esc(label)}</span>
    <span class="choice-check">${v === menu.value ? icon('check') : ''}</span></button>`).join('')}</div>`;
}
function choose(key, value) {
  const [kind, arg] = key.split(':'), p = view.type === 'settings' && projects.find((x) => x.id === view.id);
  if (kind === 'reqTag') filter = value;
  if (kind === 'org' && value !== 'new') { org = value; const first = projects.find((x) => x.org === org); view = first ? { type: 'project', id: first.id } : { type: 'empty' }; filter = 'all'; scope = 'open'; }
  if (kind === 'workerModel') p.workers[+arg].model = value;
  if (kind === 'workerAdd') p.workers.push({ agent: value, model: MODELS[value][0], on: true });
  if (kind === 'sessionProject') { newSession.project = value; newSession.model = null; }
  if (kind === 'sessionModel') newSession.model = value;
}
const $ = (s) => document.querySelector(s);
const mark = (p, cls = '') => `<span class="proj-mark ${cls}" style="--c:${p.color}">${p.icon ? `<img src="${p.icon}" alt="">` : esc(p.name[0])}</span>`;
const COLORS = ['#f38d25', '#d97b6f', '#c9a23d', '#5fb08a', '#4fa3b5', '#6f8fd9', '#9a7bd1', '#8a8f98'];
const reqOf = (id) => requests.find((r) => r.id === id);
const needs = (p) => requests.filter((r) => r.project === p && r.status === 'you').length;
const avatar = (who, agent) => agent
  ? `<span class="av agent"><img src="${AGENT[agent][0]}" alt=""></span>`
  : `<span class="av" style="background:${PEOPLE[who]?.[1] || '#999'}">${PEOPLE[who]?.[0] || '?'}</span>`;

// ---------- sidebar ----------
function sidebar() {
  const groups = [['blocked', 'Needs you'], ['working', 'Working'], ['idle', 'Idle']];
  const agentRows = groups.map(([s, label]) => {
    const list = agents.filter((a) => a.state === s && matches(a.title, AGENT[a.kind][1]));
    if (!list.length) return '';
    return `<div class="group">${label} <span>${list.length}</span></div>` + list.map((a) => `
      <button class="side-row ${view.type === 'agent' && view.id === a.id ? 'sel' : ''}" data-agent="${a.id}">
        <span class="agent-badge"><img class="agent-ico" src="${AGENT[a.kind][0]}" alt=""><span class="state ${a.state}" title="${{ blocked: 'Needs you', working: 'Working', idle: 'Idle' }[a.state]}"></span></span>
        <span class="side-name">${esc(a.title)}</span><span class="side-meta">${a.age}</span></button>`).join('');
  }).join('');
  const projectRows = projects.filter((p) => p.org === org && matches(p.name, p.key, p.desc)).map((p) => {
    const n = needs(p.id);
    return `<button class="side-row ${view.type === 'project' && view.id === p.id ? 'sel' : ''}" data-project="${p.id}">
      ${mark(p)}<span class="side-name">${esc(p.name)}</span>
      ${n ? `<span class="count">${n}</span>` : ''}</button>`;
  }).join('');
  return `<aside class="sidebar">
    <div class="titlebar"><span class="lights"><i></i><i></i><i></i></span>${orgSwitch()}
      <span class="spacer"></span><button class="ib" title="Hide sidebar">${icon('sidebar')}</button></div>
    <label class="side-search">${icon('search')}<input data-search placeholder="Search" value="${esc(query)}" spellcheck="false" autocomplete="off"><kbd>⌘K</kbd></label>
    <div class="side-scroll">
      <div class="section">Projects <button class="ib sm" data-new-project title="New project">${icon('plus')}</button></div>${projectRows}
      <div class="section">Sessions <button class="ib sm" data-new-session title="New session">${icon('plus')}</button></div>${agentRows}
    </div>
    <div class="side-foot"><button class="side-row">${icon('gear')}<span class="side-name">Settings</span></button></div>
  </aside>`;
}

// ---------- editor ----------
function editor(kind) {
  return `<div class="rich" contenteditable="true" data-editor="${kind}" data-ph="${kind === 'reply' ? 'Reply' : 'New Request'}"></div>`;
}

// ---------- project (forum) ----------
function projectView(p) {
  const list = requests.filter((r) => r.project === p.id && (filter === 'all' || r.tag === filter) && matches(r.title, refOf(r), ...r.msgs.map((m) => m.html)));
  const open = list.filter((r) => r.status !== 'resolved'), done = list.filter((r) => r.status === 'resolved');
  const shown = scope === 'open' ? open : done;
  const card = (r) => {
    const last = [...r.msgs].reverse().find((m) => m.html);
    const txt = last ? last.html.replace(/<[^>]+>/g, '') : '';
    const replies = r.msgs.filter((m) => m.html).length - 1;
    return `<button class="req ${openId === r.id ? 'sel' : ''}" data-req="${r.id}">
      <div class="req-top"><span class="req-title">${esc(r.title)}</span><span class="st ${r.status}">${RSTATUS[r.status]}</span></div>
      <div class="req-snip">${esc(txt)}</div>
      <div class="req-meta"><span class="ref">${refOf(r)}</span><span class="tag ${r.tag}">${esc(tagLabel(p, r.tag))}</span>
        ${r.agent ? `<img class="mini" src="${AGENT[r.agent][0]}" alt="">` : ''}
        <span class="m">${icon('chat')} ${replies}</span>${r.votes ? `<span class="m">+${r.votes}</span>` : ''}<span class="m">${r.ago}</span></div>
    </button>`;
  };
  return `<section class="forum">
    <div class="pane-head">${mark(p, 'lg')}
      <div class="ph-text"><b>${esc(p.name)}</b><span>${esc(p.desc)}</span></div>
      <button class="ib" data-settings="${p.id}" title="Project settings">${icon('gear')}</button></div>
    <div class="forum-scroll">
      <form class="composer" data-compose="new">${editor('new')}<button class="btn primary sm send">Post</button></form>
      <div class="filters"><div class="seg">${[['open', 'Open', open.length], ['resolved', 'Resolved', done.length]].map(([k, v, n]) => `<button class="${scope === k ? 'on' : ''}" data-scope="${k}">${v}<span>${n}</span></button>`).join('')}</div>
        <span class="spacer"></span>${choice('reqTag', filter, [['all', 'All tags'], ...tagsOf(p)])}</div>
      ${shown.map(card).join('') || `<div class="empty-list">${scope === 'open' ? 'No open requests' : 'Nothing resolved yet'}${filter !== 'all' ? ` tagged ${esc(tagLabel(p, filter))}` : ''}.</div>`}
    </div>
  </section>`;
}

function threadView(r) {
  if (!r) return `<section class="thread empty-thread">${icon('chat', 'big')}</section>`;
  const body = r.msgs.map((m, i) => {
    if (m.sys) return `<div class="sys">${m.agent ? `<button class="link" data-agent="${m.agent}">${esc(m.sys)}</button>` : esc(m.sys)}</div>`;
    if (m.approve) return `<div class="approval"><pre>${esc(m.approve)}</pre>
      <div class="ap-row"><button class="btn primary sm" data-approve="${r.id}">Allow</button><button class="btn sm" data-deny="${r.id}">Deny</button></div></div>`;
    const isAgent = m.who === 'agent', name = isAgent ? AGENT[m.agent][1] : m.who;
    return `<div class="post ${i === 0 ? 'first' : ''}">${avatar(m.who, isAgent && m.agent)}
      <div class="post-body"><div class="post-h"><b>${esc(name)}</b><span class="t">${m.at}</span></div>
        <div class="md">${m.html}</div>
        ${m.shot ? '<div class="shot"><span></span><span></span><span></span><span></span></div>' : ''}
        ${(m.att || []).filter(([k]) => k === 'link').map(([, v]) => `<a class="att link" href="#">${icon('link')} ${esc(v)}</a>`).join('')}
      </div></div>`;
  }).join('');
  return `<section class="thread">
    <div class="pane-head thread-head"><span class="ref">${refOf(r)}</span><b class="thread-title">${esc(r.title)}</b><span class="tag ${r.tag}">${esc(tagLabel(projects.find((p) => p.id === r.project), r.tag))}</span><span class="st ${r.status}">${RSTATUS[r.status]}</span><span class="spacer"></span>
      ${r.status !== 'resolved' ? `<button class="btn sm" data-resolve="${r.id}">${icon('check')} Resolve</button>` : `<button class="btn sm" data-reopen="${r.id}">Reopen</button>`}</div>
    <div class="thread-scroll">${body}</div>
    <form class="reply" data-compose="reply">${editor('reply')}<button class="btn primary sm send">Send</button></form>
  </section>`;
}

// ---------- agent TUI ----------
// Tabs of a session's window: the agent, plus local shells opened with "+".
const terms = {};
const termTabs = (a) => (terms[a.id] ||= { list: [{ id: 'agent' }], active: 'agent', n: 0 });
const SHELL_OUT = { pwd: '/Users/shiqi', whoami: 'shiqi', ls: 'Desktop    Documents  Downloads  repos' };

function agentView(a) {
  const r = a.from && reqOf(a.from);
  const tui = {
    blocked: `<span class="o">⏺</span> Bash(ssh -o BatchMode=yes shiqis-macbook-pro.local echo ok)
  ⎿ ssh: handshake failed: EOF

<span class="o">⏺</span> The host key changed when the relay was rebuilt; the old entry needs clearing.

<div class="tui-box">Bash command

  ssh-keygen -R shiqis-macbook-pro.local
  Remove the stale host key

Do you want to proceed?
<span class="o">❯ 1. Yes</span>
  2. Yes, and don't ask again for ssh-keygen commands
  3. No, and tell Claude what to do differently</div>`,
    working: `<span class="o">⏺</span> Read threads/schema.sql
<span class="o">⏺</span> Update(threads/schema.sql)
  ⎿ Updated with 4 additions
<span class="o">⏺</span> Bash(npm test)
  ⎿ <span class="g">✓ 38 passed</span>

<span class="o">✻</span> Wiring vote counts into the request list… <span class="d">(esc to interrupt)</span>`,
    idle: `<span class="o">⏺</span> Done. Replied to ${a.from ? refOf(reqOf(a.from)) : 'the request'} with <span class="b">fen reply</span>.

<span class="d">✻ Baked for 2m 14s · done</span>`,
  }[a.state];
  const fresh = `<span class="o">✻</span> Welcome to ${AGENT[a.kind][1]}${a.project ? ` · ${esc(projects.find((p) => p.id === a.project)?.name || '')}` : ''}

<span class="d">  cwd: ~/.fen/${esc(a.id)}${a.model ? `   model: ${esc(a.model)}` : ''}</span>`;
  const t = termTabs(a), shell = t.list.find((x) => x.id === t.active && x.shell);
  const tabs = t.list.map((x) => x.shell
    ? `<span class="tab ${x.id === t.active ? 'on' : ''}" data-term-tab="${x.id}">${icon('term')}${esc(x.title)}<button type="button" class="tab-x" data-term-close="${x.id}" title="Close tab">${icon('close')}</button></span>`
    : `<span class="tab ${x.id === t.active ? 'on' : ''}" data-term-tab="agent"><img src="${AGENT[a.kind][0]}" alt="">${AGENT[a.kind][1]}</span>`).join('');
  const body = shell
    ? `<pre class="sh">${shell.lines.map((l) => l.cmd !== undefined ? `<span class="d">shiqi@MacBook-Pro ~ %</span> ${esc(l.cmd)}` : esc(l.out)).join('\n')}${shell.lines.length ? '\n' : ''}<span class="d">shiqi@MacBook-Pro ~ %</span> <input class="sh-input" data-shell="${shell.id}" spellcheck="false" autocomplete="off"></pre>`
    : `<pre>${a.fresh ? fresh : tui}

<div class="tui-input">❯ <span class="cursor"></span></div><span class="d">  ⏵⏵ bypass permissions on (shift+tab to cycle)</span></pre>`;
  return `<section class="tui-pane">
    <div class="tabs">${tabs}<button type="button" class="ib sm" data-new-term title="New tab (local shell)">${icon('plus')}</button><span class="spacer"></span>
      ${r ? `<button class="btn sm" data-goto-req="${r.id}"><span class="ref">${refOf(r)}</span>${esc(r.title)}</button>` : ''}</div>
    <div class="tui">${body}</div>
  </section>`;
}

function projectModal() {
  return `<div class="scrim" data-scrim><form class="modal" data-create-project>
    <div class="m-head"><b>New project</b><button type="button" class="ib" data-scrim>${icon('close')}</button></div>
    <input name="name" placeholder="Name" required>
    <textarea name="desc" rows="3" placeholder="Description"></textarea>
    <div class="m-foot"><button type="button" class="btn" data-scrim>Cancel</button><button class="btn primary">Create</button></div>
  </form></div>`;
}

// ---------- new session ----------
function sessionModal() {
  const s = newSession, p = projects.find((x) => x.id === s.project);
  // With a project, only its enabled workers are offered (Settings > Agents).
  const allowed = p ? [...new Set(p.workers.filter((w) => w.on).map((w) => w.agent))] : Object.keys(AGENT);
  if (!allowed.includes(s.agent)) s.agent = allowed[0];
  const models = p ? p.workers.filter((w) => w.on && w.agent === s.agent).map((w) => w.model) : MODELS[s.agent];
  if (!models.includes(s.model)) s.model = models[0];
  return `<div class="scrim" data-scrim><form class="modal" data-create-session>
    <div class="m-head"><b>New session</b><button type="button" class="ib" data-scrim>${icon('close')}</button></div>
    <div class="fld"><span>Project</span>${choice('sessionProject', s.project, [['', 'No project'], ...projects.filter((x) => x.org === org).map((x) => [x.id, x.name])])}</div>
    <div class="fld"><span>Agent</span><div class="agent-picks">${allowed.map((k) => `
      <button type="button" class="agent-pick ${k === s.agent ? 'on' : ''}" data-session-agent="${k}"><img src="${AGENT[k][0]}" alt="">${AGENT[k][1]}</button>`).join('')}</div></div>
    <div class="fld"><span>Model</span>${choice('sessionModel', s.model, models.map((m) => [m, m]))}</div>
    <textarea name="prompt" rows="3" placeholder="First message (optional)"></textarea>
    <div class="m-foot"><button type="button" class="btn" data-scrim>Cancel</button><button class="btn primary">Start</button></div>
  </form></div>`;
}

// ---------- project settings ----------
const SETTINGS_TABS = [['general', 'General', 'sliders'], ['knowledge', 'Knowledge', 'book'], ['queue', 'Queue', 'layers'], ['agents', 'Agents', 'bot']];
function settingsView(p) {
  const tab = view.tab || 'general';
  const body = { general: settingsGeneral, knowledge: settingsKnowledge, queue: settingsQueue, agents: settingsAgents }[tab](p);
  return `<section class="settings">
    <div class="pane-head"><button class="ib" data-project="${p.id}" title="Back to requests">${icon('back')}</button>
      ${mark(p, 'lg')}
      <div class="ph-text"><b>${esc(p.name)}</b><span>Project settings</span></div></div>
    <div class="set-wrap"><nav class="set-nav">${SETTINGS_TABS.map(([k, label, ic]) => `
      <button class="side-row ${tab === k ? 'sel' : ''}" data-settings="${p.id}" data-tab="${k}">${icon(ic)}<span class="side-name">${label}</span>
      ${k === 'knowledge' && p.knowledge.length ? `<span class="side-meta">${p.knowledge.length}</span>` : ''}</button>`).join('')}</nav>
    <div class="set-body ${tab}">${body}</div></div>
  </section>`;
}

function settingsGeneral(p) {
  return `<h2>General</h2>
    <div class="set-card">
      <div class="row"><span>Icon</span><div class="icon-edit">${mark(p, 'xl')}
        <div class="swatches">${COLORS.map((c) => `<button type="button" class="swatch ${!p.icon && p.color === c ? 'on' : ''}" style="--c:${c}" data-p-color="${c}" aria-label="Color ${c}"></button>`).join('')}</div>
        <span class="spacer"></span>
        <label class="btn sm">Upload image…<input type="file" accept="image/*" data-p-icon hidden></label>
        ${p.icon ? '<button type="button" class="link-btn" data-p-icon-clear>Remove</button>' : ''}</div></div>
      <label class="row"><span>Name</span><input data-p-field="name" value="${esc(p.name)}"></label>
      <label class="row"><span>Key</span><input data-p-field="key" value="${esc(p.key)}" maxlength="6" spellcheck="false" class="key-input"><em class="key-hint">Requests are numbered ${esc(p.key)}-1, ${esc(p.key)}-2, …</em></label>
      <label class="row"><span>Description</span><input data-p-field="desc" value="${esc(p.desc)}"></label>
      <label class="row"><span>Repository</span><input data-p-field="repo" value="${esc(p.repo || '')}" placeholder="owner/repo"></label>
    </div>
    <h3>Tags</h3><p class="hint">Every request gets one tag. The request list can be filtered by tag.</p>
    <div class="set-card"><div class="tag-edit">${p.tags.map(([k, v]) => `
      <span class="tag ${k}">${esc(v)}<button type="button" class="tag-x" data-tag-remove="${k}" title="Remove ${esc(v)}" ${p.tags.length < 2 ? 'disabled' : ''}>${icon('close')}</button></span>`).join('')}
      <input class="tag-new" data-tag-add placeholder="Add tag, press Enter"></div>
      <div class="row-foot"><button type="button" class="link-btn" data-tags-reset>Reset to Bug, Feature, Discussion</button></div></div>`;
}

// Knowledge is stored as an OKF bundle (folders of markdown files); shown
// as content: sections by name, people and agents by name, dates as dates.
const SECTIONS = { architecture: 'Architecture', runbooks: 'Runbooks', decisions: 'Decisions', glossary: 'Glossary' };
const sectionName = (folder) => SECTIONS[folder] || folder.replace(/[-_]+/g, ' ').replace(/^./, (c) => c.toUpperCase());
const actorName = (actor) => actor.startsWith('human:') ? actor.slice(6)
  : ({ 'claude-code': 'Claude Code', codex: 'Codex', kimi: 'Kimi Code' }[actor.split('/')[0]] || actor.split('/')[0]);
const when = (iso) => new Date(iso).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'UTC' });
const STATUS = { draft: 'Draft', stable: 'Current', deprecated: 'Deprecated' };

function settingsKnowledge(p) {
  const all = p.knowledge;
  if (!all.length) return `<h2>Knowledge</h2><div class="empty-k">${icon('book', 'big')}<b>No knowledge yet</b>
    <p>As agents work on this project's requests, what they learn (how things fit together, how to fix recurring problems, decisions) appears here.</p></div>`;
  const list = all.filter((c) => knowledgeFilter === 'all' || (knowledgeFilter === 'review' ? trustOf(c) !== 'human' : c.status === knowledgeFilter));
  // The open entry is always one the filter shows.
  const c = list.find((x) => x.id === knowledgeId) || list[0];
  const folders = [...new Set(list.map((x) => x.id.split('/')[0]))];
  const row = (x) => `<button class="k-row ${x === c ? 'sel' : ''}" data-concept="${x.id}">
      <span class="k-title">${esc(x.title)}</span><span class="k-sub"><span class="trust ${trustOf(x)}" title="${TRUST[trustOf(x)]}"></span><span class="k-type">${esc(x.type)}</span>${x.status !== 'stable' ? `<span class="k-status ${x.status}">${STATUS[x.status]}</span>` : ''}</span></button>`;
  const reviewed = c?.verified?.length ? c.verified[c.verified.length - 1] : null;
  const fields = c && [
    ['Status', STATUS[c.status]],
    ['Written by', `${esc(actorName(c.generated.by))} · ${when(c.generated.at)}`],
    ['Reviewed by', reviewed ? `${esc(actorName(reviewed.by))} · ${when(reviewed.at)}` : '<span class="muted">Not reviewed yet</span>'],
    ['Tags', c.tags.map((t) => `<span class="k-tag">${esc(t)}</span>`).join('')],
    ['Based on', c.sources.map((x) => `<span class="k-src">${esc(x)}</span>`).join('')],
  ];
  return `<div class="k-head"><h2>Knowledge</h2><span class="hint">What agents learned working on ${esc(p.name)}. Kept on your cloud computer as an <a href="https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md" target="_blank">OKF</a> bundle.</span></div>
    <div class="filters">${[['all', 'All'], ['review', 'Needs review'], ['draft', 'Draft'], ['deprecated', 'Deprecated']].map(([k, v]) => `<button class="chip ${knowledgeFilter === k ? 'on' : ''}" data-kfilter="${k}">${v}</button>`).join('')}</div>
    ${c ? `<div class="k-split"><div class="k-list">${folders.map((f) => `<div class="group">${esc(sectionName(f))}</div>` + list.filter((x) => x.id.startsWith(f + '/')).map(row).join('')).join('')}</div>
    <article class="k-doc"><div class="k-doc-h"><div><b>${esc(c.title)}</b><span class="hint">${esc(c.description)}</span></div>
      <span class="trust-label ${trustOf(c)}">${TRUST[trustOf(c)]}</span></div>
      <dl class="k-fields">${fields.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join('')}</dl>
      <div class="md">${c.body}</div>
      <div class="k-actions"><span class="spacer"></span>
        ${trustOf(c) !== 'human' ? `<button class="btn sm" data-k-review="${c.id}">${icon('check')} Mark reviewed</button>` : ''}
        ${c.status !== 'deprecated' ? `<button class="btn sm" data-k-deprecate="${c.id}">Deprecate</button>` : `<button class="btn sm" data-k-restore="${c.id}">Restore</button>`}</div>
    </article></div>` : `<p class="hint k-none">Nothing here.</p>`}`;
}

function settingsQueue(p) {
  return `<h2>Queue</h2>
    <div class="set-card"><div class="row wide"><span>Requests worked on at once<small>Each runs in its own session. Others wait as Queued.</small></span>
      <div class="stepper"><button type="button" data-queue="-1" ${p.queue <= 1 ? 'disabled' : ''} aria-label="Fewer">−</button><b>${p.queue}</b><button type="button" data-queue="1" ${p.queue >= 10 ? 'disabled' : ''} aria-label="More">+</button></div></div></div>
    ${queueNow(p)}`;
}

// What the queue is doing right now: the requests holding a slot, then the ones waiting, in order.
function queueNow(p) {
  const mine = requests.filter((r) => r.project === p.id);
  const working = mine.filter((r) => r.status === 'working'), queued = mine.filter((r) => r.status === 'queued');
  const row = (r, i) => `<button type="button" class="q-row" data-goto-req="${r.id}">
    ${r.status === 'working' && r.agent ? `<img class="mini" src="${AGENT[r.agent][0]}" alt="">` : `<span class="q-pos">${i + 1}</span>`}
    <span class="ref">${refOf(r)}</span><span class="q-title">${esc(r.title)}</span><span class="tag ${r.tag}">${esc(tagLabel(p, r.tag))}</span><span class="st ${r.status}">${RSTATUS[r.status]}</span></button>`;
  return `<h3>Now</h3><p class="hint">${working.length} of ${p.queue} in progress${queued.length ? ` · ${queued.length} waiting` : ''}</p>
    <div class="set-card q-list">${working.map(row).join('')}${queued.map(row).join('')}
      ${working.length + queued.length ? '' : '<div class="q-empty">Nothing in progress or waiting.</div>'}</div>`;
}

function settingsAgents(p) {
  const on = p.workers.filter((w) => w.on).length;
  return `<h2>Agents</h2><p class="hint">Which agents and models may pick up this project's requests. A new request goes to the first enabled worker that is free.</p>
    <div class="set-card workers">${p.workers.map((w, i) => `
      <div class="worker ${w.on ? '' : 'off'}"><img src="${AGENT[w.agent][0]}" alt=""><b>${AGENT[w.agent][1]}</b>
        ${choice('workerModel:' + i, w.model, MODELS[w.agent].map((m) => [m, m]))}
        <span class="spacer"></span>
        <button type="button" class="switch" role="switch" aria-checked="${w.on}" aria-label="${w.on ? 'Disable' : 'Enable'} ${AGENT[w.agent][1]}" data-worker-on="${i}" ${w.on && on < 2 ? 'disabled' : ''}></button>
        <button type="button" class="ib" data-worker-remove="${i}" title="Remove" ${p.workers.length < 2 ? 'disabled' : ''}>${icon('trash')}</button></div>`).join('')}
      <div class="row-foot">${choice('workerAdd', null, Object.entries(AGENT).map(([k, [img, n]]) => [k, n, img]), { placeholder: 'Add worker…' })}</div></div>`;
}

function render() {
  const main = view.type === 'agent'
    ? agentView(agents.find((a) => a.id === view.id))
    : view.type === 'settings'
    ? settingsView(projects.find((p) => p.id === view.id))
    : view.type === 'empty'
    ? `<section class="forum empty-org"><p>No projects in this organization yet.</p><button class="btn sm" data-new-project>${icon('plus')} New project</button></section>`
    : projectView(projects.find((p) => p.id === view.id)) + threadView(reqOf(openId)?.project === view.id ? reqOf(openId) : null);
  $('#app').innerHTML = sidebar() + `<main class="main">${main}</main>` + (newProject ? projectModal() : '') + (newSession ? sessionModal() : '') + (menu ? choiceMenu() : '');
  const t = $('.thread-scroll'); if (t) t.scrollTop = t.scrollHeight;
}

// ---------- simulation ----------
function progress(r) {
  const p = projects.find((x) => x.id === r.project);
  // At the project's limit, the request waits in Queued until a slot frees.
  if (p && requests.filter((x) => x.project === p.id && x.status === 'working').length >= p.queue) { setTimeout(() => progress(r), 1500); return; }
  r.agent ||= p?.workers.find((w) => w.on)?.agent;
  setTimeout(() => {
    const id = 'a' + (++seq);
    agents.unshift({ id, kind: r.agent || 'claude', title: r.title.slice(0, 18), state: 'working', age: '0m', from: r.id });
    r.agent ||= 'claude'; r.status = 'working'; r.msgs.push({ sys: AGENT[r.agent][1], agent: id }); render();
    setTimeout(() => {
      agents.find((a) => a.id === id).state = 'idle';
      r.msgs.push({ who: 'agent', agent: r.agent, at: 'now', html: '<p>Done:</p>', att: [['link', `preview-${r.id}.fen.run`]] });
      r.status = 'you'; render();
    }, 2600);
  }, 900);
}

const toolMap = { bold: 'bold', italic: 'italic', strike: 'strikeThrough', ul: 'insertUnorderedList', ol: 'insertOrderedList' };
document.addEventListener('mousedown', (e) => { if (e.target.closest('.tb')) e.preventDefault(); });
document.addEventListener('click', (e) => {
  const b = e.target.closest('button,[data-scrim]');
  if (menu && b?.dataset.choose !== undefined) { const key = menu.key; menu = null; choose(key, b.dataset.choose); render(); return; }
  if (b?.dataset.choice) {
    const key = b.dataset.choice, r = b.getBoundingClientRect();
    menu = menu?.key === key ? null : { key, value: b.dataset.value, options: JSON.parse(b.dataset.options), x: r.left, y: r.bottom + 4, w: r.width };
    render(); return;
  }
  if (menu) { menu = null; render(); if (!b || b.closest('[data-scrim]') === b) return; }
  if (!b) return;
  const d = b.dataset;
  if (d.cmd) {
    const c = d.cmd;
    if (toolMap[c]) document.execCommand(toolMap[c]);
    else if (c === 'quote') document.execCommand('formatBlock', false, 'blockquote');
    else if (c === 'code') { const s = getSelection().toString() || 'code'; document.execCommand('insertHTML', false, `<code>${esc(s)}</code>&nbsp;`); }
    else if (c === 'link') { const u = prompt('Link URL'); if (u) document.execCommand('createLink', false, u); }
    else if (c === 'image') document.execCommand('insertHTML', false, '<div class="shot"><span></span><span></span><span></span><span></span></div><p><br></p>');
    return;
  }
  if ('scrim' in d && (e.target === b || b.tagName === 'BUTTON')) { newProject = false; newSession = null; render(); return; }
  if ('newSession' in d) { newSession = { agent: 'claude', project: view.type === 'agent' ? '' : view.id }; render(); return; }
  if (d.termClose) { const t = termTabs(agents.find((x) => x.id === view.id)); const i = t.list.findIndex((x) => x.id === d.termClose); t.list.splice(i, 1); if (t.active === d.termClose) t.active = t.list[Math.max(0, i - 1)].id; render(); return; }
  if ('newTerm' in d) { const t = termTabs(agents.find((x) => x.id === view.id)); const id = 's' + (++t.n); t.list.push({ id, shell: true, title: 'zsh', lines: [] }); t.active = id; render(); $('.sh-input')?.focus(); return; }
  if (d.sessionAgent) { newSession.agent = d.sessionAgent; newSession.model = null; render(); return; }
  if (d.workerOn) { const p = projects.find((x) => x.id === view.id); p.workers[+d.workerOn].on = !p.workers[+d.workerOn].on; render(); return; }
  if (d.settings) { view = { type: 'settings', id: d.settings, tab: d.tab || (view.type === 'settings' ? view.tab : 'general') }; render(); return; }
  const sp = view.type === 'settings' && projects.find((x) => x.id === view.id);
  if (d.pColor) { sp.color = d.pColor; sp.icon = null; render(); return; }
  if ('pIconClear' in d) { sp.icon = null; render(); return; }
  if (d.tagRemove) { sp.tags = sp.tags.filter(([k]) => k !== d.tagRemove); render(); return; }
  if ('tagsReset' in d) { sp.tags = [...DEFAULT_TAGS]; render(); return; }
  if (d.queue) { sp.queue = Math.min(10, Math.max(1, sp.queue + +d.queue)); render(); return; }
  if (d.workerRemove) { sp.workers.splice(+d.workerRemove, 1); if (!sp.workers.some((w) => w.on)) sp.workers[0].on = true; render(); return; }
  if (d.concept) { knowledgeId = d.concept; render(); return; }
  if (d.kfilter) { knowledgeFilter = d.kfilter; render(); return; }
  if (d.kReview) { const c = sp.knowledge.find((x) => x.id === d.kReview); (c.verified ||= []).push({ by: 'human:shiqi', at: new Date().toISOString().slice(0, 16) + 'Z' }); if (c.status === 'draft') c.status = 'stable'; render(); return; }
  if (d.kDeprecate) { sp.knowledge.find((x) => x.id === d.kDeprecate).status = 'deprecated'; render(); return; }
  if (d.kRestore) { sp.knowledge.find((x) => x.id === d.kRestore).status = 'stable'; render(); return; }
  if ('newProject' in d) { newProject = true; render(); $('.modal input').focus(); return; }
  if (d.agent) { view = { type: 'agent', id: d.agent }; render(); return; }
  if (d.project) { view = { type: 'project', id: d.project }; composing = false; filter = 'all'; scope = 'open'; render(); return; }
  if (d.gotoReq) { const r = reqOf(+d.gotoReq); view = { type: 'project', id: r.project }; openId = r.id; render(); return; }
  if (d.req) { openId = +d.req; render(); return; }
  if (d.filter) { filter = d.filter; render(); return; }
  if (d.scope) { scope = d.scope; render(); return; }
  if ('composeOpen' in d) { composing = true; render(); $('.c-title').focus(); return; }
  if ('cancel' in d) { composing = false; render(); return; }
  if (d.pick) { b.parentElement.querySelectorAll('.pick').forEach((x) => x.classList.toggle('on', x === b)); return; }
  if (d.resolve) { const r = reqOf(+d.resolve); r.status = 'resolved'; r.msgs.push({ sys: 'Resolved' }); render(); return; }
  if (d.reopen) { const r = reqOf(+d.reopen); r.status = 'you'; r.msgs.push({ sys: 'Reopened' }); render(); return; }
  if (d.approve || d.deny) {
    const r = reqOf(+(d.approve || d.deny)), ok = !!d.approve;
    r.msgs = r.msgs.map((m) => m.approve ? { sys: ok ? 'Allowed' : 'Denied' } : m);
    r.status = 'working'; const a = agents.find((x) => x.from === r.id); if (a) a.state = 'working'; render();
    setTimeout(() => { r.msgs.push({ who: 'agent', agent: r.agent, at: 'now', html: '<p>Fixed: 20 attaches in a row, none hung.</p>' }); r.status = 'you'; if (a) a.state = 'idle'; render(); }, 2200);
  }
});
document.addEventListener('submit', (e) => {
  e.preventDefault();
  const f = e.target;
  if ('createSession' in f.dataset) {
    const fd = new FormData(f), prompt = fd.get('prompt').trim(), id = 'a' + (++seq);
    agents.unshift({ id, kind: newSession.agent, model: newSession.model, project: newSession.project || undefined, fresh: !prompt,
      title: (prompt.split('\n')[0] || 'New session').slice(0, 24), state: prompt ? 'working' : 'idle', age: '0m' });
    newSession = null; view = { type: 'agent', id }; render(); return;
  }
  if ('createProject' in f.dataset) {
    const fd = new FormData(f), name = fd.get('name').trim();
    const id = name.toLowerCase().replace(/[^a-z0-9]+/g, '-') || 'p' + Date.now();
    projects.push({ id, org, key: keyFrom(name), next: 1, name, desc: fd.get('desc').trim(), color: ['#d97b6f', '#c9a23d', '#4fa3b5'][projects.length % 3],
      tags: [...DEFAULT_TAGS], queue: 3, workers: workers(['claude', 'Opus 5.5']), knowledge: [] });
    newProject = false; view = { type: 'project', id }; render(); return;
  }
  const ed = f.querySelector('.rich'), html = ed.innerHTML.trim();
  if (f.dataset.compose === 'new') {
    const title = (ed.innerText.trim().split('\n')[0] || '').slice(0, 60); if (!title) return;
    const r = { id: ++seq, num: projects.find((x) => x.id === view.id).next++, project: view.id, tag: projects.find((x) => x.id === view.id).tags[0][0], status: 'queued', title, author: 'shiqi', ago: 'now', msgs: [{ who: 'shiqi', at: 'now', html: html || '<p></p>' }] };
    requests.unshift(r); openId = r.id; render(); progress(r);
  } else {
    if (!ed.textContent.trim() && !ed.querySelector('.shot')) return;
    const r = reqOf(openId); r.msgs.push({ who: 'shiqi', at: 'now', html }); r.status = 'queued'; render(); progress(r);
  }
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.isComposing && !e.shiftKey && e.target.matches('[data-editor=new]')) { e.preventDefault(); e.target.closest('form').requestSubmit(); return; }
  if (e.key === 'Enter' && e.metaKey && e.target.closest('.rich')) { e.preventDefault(); e.target.closest('form').requestSubmit(); return; }
  // Markdown shortcuts at the start of a line: "# ", "## ", "- ", "1. ", "> "
  if (e.key !== ' ' || !e.target.closest?.('.rich')) return;
  const sel = getSelection(), node = sel.anchorNode;
  if (!node || node.nodeType !== 3) return;
  const before = node.textContent.slice(0, sel.anchorOffset);
  const rule = { '#': ['formatBlock', 'h3'], '##': ['formatBlock', 'h4'], '-': ['insertUnorderedList'], '*': ['insertUnorderedList'], '1.': ['insertOrderedList'], '>': ['formatBlock', 'blockquote'] }[before];
  if (!rule) return;
  e.preventDefault();
  node.textContent = node.textContent.slice(sel.anchorOffset);
  document.execCommand(rule[0], false, rule[1]);
});

if (location.hash.startsWith("#agent-")) view = { type: "agent", id: location.hash.slice(7) };
render();
if (location.hash === "#new") { const e = document.querySelector('[data-editor=new]'); e.innerHTML = '<p>Settings buttons are hard to see in dark mode</p><p>Mostly the secondary buttons on the Governance page</p>'; e.closest('form').classList.add('has'); }
document.addEventListener('input', (e) => {
  const ed = e.target.closest?.('.rich'); if (!ed) return;
  const has = !!ed.innerText.trim() || !!ed.querySelector('img,.shot');
  if (!has) ed.innerHTML = '';
  ed.closest('form').classList.toggle('has', has);
});

// Settings fields, worker model/switch, and the new session's project.
document.addEventListener('change', (e) => {
  const t = e.target, d = t.dataset;
  const p = view.type === 'settings' && projects.find((x) => x.id === view.id); if (!p) return;
  if ('pIcon' in d && t.files[0]) { const fr = new FileReader(); fr.onload = () => { p.icon = fr.result; render(); }; fr.readAsDataURL(t.files[0]); return; }
  if (d.pField === 'key') { const v = t.value.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 6); if (v) p.key = v; render(); return; }
  if (d.pField) { const v = t.value.trim(); if (d.pField !== 'name' || v) p[d.pField] = v; render(); return; }
});
document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && menu) { menu = null; render(); return; }
  if (e.key !== 'Enter' || !('tagAdd' in (e.target.dataset || {}))) return;
  e.preventDefault();
  const p = projects.find((x) => x.id === view.id), label = e.target.value.trim();
  const id = label.toLowerCase().replace(/[^a-z0-9]+/g, '-') || 't' + Date.now();
  if (label && !p.tags.some(([k]) => k === id)) p.tags.push([id, label]);
  render(); $('[data-tag-add]')?.focus();
});
if (location.hash.startsWith('#settings-')) { const [, id, tab] = location.hash.split('-'); view = { type: 'settings', id, tab }; render(); }
if (location.hash === '#new-session') { newSession = { agent: 'claude', project: 'fennec' }; render(); }

document.addEventListener('click', (e) => {
  const tab = e.target.closest('[data-term-tab]');
  if (!tab || e.target.closest('[data-term-close]')) { if (e.target.closest('.tui .sh')) $('.sh-input')?.focus(); return; }
  termTabs(agents.find((x) => x.id === view.id)).active = tab.dataset.termTab; render(); $('.sh-input')?.focus();
});
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Enter' || !e.target.dataset?.shell) return;
  const t = termTabs(agents.find((x) => x.id === view.id)), sh = t.list.find((x) => x.id === e.target.dataset.shell), cmd = e.target.value.trim();
  if (cmd === 'clear') sh.lines = [];
  else { sh.lines.push({ cmd }); if (cmd) sh.lines.push({ out: SHELL_OUT[cmd] ?? `zsh: command not found: ${cmd.split(' ')[0]}` }); }
  render(); $('.sh-input')?.focus(); $('.tui').scrollTop = 1e9;
});

// Sidebar search: filters projects, sessions and the request list as you type.
document.addEventListener('input', (e) => {
  if (!('search' in e.target.dataset)) return;
  query = e.target.value; const at = e.target.selectionStart; render();
  const f = $('[data-search]'); f.focus(); f.setSelectionRange(at, at);
});
document.addEventListener('keydown', (e) => {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); $('[data-search]').focus(); $('[data-search]').select(); }
  if (e.key === 'Escape' && 'search' in (e.target.dataset || {})) { query = ''; render(); }
});
