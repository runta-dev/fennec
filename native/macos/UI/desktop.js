// Fennec desktop window: projects with request boards, and the cloud
// computer's sessions with terminal tabs. Data comes from the daemon through
// the native bridge (DesktopWindow.swift); see docs/desktop-app.md.
(() => {
'use strict';

// ---------- copy ----------
const MESSAGES = window.FennecMessages || {};
let language = 'en-US';
// UI copy is addressed by message ID (translations.json, "desktop.*").
const T = (key, vars) => {
  const entry = MESSAGES['desktop.' + key];
  let text = entry ? (entry[language] ?? (language === 'en-GB' ? entry['en-US'] : language === 'zh-TW' ? entry['zh-CN'] : undefined) ?? entry['en-US']) : key;
  if (vars) text = text.replace(/\{(\w+)\}/g, (_, k) => vars[k] ?? '');
  return text;
};

// The panel's own messages (ui.*), for settings shared with it.
const TU = (key) => { const e = MESSAGES['ui.' + key]; return e ? (e[language] ?? (language === 'en-GB' ? e['en-US'] : language === 'zh-TW' ? e['zh-CN'] : undefined) ?? e['en-US']) : key; };

// ---------- bridge ----------
let seq = 0;
const pending = new Map();
const post = (message) => window.webkit?.messageHandlers?.desktop?.postMessage(JSON.stringify(message));
const native = (action, data = {}) => new Promise((resolve, reject) => {
  const id = ++seq;
  pending.set(id, { resolve, reject });
  post({ id, action, ...data });
});
window.desktopReply = (id, ok, result) => {
  const call = pending.get(id);
  if (!call) return;
  pending.delete(id);
  if (ok) call.resolve(result); else call.reject(result || {});
};
const rpc = (method, params = {}) => native('rpc', { method, params });
const board = (method, path, body) => rpc('board.call', body === undefined ? { method, path } : { method, path, body });

// ---------- icons ----------
const I = {
  sidebar: '<rect x="2" y="3" width="12" height="10" rx="2"/><path d="M6 3v10"/>',
  search: '<path d="m11 11 3 3"/><circle cx="7" cy="7" r="4.5"/>',
  plus: '<path d="M8 3v10M3 8h10"/>',
  gear: '<g transform="scale(.6667)" stroke-width="2.25"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></g>',
  chat: '<path d="M3 4.5A1.5 1.5 0 0 1 4.5 3h7A1.5 1.5 0 0 1 13 4.5v5a1.5 1.5 0 0 1-1.5 1.5H7l-3 2.2V11h0a1 1 0 0 1-1-1Z"/>',
  close: '<path d="m4 4 8 8M12 4l-8 8"/>',
  term: '<rect x="2" y="3" width="12" height="10" rx="2"/><path d="m5 7 2 1.5L5 10M8.5 10H11"/>',
  check: '<path d="m3.5 8.5 3 3 6-7"/>',
  back: '<path d="M10 3.5 5.5 8l4.5 4.5"/>',
  book: '<path d="M3 3.5h4a1.5 1.5 0 0 1 1.5 1.5v8A1.5 1.5 0 0 0 7 11.5H3Zm10 0H9.5A1.5 1.5 0 0 0 8 5v8a1.5 1.5 0 0 1 1.5-1.5H13Z"/>',
  layers: '<path d="m8 2.5 5.5 3L8 8.5l-5.5-3Z"/><path d="m2.5 8.5 5.5 3 5.5-3M2.5 11l5.5 3 5.5-3"/>',
  bot: '<rect x="3" y="5" width="10" height="8" rx="2"/><path d="M8 2.5V5M6 9h.01M10 9h.01"/>',
  sliders: '<path d="M3 5h6M12 5h1M3 11h1M7 11h6"/><circle cx="10.5" cy="5" r="1.5"/><circle cx="5.5" cy="11" r="1.5"/>',
  trash: '<path d="M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5"/>',
  link: '<path d="M7 9a2.5 2.5 0 0 0 3.5 0l2-2A2.5 2.5 0 0 0 9 3.5l-.7.7M9 7a2.5 2.5 0 0 0-3.5 0l-2 2A2.5 2.5 0 0 0 7 12.5l.7-.7"/>',
};
const icon = (n, cls = '') => `<svg class="i ${cls}" viewBox="0 0 16 16" aria-hidden="true">${I[n]}</svg>`;
const chevron = '<svg class="i" viewBox="0 0 16 16" aria-hidden="true"><path d="m4.5 6.5 3.5 3.5 3.5-3.5"/></svg>';
const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const $ = (s) => document.querySelector(s);

// Agent icons are Fennec for iPhone's.
const AGENTS = {
  claude: ['icons/agents/claude.png', 'Claude Code'], codex: ['icons/agents/codex.png', 'Codex'], kimi: ['icons/agents/kimi.png', 'Kimi Code'],
  opencode: ['icons/agents/opencode.png', 'OpenCode'], pi: ['icons/agents/pi.png', 'Pi'], cursor: ['icons/agents/cursor.png', 'Cursor'], grok: ['icons/agents/grok.png', 'Grok'],
};
const dark = () => matchMedia('(prefers-color-scheme: dark)').matches;
const agentIcon = (a) => (a === 'kimi' && dark() ? 'icons/agents/kimi-dark.png' : AGENTS[a]?.[0] || 'icons/fennec.svg');
const agentName = (a) => AGENTS[a]?.[1] || a;

// ---------- state ----------
let me = { signedIn: false, name: '' }, dashboard = 'https://dashboard.runta.com';
let prefs = {}, version = '', cloudAgents = [], loginItem = null, starting = '';
let sessions = [];
let orgs = [], org = localStorage.getItem('desktop.org') || '';
let projects = null, boardProblem = '';
const requests = {}, threads = {}, knowledge = {};
let view = JSON.parse(localStorage.getItem('desktop.view') || 'null') || { type: 'project', id: '' };
let openRequest = null, filter = 'all', scope = 'open', query = '';
let menu = null, newProject = false, settingsTab = 'general';
let knowledgeId = '', knowledgeFilter = 'all', notice = '', busy = false;
const COLORS = ['#f38d25', '#d97b6f', '#c9a23d', '#5fb08a', '#4fa3b5', '#6f8fd9', '#9a7bd1', '#8a8f98'];
const STATUS = { you: 'needs_you', working: 'working', queued: 'queued', resolved: 'resolved' };

const projectOf = (id) => projects?.find((p) => p.id === id);
const visibleProjects = () => (projects || []).filter((p) => !org || !p.org || p.org === org);
const refOf = (p, r) => `${p.key}-${r.number}`;
const tagLabel = (p, id) => p?.tags.find((t) => t.id === id)?.label || id;
const matches = (...texts) => !query || texts.some((t) => t && String(t).toLowerCase().includes(query.toLowerCase()));
const ago = (time) => {
  const ms = Date.now() - (typeof time === 'number' ? time : Date.parse(time));
  if (!isFinite(ms)) return '';
  const m = Math.max(0, Math.round(ms / 60000));
  if (m < 1) return T('now');
  if (m < 60) return T('minutes', { n: m });
  if (m < 1440) return T('hours', { n: Math.round(m / 60) });
  return T('days', { n: Math.round(m / 1440) });
};
const clock = (time) => new Date(time).toLocaleTimeString(language, { hour: '2-digit', minute: '2-digit' });
const save = () => { localStorage.setItem('desktop.view', JSON.stringify(view)); localStorage.setItem('desktop.org', org); };
const authorName = (m) => (m.agent ? agentName(m.agent) : m.author);
const avatar = (m) => m.agent
  ? `<span class="av agent"><img src="${agentIcon(m.agent)}" alt=""></span>`
  : `<span class="av" style="background:${COLORS[[...String(m.author)].reduce((h, c) => h + c.charCodeAt(0), 0) % COLORS.length]}">${esc(String(m.author).slice(0, 1).toUpperCase())}</span>`;
const mark = (p, cls = '') => `<span class="proj-mark ${cls}" style="--c:${esc(p.color)}">${p.icon ? `<img src="${esc(p.icon)}" alt="">` : esc([...p.name][0] || '?')}</span>`;

// Messages are markdown from people and agents: escaped, then a small,
// safe subset (code, bold, italic, links to http(s), lists, paragraphs).
function markdown(text) {
  const blocks = String(text).replace(/\r\n/g, '\n').split(/\n```[^\n]*\n([\s\S]*?)\n```(?:\n|$)|^```[^\n]*\n([\s\S]*?)\n```(?:\n|$)/);
  let html = '';
  blocks.forEach((part, i) => {
    if (part === undefined) return;
    if (i % 3 !== 0) { html += `<pre><code>${esc(part)}</code></pre>`; return; }
    for (const para of part.split(/\n{2,}/)) {
      if (!para.trim()) continue;
      const lines = para.split('\n');
      if (lines.every((l) => /^\s*(?:[-*]|\d+\.)\s+/.test(l))) {
        const ordered = /^\s*\d+\./.test(lines[0]);
        html += `<${ordered ? 'ol' : 'ul'}>${lines.map((l) => `<li>${inline(l.replace(/^\s*(?:[-*]|\d+\.)\s+/, ''))}</li>`).join('')}</${ordered ? 'ol' : 'ul'}>`;
      } else html += `<p>${lines.map(inline).join('<br>')}</p>`;
    }
  });
  return html;
}
function inline(text) {
  const codes = [];
  let s = esc(text).replace(/`([^`]+)`/g, (_, c) => `\u0000${codes.push(c) - 1}\u0000`);
  s = s.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, (_, label, url) => `<a href="${url}" data-url="${url}">${label}</a>`)
    .replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g, (_, pre, url) => `${pre}<a href="${url}" data-url="${url}">${url}</a>`)
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>').replace(/(^|\W)_([^_]+)_(?=\W|$)/g, '$1<em>$2</em>');
  return s.replace(/\u0000(\d+)\u0000/g, (_, i) => `<code>${codes[+i]}</code>`);
}

// Markdown as one line of text, for previews.
const plain = (text) => String(text).replace(/```[^\n]*\n?/g, ' ').replace(/`([^`]*)`/g, '$1').replace(/\*\*([^*]+)\*\*/g, '$1')
  .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1').replace(/^\s*(?:[-*]|\d+\.)\s+/gm, '').replace(/\s+/g, ' ').trim();

// ---------- dropdown (self-drawn, like the panel's choice menu) ----------
function choice(key, value, options, { placeholder = '', cls = '' } = {}) {
  const current = options.find(([v]) => v === value);
  return `<button type="button" class="choice-trigger ${cls} ${menu?.key === key ? 'open' : ''}" data-choice="${esc(key)}" data-value="${esc(value ?? '')}"
    data-options="${esc(JSON.stringify(options))}">${current?.[2] ? `<img src="${esc(current[2])}" alt="">` : ''}<span class="${current ? '' : 'ph'}">${esc(current ? current[1] : placeholder)}</span>${chevron}</button>`;
}
function choiceMenu() {
  return `<div class="choice-menu" style="left:${menu.x}px;top:${menu.y}px;min-width:${menu.w}px">${menu.options.map(([v, label, img, c]) => `
    ${c === 'add' ? '<div class="menu-sep"></div>' : ''}<button type="button" data-choose="${esc(v)}" class="${v === menu.value ? 'on' : ''} ${c === 'add' ? 'menu-action' : ''}">${c === 'add' ? icon('plus') : c ? `<span class="proj-mark" style="--c:${esc(c)}">${esc([...label][0])}</span>` : img ? `<img src="${esc(img)}" alt="">` : ''}<span class="menu-label">${esc(label)}</span>
    <span class="choice-check">${v === menu.value ? icon('check') : ''}</span></button>`).join('')}</div>`;
}

// ---------- sidebar ----------
// New session is a menu of agents, as on iPhone: pick one and it starts.
function newSessionButton() {
  const agents = (cloudAgents.length ? cloudAgents : Object.keys(AGENTS)).filter((a) => AGENTS[a]);
  return `<button class="ib sm ${menu?.key === 'newSession' ? 'open' : ''}" data-choice="newSession" data-value="" data-options="${esc(JSON.stringify(agents.map((a) => [a, agentName(a), agentIcon(a)])))}" title="${esc(T('new_session'))}">${icon('plus')}</button>`;
}
async function startSession(agent) {
  if (starting) return;
  starting = '…';
  try {
    const started = await rpc('sessions.start', { agent, project: '', model: '', message: '' });
    starting = started.session_id;
    view = { type: 'session', id: started.session_id }; save();
    post({ action: 'refresh-sessions' });
  } catch (error) { starting = ''; failed(error); }
  render();
}
function sessionRows() {
  const groups = [['blocked', T('needs_you')], ['working', T('working')], ['idle', T('idle')]];
  return groups.map(([state, label]) => {
    const list = sessions.filter((s) => s.group === state && matches(s.title, agentName(s.agent)));
    if (!list.length) return '';
    return `<div class="group">${label}<span>${list.length}</span></div>` + list.map((s) => `
      <button class="side-row ${view.type === 'session' && view.id === s.id ? 'sel' : ''}" data-session="${esc(s.id)}">
        <span class="agent-badge"><img class="agent-ico" src="${agentIcon(s.agent)}" alt=""><span class="state ${state}"></span></span>
        <span class="side-name">${esc(s.title || T('untitled_session'))}</span><span class="side-meta">${esc(s.since ? ago(s.since) : '')}</span></button>`).join('');
  }).join('');
}

function sidebar() {
  const current = orgs.find((o) => o.id === org) || orgs[0];
  const orgOptions = [...orgs.map((o, i) => [o.id, o.name || T('organization'), null, COLORS[i % COLORS.length]]), ['new', T('new_organization'), null, 'add']];
  const projectRows = projects === null ? '' : visibleProjects().filter((p) => matches(p.name, p.key, p.description)).map((p) => `
    <button class="side-row ${view.type !== 'session' && view.id === p.id ? 'sel' : ''}" data-project="${esc(p.id)}">${mark(p)}<span class="side-name">${esc(p.name)}</span>
      ${p.needs_you ? `<span class="count">${p.needs_you}</span>` : ''}</button>`).join('');
  return `<aside class="sidebar">
    <div class="titlebar" data-drag><span class="lights"></span>
      ${current ? `<button type="button" class="org-switch ${menu?.key === 'org' ? 'open' : ''}" data-choice="org" data-value="${esc(current.id)}" data-options="${esc(JSON.stringify(orgOptions))}"><span class="proj-mark" style="--c:${COLORS[Math.max(0, orgs.indexOf(current)) % COLORS.length]}">${esc([...(current.name || 'F')][0])}</span><b>${esc(current.name)}</b>${chevron}</button>` : '<b class="app-name">Fennec</b>'}
      <span class="spacer"></span></div>
    <label class="side-search">${icon('search')}<input data-search placeholder="${esc(T('search'))}" value="${esc(query)}" spellcheck="false" autocomplete="off"><kbd>⌘K</kbd></label>
    <div class="side-scroll">
      <div class="section">${T('projects')}<button class="ib sm" data-new-project title="${esc(T('new_project'))}">${icon('plus')}</button></div>${projectRows}
      <div class="section">${T('sessions')}${newSessionButton()}</div>${sessionRows()}
    </div>
    <div class="side-foot"><button class="side-row ${view.type === 'app' ? 'sel' : ''}" data-app-settings>${icon('gear')}<span class="side-name">${T('settings')}</span></button></div>
  </aside>`;
}

// ---------- project ----------
function projectView(p) {
  const list = (requests[p.id] || []).filter((r) => (filter === 'all' || r.tag === filter) && matches(r.title, refOf(p, r), r.latest?.body));
  const open = list.filter((r) => r.status !== 'resolved'), done = list.filter((r) => r.status === 'resolved');
  const shown = scope === 'open' ? open : done;
  const card = (r) => `<button class="req ${openRequest === r.number ? 'sel' : ''}" data-request="${r.number}">
      <div class="req-top"><span class="req-title">${esc(r.title)}</span><span class="st ${r.status}">${T(STATUS[r.status])}</span></div>
      ${r.latest ? `<div class="req-snip">${esc(plain(r.latest.body))}</div>` : ''}
      <div class="req-meta"><span class="ref">${esc(refOf(p, r))}</span><span class="tag ${esc(r.tag)}">${esc(tagLabel(p, r.tag))}</span>
        ${r.agent ? `<img class="mini" src="${agentIcon(r.agent)}" alt="">` : ''}
        <span class="m">${icon('chat')} ${r.replies}</span>${r.votes ? `<span class="m">+${r.votes}</span>` : ''}<span class="m">${esc(ago(r.updated))}</span></div>
    </button>`;
  const loading = requests[p.id] === undefined;
  return `<section class="forum">
    <div class="pane-head" data-drag>${mark(p, 'lg')}<div class="ph-text"><b>${esc(p.name)}</b><span>${esc(p.description || '')}</span></div>
      <button class="ib" data-settings="${esc(p.id)}" title="${esc(T('project_settings'))}">${icon('gear')}</button></div>
    <div class="forum-scroll">
      <form class="composer" data-compose><textarea data-editor="new" rows="1" placeholder="${esc(T('new_request'))}"></textarea>
        <div class="composer-foot"><span class="hint">${T('composer_hint')}</span><button class="btn primary sm" type="submit">${T('post')}</button></div></form>
      <div class="filters"><div class="seg">${[['open', T('open'), open.length], ['resolved', T('resolved'), done.length]].map(([k, v, n]) => `<button type="button" class="${scope === k ? 'on' : ''}" data-scope="${k}">${v}<span>${n}</span></button>`).join('')}</div>
        <span class="spacer"></span>${choice('reqTag', filter, [['all', T('all_tags')], ...p.tags.map((t) => [t.id, t.label])])}</div>
      ${loading ? '' : shown.map(card).join('') || `<div class="empty-list">${scope === 'open' ? T('no_open_requests') : T('nothing_resolved')}</div>`}
    </div>
  </section>`;
}

function threadView(p) {
  const r = openRequest && threads[`${p.id}/${openRequest}`];
  if (!r) return `<section class="thread empty"><p>${T(openRequest ? 'loading' : 'pick_request')}</p></section>`;
  let lastAgent = null;
  const msgs = r.messages.map((m, i) => {
    let divider = '';
    if (m.agent && lastAgent && m.agent !== lastAgent) divider = `<div class="sys">${esc(agentName(m.agent))}</div>`;
    if (m.agent) lastAgent = m.agent;
    return divider + `<div class="post ${i === 0 ? 'first' : ''}">${avatar(m)}<div class="post-body"><div class="post-h"><b>${esc(authorName(m))}</b><span class="t">${esc(clock(m.at))}</span></div>
      <div class="md">${markdown(m.body)}</div></div></div>`;
  }).join('');
  const session = r.session && sessions.find((s) => s.id === r.session);
  return `<section class="thread">
    <div class="pane-head thread-head" data-drag><span class="ref">${esc(refOf(p, r))}</span><b class="thread-title">${esc(r.title)}</b>
      ${choice('setTag', r.tag, p.tags.map((t) => [t.id, t.label]), { cls: 'tag-choice' })}<span class="st ${r.status}">${T(STATUS[r.status])}</span><span class="spacer"></span>
      ${session ? `<button class="btn sm" data-session="${esc(session.id)}"><img class="mini" src="${agentIcon(session.agent)}" alt="">${T('open_session')}</button>` : ''}
      ${r.status !== 'resolved' ? `<button class="btn sm" data-resolve>${icon('check')} ${T('resolve')}</button>` : `<button class="btn sm" data-reopen>${T('reopen')}</button>`}</div>
    <div class="thread-scroll">${msgs}</div>
    <form class="reply" data-reply><textarea data-editor="reply" rows="1" placeholder="${esc(T('reply'))}"></textarea><button class="btn primary sm" type="submit">${T('send')}</button></form>
  </section>`;
}

// ---------- session ----------
const terms = {};
const tabsOf = (id) => (terms[id] ||= { list: [{ id: 'agent' }], active: 'agent', n: 0 });
function sessionView(s) {
  const t = tabsOf(s.id);
  const request = Object.entries(requests).flatMap(([pid, list]) => list.filter((r) => r.session === s.id).map((r) => [projectOf(pid), r]))[0];
  const tabs = t.list.map((x) => x.shell
    ? `<span class="tab ${x.id === t.active ? 'on' : ''}" data-term-tab="${x.id}">${icon('term')}${esc(x.title)}<button type="button" class="tab-x" data-term-close="${x.id}" title="${esc(T('close_tab'))}">${icon('close')}</button></span>`
    : `<span class="tab ${x.id === t.active ? 'on' : ''}" data-term-tab="agent"><img src="${agentIcon(s.agent)}" alt="">${esc(agentName(s.agent))}</span>`).join('');
  return `<section class="tui-pane">
    <div class="tabs" data-drag>${tabs}<button type="button" class="ib sm" data-new-term title="${esc(T('new_tab'))}">${icon('plus')}</button><span class="spacer"></span>
      ${request?.[0] ? `<button class="btn sm" data-goto="${esc(request[0].id)}/${request[1].number}"><span class="ref">${esc(refOf(...request))}</span>${esc(request[1].title)}</button>` : ''}</div>
    <div class="tui" id="term-slot"></div>
  </section>`;
}

// Terminals live outside render() so switching views keeps them running.
const terminals = new Map();
const pool = document.createElement('div');
pool.hidden = true;
// Terminals are Ghostty's emulator (ghostty-web: the libghostty-vt core that
// herdr desktop also uses, compiled to WebAssembly) with herdr's defaults:
// JetBrains Mono 14 at weight 500, line height 1, Graphite/Paper colors.
const ghostty = window.GhosttyWeb ? GhosttyWeb.init() : Promise.reject(new Error('ghostty-web missing'));
function terminalFor(key, kind, session) {
  let t = terminals.get(key);
  if (t) return t;
  const el = document.createElement('div');
  el.className = 'term';
  t = { el, term: null, fit: null, open: false, ended: false, queued: [] };
  terminals.set(key, t);
  pool.append(el);
  ghostty.then(() => {
    if (terminals.get(key) !== t) return;
    const term = new GhosttyWeb.Terminal({ fontSize: 14, fontFamily: '"JetBrains Mono", "SF Mono", Menlo, monospace', cursorBlink: true, cursorStyle: 'block', scrollback: 10000, theme: termTheme() });
    const fit = new GhosttyWeb.FitAddon();
    term.loadAddon(fit);
    term.open(el);
    t.term = term; t.fit = fit;
    try { window.installTerminalLinks?.(term, (url) => post({ action: 'open-url', url })); } catch (_) {}
    term.onData((data) => { if (t.open) post({ action: 'term-input', key, data }); });
    term.onResize(({ cols, rows }) => { if (t.open) post({ action: 'term-resize', key, cols, rows }); });
    new ResizeObserver(() => { if (el.isConnected && el.offsetWidth) fit.fit(); }).observe(el);
    requestAnimationFrame(() => {
      fit.fit();
      t.open = true;
      post({ action: 'term-open', key, kind, session, cols: term.cols, rows: term.rows });
      if (el.isConnected) term.focus();
    });
  }, () => { t.ended = true; });
  return t;
}
// herdr's Graphite (dark) and Paper (light) palettes (herdr-app, Apache-2.0).
const termTheme = () => dark()
  ? { background: '#17191d', foreground: '#d2d4d9', cursor: '#d2d4d9', cursorAccent: '#17191d', selectionBackground: '#394454',
    black: '#45484f', red: '#dc8c95', green: '#a6bd91', yellow: '#d8be87', blue: '#96b2d7', magenta: '#b5a2ce', cyan: '#8cbdc3', white: '#d2d4d9',
    brightBlack: '#818793', brightRed: '#f0a5ad', brightGreen: '#bad5a2', brightYellow: '#ead2a1', brightBlue: '#b0cbef', brightMagenta: '#cbb7e6', brightCyan: '#a4d5db', brightWhite: '#f0f0f2' }
  : { background: '#ffffff', foreground: '#25272c', cursor: '#25272c', cursorAccent: '#ffffff', selectionBackground: '#cfdcf5',
    black: '#25272c', red: '#c62a2a', green: '#1a7f37', yellow: '#8f5d00', blue: '#1f62d0', magenta: '#7a45c8', cyan: '#0f6f74', white: '#5f6068',
    brightBlack: '#7b7e87', brightRed: '#d1242f', brightGreen: '#1f883d', brightYellow: '#9a6700', brightBlue: '#0969da', brightMagenta: '#8250df', brightCyan: '#1b7c83', brightWhite: '#111214' };
window.termOutput = (key, base64) => {
  const t = terminals.get(key);
  if (!t?.term) return;
  const bytes = Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
  t.term.write(bytes, () => post({ action: 'term-credit', key, bytes: bytes.length }));
};
window.termEnded = (key, failed) => {
  const t = terminals.get(key);
  if (!t) return;
  t.open = false; t.ended = true;
  t.term?.write(`\r\n\x1b[2m${failed ? T('terminal_failed') : T('terminal_closed')}\x1b[0m\r\n`);
};
function closeTerminal(key) {
  const t = terminals.get(key);
  if (!t) return;
  post({ action: 'term-close', key });
  t.term?.dispose(); t.el.remove();
  terminals.delete(key);
}
function mountTerminal() {
  const slot = $('#term-slot');
  if (!slot || view.type !== 'session') return;
  const s = sessions.find((x) => x.id === view.id);
  if (!s) return;
  const t = tabsOf(s.id), tab = t.list.find((x) => x.id === t.active) || t.list[0];
  const key = `${s.id}:${tab.id}`;
  const term = terminalFor(key, tab.shell ? 'shell' : 'agent', s.id);
  slot.append(term.el);
  requestAnimationFrame(() => { term.fit?.fit(); term.term?.focus(); });
}

// ---------- settings ----------
const SETTINGS_TABS = [['general', 'general', 'sliders'], ['knowledge', 'knowledge', 'book'], ['queue', 'queue', 'layers'], ['agents', 'agents', 'bot']];
function settingsView(p) {
  const body = { general: settingsGeneral, knowledge: settingsKnowledge, queue: settingsQueue, agents: settingsAgents }[settingsTab](p);
  return `<section class="settings">
    <div class="pane-head" data-drag><button class="ib" data-project="${esc(p.id)}" title="${esc(T('back_to_requests'))}">${icon('back')}</button>
      ${mark(p, 'lg')}<div class="ph-text"><b>${esc(p.name)}</b><span>${T('project_settings')}</span></div></div>
    <div class="set-wrap"><nav class="set-nav">${SETTINGS_TABS.map(([k, label, ic]) => `
      <button class="side-row ${settingsTab === k ? 'sel' : ''}" data-set-tab="${k}">${icon(ic)}<span class="side-name">${T(label)}</span>
      ${k === 'knowledge' && knowledge[p.id]?.length ? `<span class="side-meta">${knowledge[p.id].length}</span>` : ''}</button>`).join('')}</nav>
    <div class="set-body ${settingsTab}">${body}</div></div>
  </section>`;
}

function settingsGeneral(p) {
  return `<h2>${T('general')}</h2>
    <div class="set-card">
      <div class="row"><span>${T('icon')}</span><div class="icon-edit">${mark(p, 'xl')}
        <div class="swatches">${COLORS.map((c) => `<button type="button" class="swatch ${!p.icon && p.color === c ? 'on' : ''}" style="--c:${c}" data-color="${c}" aria-label="${esc(c)}"></button>`).join('')}</div>
        <span class="spacer"></span>
        <label class="btn sm">${T('upload_image')}<input type="file" accept="image/png,image/jpeg,image/gif,image/webp" data-icon-file hidden></label>
        ${p.icon ? `<button type="button" class="link-btn" data-icon-clear>${T('remove')}</button>` : ''}</div></div>
      <label class="row"><span>${T('name')}</span><input data-field="name" value="${esc(p.name)}" maxlength="80"></label>
      <label class="row"><span>${T('key')}</span><input data-field="key" value="${esc(p.key)}" maxlength="6" spellcheck="false" class="key-input"><em class="key-hint">${T('key_hint', { key: esc(p.key) })}</em></label>
      <label class="row"><span>${T('description')}</span><input data-field="description" value="${esc(p.description || '')}" maxlength="300"></label>
      <label class="row"><span>${T('repository')}</span><input data-field="repository" value="${esc(p.repository || '')}" placeholder="owner/repo" spellcheck="false"></label>
    </div>
    <h3>${T('tags')}</h3><p class="hint">${T('tags_hint')}</p>
    <div class="set-card"><div class="tag-edit">${p.tags.map((t) => `
      <span class="tag ${esc(t.id)}">${esc(t.label)}<button type="button" class="tag-x" data-tag-remove="${esc(t.id)}" title="${esc(T('remove'))}" ${p.tags.length < 2 ? 'disabled' : ''}>${icon('close')}</button></span>`).join('')}
      <input class="tag-new" data-tag-add placeholder="${esc(T('add_tag'))}" maxlength="24"></div>
      <div class="row-foot"><button type="button" class="link-btn" data-tags-reset>${T('reset_tags')}</button></div></div>`;
}

const SECTIONS = { architecture: 'section_architecture', runbooks: 'section_runbooks', decisions: 'section_decisions', glossary: 'section_glossary' };
const sectionName = (folder) => (SECTIONS[folder] ? T(SECTIONS[folder]) : folder.replace(/[-_]+/g, ' ').replace(/^./, (c) => c.toUpperCase()));
const actorName = (actor) => (actor.startsWith('human:') ? actor.slice(6) : actor === 'claude-code' || actor === 'claude' ? 'Claude Code' : agentName(actor));
const reviewed = (c) => c.verified.some((v) => v.by.startsWith('human:'));
const KSTATUS = { draft: 'k_draft', stable: 'k_current', deprecated: 'k_deprecated' };
function settingsKnowledge(p) {
  const all = knowledge[p.id];
  if (!all) return `<h2>${T('knowledge')}</h2><p class="hint">${T('loading')}</p>`;
  const list = all.filter((c) => knowledgeFilter === 'all' || (knowledgeFilter === 'review' ? !reviewed(c) : c.status === knowledgeFilter));
  const c = list.find((x) => x.id === knowledgeId) || list[0];
  const folders = [...new Set(list.map((x) => (x.id.includes('/') ? x.id.split('/')[0] : '')))];
  const row = (x) => `<button class="k-row ${x === c ? 'sel' : ''}" data-concept="${esc(x.id)}"><span class="k-title">${esc(x.title)}</span>
    <span class="k-sub"><span class="trust ${reviewed(x) ? 'human' : 'none'}"></span><span class="k-type">${esc(x.type)}</span>${x.status !== 'stable' ? `<span class="k-status ${esc(x.status)}">${T(KSTATUS[x.status] || 'k_draft')}</span>` : ''}</span></button>`;
  const when = (iso) => (iso ? new Date(iso).toLocaleString(language, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '');
  return `<div class="k-head"><h2>${T('knowledge')}</h2><span class="hint">${T('knowledge_hint', { name: esc(p.name) })}</span></div>
    <div class="filters">${[['all', 'all'], ['review', 'needs_review'], ['draft', 'k_draft'], ['deprecated', 'k_deprecated']].map(([k, v]) => `<button class="chip ${knowledgeFilter === k ? 'on' : ''}" data-kfilter="${k}">${T(v)}</button>`).join('')}</div>
    ${!all.length ? `<div class="set-card k-empty">${T('knowledge_empty')}</div>` : c ? `<div class="k-split"><div class="k-list">${folders.map((f) => (f ? `<div class="group">${esc(sectionName(f))}</div>` : '') + list.filter((x) => (x.id.includes('/') ? x.id.split('/')[0] : '') === f).map(row).join('')).join('')}</div>
    <article class="k-doc"><div class="k-doc-h"><b>${esc(c.title)}</b><span class="spacer"></span>
      ${!reviewed(c) ? `<button class="btn sm" data-review="review">${icon('check')} ${T('mark_reviewed')}</button>` : ''}
      ${c.status === 'deprecated' ? `<button class="btn sm" data-review="restore">${T('restore')}</button>` : `<button class="btn sm" data-review="deprecate">${T('deprecate')}</button>`}</div>
      ${c.description ? `<p class="k-desc">${esc(c.description)}</p>` : ''}
      <dl class="k-fields"><dt>${T('status')}</dt><dd>${T(KSTATUS[c.status] || 'k_draft')}</dd>
        ${c.generated_by ? `<dt>${T('written_by')}</dt><dd>${esc(actorName(c.generated_by))} · ${esc(when(c.generated_at))}</dd>` : ''}
        <dt>${T('reviewed_by')}</dt><dd>${c.verified.length ? c.verified.map((v) => `${esc(actorName(v.by))} · ${esc(when(v.at))}`).join('<br>') : T('not_yet')}</dd>
        ${c.tags.length ? `<dt>${T('tags')}</dt><dd>${c.tags.map((t) => `<span class="k-tag">${esc(t)}</span>`).join(' ')}</dd>` : ''}
        ${c.sources.length ? `<dt>${T('based_on')}</dt><dd>${c.sources.map((s) => `<span class="k-src">${esc(s)}</span>`).join(' ')}</dd>` : ''}</dl>
      <div class="md k-body">${markdown(c.body)}</div></article></div>` : `<div class="set-card k-empty">${T('nothing_here')}</div>`}`;
}

function settingsQueue(p) {
  const list = requests[p.id] || [];
  const working = list.filter((r) => r.status === 'working'), queued = list.filter((r) => r.status === 'queued').reverse();
  const row = (r, i) => `<button type="button" class="q-row" data-goto="${esc(p.id)}/${r.number}">
    ${r.status === 'working' && r.agent ? `<img class="mini" src="${agentIcon(r.agent)}" alt="">` : `<span class="q-pos">${i + 1}</span>`}
    <span class="ref">${esc(refOf(p, r))}</span><span class="q-title">${esc(r.title)}</span><span class="tag ${esc(r.tag)}">${esc(tagLabel(p, r.tag))}</span><span class="st ${r.status}">${T(STATUS[r.status])}</span></button>`;
  return `<h2>${T('queue')}</h2>
    <div class="set-card"><div class="row wide"><span>${T('at_once')}<small>${T('at_once_hint')}</small></span>
      <div class="stepper"><button type="button" data-queue="-1" ${p.queue <= 1 ? 'disabled' : ''} aria-label="${esc(T('fewer'))}">−</button><b>${p.queue}</b><button type="button" data-queue="1" ${p.queue >= 10 ? 'disabled' : ''} aria-label="${esc(T('more'))}">+</button></div></div></div>
    <h3>${T('now_heading')}</h3><p class="hint">${T('in_progress', { n: working.length, limit: p.queue })}${queued.length ? ' · ' + T('waiting', { n: queued.length }) : ''}</p>
    <div class="set-card q-list">${working.map(row).join('')}${queued.map(row).join('')}
      ${working.length + queued.length ? '' : `<div class="q-empty">${T('queue_empty')}</div>`}</div>`;
}

function settingsAgents(p) {
  const on = p.workers.filter((w) => w.on).length;
  return `<h2>${T('agents')}</h2><p class="hint">${T('agents_hint')}</p>
    <div class="set-card workers">${p.workers.map((w, i) => `
      <div class="worker ${w.on ? '' : 'off'}"><img src="${agentIcon(w.agent)}" alt=""><b>${esc(agentName(w.agent))}</b>
        <input class="model-input" data-worker-model="${i}" value="${esc(w.model || '')}" placeholder="${esc(T('default_model'))}" spellcheck="false" maxlength="80">
        <span class="spacer"></span>
        <button type="button" class="switch" role="switch" aria-checked="${w.on}" data-worker-on="${i}" ${w.on && on < 2 ? 'disabled' : ''}></button>
        <button type="button" class="ib" data-worker-remove="${i}" title="${esc(T('remove'))}" ${p.workers.length < 2 || (w.on && on < 2) ? 'disabled' : ''}>${icon('trash')}</button></div>`).join('')}
      <div class="row-foot">${choice('workerAdd', null, Object.keys(AGENTS).map((k) => [k, agentName(k), agentIcon(k)]), { placeholder: T('add_worker') })}</div></div>`;
}

// ---------- app settings ----------
async function loadLoginItem() {
  try { loginItem = await rpc('login-item.status'); } catch (_) { loginItem = null; }
  if (view.type === 'app') render();
}
const LANGUAGES = [['en-US', 'English (US)'], ['en-GB', 'English (UK)'], ['zh-CN', 'chinese_simplified'], ['zh-TW', 'chinese_traditional'], ['ja', 'japanese'], ['ko', 'korean']];
function appSettings() {
  const theme = prefs.theme || 'system', mode = prefs.permission_mode || 'ask';
  const modes = [['ask', 'sandbox_mode_ask', 'permission_mode_ask_description'], ['auto', 'sandbox_mode_auto', 'permission_mode_auto_description'], ['full', 'sandbox_mode_full', 'permission_mode_full_description']];
  return `<section class="settings app-settings">
    <div class="pane-head" data-drag><div class="ph-text"><b>${T('settings')}</b></div></div>
    <div class="set-body">
      <h2>${T('general')}</h2>
      <div class="set-card">
        <div class="row"><span>${TU('appearance')}</span><div class="seg">${[['system', 'system'], ['light', 'light'], ['dark', 'dark']].map(([k, v]) => `<button type="button" class="${theme === k ? 'on' : ''}" data-theme="${k}">${TU(v)}</button>`).join('')}</div></div>
        <div class="row"><span>${TU('language')}</span>${choice('language', language, LANGUAGES.map(([k, v]) => [k, k.startsWith('en') ? v : TU(v)]))}</div>
        <div class="row"><span>${TU('launch_at_login')}</span>
          <button type="button" class="switch" role="switch" aria-checked="${!!loginItem?.enabled}" data-login-item ${!loginItem?.supported || loginItem?.busy ? 'disabled' : ''}></button></div>
      </div>
      <h3>${TU('permission_mode')}</h3>
      <div class="set-card modes">${modes.map(([k, label, desc]) => `
        <button type="button" class="mode ${mode === k ? 'on' : ''}" data-permission="${k}" role="radio" aria-checked="${mode === k}"><span class="radio"></span><span class="mode-text"><b>${TU(label)}</b><small>${TU(desc)}</small></span></button>`).join('')}</div>
      <h3>${T('account')}</h3>
      <div class="set-card">
        <div class="row"><span>${T('signed_in_as')}</span><b class="value">${esc(me.name || '')}</b>
          <button type="button" class="btn sm" data-dashboard>${T('usage_and_billing')}</button><button type="button" class="btn sm" data-sign-out>${TU('sign_out')}</button></div>
      </div>
      <p class="hint about">Fennec ${esc(version)}</p>
    </div>
  </section>`;
}

// ---------- dialogs ----------
function projectModal() {
  return `<div class="scrim" data-scrim><form class="modal" data-new-project-form>
    <div class="m-head"><b>${T('new_project')}</b><button type="button" class="ib" data-cancel>${icon('close')}</button></div>
    <label class="fld"><span>${T('name')}</span><input name="name" required maxlength="80" autofocus></label>
    <label class="fld"><span>${T('description')}</span><input name="description" maxlength="300"></label>
    <label class="fld"><span>${T('repository')}</span><input name="repository" placeholder="owner/repo" spellcheck="false"></label>
    ${notice ? `<p class="form-error">${esc(notice)}</p>` : ''}
    <div class="m-foot"><button type="button" class="btn" data-cancel>${T('cancel')}</button><button class="btn primary" ${busy ? 'disabled' : ''}>${T('create')}</button></div>
  </form></div>`;
}
// ---------- render ----------
function mainView() {
  if (!me.signedIn) return `<section class="center"><img class="fox" src="icons/fennec.svg" alt=""><p>${T('sign_in_first')}</p><button class="btn primary" data-open-panel>${T('open_fennec')}</button></section>`;
  if (view.type === 'app') return appSettings();
  if (view.type === 'session') {
    const s = sessions.find((x) => x.id === view.id);
    if (s && starting === s.id) starting = '';
    return s ? sessionView(s) : `<section class="center"><p>${T(starting === view.id ? 'starting' : 'session_ended')}</p></section>`;
  }
  if (projects === null) return `<section class="center"><p>${esc(boardProblem || T('connecting'))}</p></section>`;
  const p = projectOf(view.id) || visibleProjects()[0];
  if (!p) return `<section class="center"><p>${T('no_projects')}</p><button class="btn primary" data-new-project>${icon('plus')} ${T('new_project')}</button></section>`;
  if (view.id !== p.id) { view = { type: view.type, id: p.id }; save(); }
  if (view.type === 'settings') return settingsView(p);
  return projectView(p) + threadView(p);
}

function render() {
  const focus = document.activeElement, focusKey = focus?.dataset?.editor || (focus && 'search' in focus.dataset ? 'search' : null);
  const draft = focus?.tagName === 'TEXTAREA' ? focus.value : null, at = focus?.selectionStart;
  const keep = {};
  document.querySelectorAll('textarea[data-editor]').forEach((t) => { keep[t.dataset.editor] = t.value; });
  for (const t of terminals.values()) pool.append(t.el);
  const scrolled = Object.fromEntries(SCROLLERS.map((sel) => [sel, $(sel)?.scrollTop]));
  $('#app').innerHTML = sidebar() + `<main class="main">${mainView()}</main>` + (newProject ? projectModal() : '') + (menu ? choiceMenu() : '') + (notice && !newProject ? `<div class="toast">${esc(notice)}</div>` : '');
  document.body.append(pool);
  document.querySelectorAll('textarea[data-editor]').forEach((t) => { if (keep[t.dataset.editor]) { t.value = keep[t.dataset.editor]; grow(t); } });
  if (focusKey) {
    const el = focusKey === 'search' ? $('[data-search]') : $(`[data-editor="${focusKey}"]`);
    if (el) { if (draft !== null) el.value = draft; el.focus(); if (at != null) el.setSelectionRange(at, at); }
  }
  for (const sel of SCROLLERS) if ($(sel) && scrolled[sel] != null) $(sel).scrollTop = scrolled[sel];
  // A thread opens at its newest message, and follows new ones.
  const t = $('.thread-scroll'), r = threads[`${view.id}/${openRequest}`];
  const bottom = r ? `${view.id}/${openRequest}/${r.messages.length}` : '';
  if (t && bottom !== threadBottom) { t.scrollTop = t.scrollHeight; threadBottom = bottom; }
  mountTerminal();
  dragRegions();
}
const SCROLLERS = ['.side-scroll', '.forum-scroll', '.thread-scroll', '.set-body', '.k-list', '.k-doc'];
let threadBottom = '';
const grow = (t) => { t.style.height = 'auto'; t.style.height = Math.min(t.scrollHeight, 240) + 'px'; };

// The window has no title bar; the page tells the window which strips drag it.
function dragRegions() {
  const rect = (el) => { const r = el.getBoundingClientRect(); return [r.left, r.top, r.width, r.height]; };
  const drag = [...document.querySelectorAll('[data-drag]')].map(rect);
  const exclude = [...document.querySelectorAll('[data-drag] button, [data-drag] input, [data-drag] .tab, [data-drag] a')].map(rect);
  post({ action: 'drag-regions', drag, exclude });
}
addEventListener('resize', dragRegions);

// ---------- data ----------
let loadingProjects = false;
async function loadProjects() {
  if (!me.signedIn || loadingProjects) return;
  loadingProjects = true;
  try {
    projects = await board('GET', '/v1/projects');
    boardProblem = '';
  } catch (error) {
    if (projects === null) boardProblem = error.code === 'not_ready' ? T('connecting') : T('board_unavailable');
  } finally { loadingProjects = false; }
}
async function loadRequests(id) {
  try { requests[id] = await board('GET', `/v1/projects/${id}/requests`); } catch (_) {}
}
async function loadThread(id, n) {
  try { threads[`${id}/${n}`] = await board('GET', `/v1/projects/${id}/requests/${n}`); } catch (_) {}
}
async function loadKnowledge(id) {
  try { knowledge[id] = await board('GET', `/v1/projects/${id}/knowledge`); } catch (_) { knowledge[id] ||= []; }
}
async function refresh() {
  if (!me.signedIn || document.hidden || menu) return;
  await loadProjects();
  const p = view.type !== 'session' && (projectOf(view.id) || visibleProjects()[0]);
  if (p) {
    await loadRequests(p.id);
    if (view.type === 'project' && openRequest) await loadThread(p.id, openRequest);
    if (view.type === 'settings' && settingsTab === 'knowledge' && !knowledge[p.id]) await loadKnowledge(p.id);
  }
  // Requests of other projects too, so a session can link to its request.
  for (const other of visibleProjects()) if (other !== p && requests[other.id] === undefined) await loadRequests(other.id);
  if (!document.querySelector('.scrim')) render();
}
setInterval(refresh, 3000);
document.addEventListener('visibilitychange', refresh);

function flash(message) { notice = message; render(); setTimeout(() => { if (notice === message) { notice = ''; render(); } }, 4000); }
const failed = (error) => flash(error?.message || T('try_again'));
async function patchProject(p, change) {
  try { Object.assign(p, await board('PATCH', `/v1/projects/${p.id}`, change)); } catch (error) { failed(error); }
  render();
}

// Native pushes the account, language and the session list.
window.desktopState = (state) => {
  if (state.language && state.language !== language) language = state.language;
  if (state.dashboard) dashboard = state.dashboard;
  if (state.settings) prefs = state.settings;
  if (state.version) version = state.version;
  if (state.agents) cloudAgents = state.agents;
  const wasSignedIn = me.signedIn;
  if (state.account) me = state.account;
  if (state.sessions) sessions = state.sessions.filter((t) => t.state === 'live').map((t) => {
    const raw = typeof t.status === 'string' ? t.status : t.status?.state;
    const id = t.labels?.['fennec.session'];
    return { id, agent: t.labels?.['fennec.agent'] || t.agent, title: (t.title || t.name || '').replace(/^[⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏]\s*/u, '').trim(), group: ['blocked', 'working'].includes(raw) ? raw : 'idle', since: t.status_ms || t.created_ms };
  }).filter((s) => s.id);
  if (me.signedIn && !wasSignedIn) { loadOrganizations(); refresh(); }
  if (!menu && !document.querySelector('.scrim')) render();
};
async function loadOrganizations() {
  try {
    orgs = await rpc('account.organizations');
    if (!orgs.some((o) => o.id === org)) org = (orgs.find((o) => o.current) || orgs[0])?.id || '';
    save(); render();
  } catch (_) {}
}

// ---------- actions ----------
function choose(key, value) {
  const p = projectOf(view.id);
  if (key === 'org') {
    if (value === 'new') { post({ action: 'open-url', url: dashboard }); return; }
    org = value; save(); openRequest = null; filter = 'all'; scope = 'open';
    const first = visibleProjects()[0];
    view = { type: 'project', id: first?.id || '' };
  }
  if (key === 'reqTag') filter = value;
  if (key === 'setTag' && p && openRequest) {
    board('PATCH', `/v1/projects/${p.id}/requests/${openRequest}`, { tag: value }).then((r) => { threads[`${p.id}/${openRequest}`] = r; loadRequests(p.id).then(render); }, failed);
  }
  if (key === 'workerAdd' && p) patchProject(p, { workers: [...p.workers, { agent: value, model: '', on: true }] });
  if (key === 'newSession') startSession(value);
  if (key === 'language') rpc('settings.set', { language: value }).then(() => { language = value; prefs.language = value; render(); }, failed);
}

document.addEventListener('click', async (e) => {
  const link = e.target.closest('a[data-url]');
  if (link) { e.preventDefault(); post({ action: 'open-url', url: link.dataset.url }); return; }
  const b = e.target.closest('button,[data-scrim],[data-term-tab]');
  if (menu && b?.dataset.choose !== undefined) { const key = menu.key; menu = null; choose(key, b.dataset.choose); render(); return; }
  if (b?.dataset.choice) {
    const key = b.dataset.choice, r = b.getBoundingClientRect();
    menu = menu?.key === key ? null : { key, value: b.dataset.value, options: JSON.parse(b.dataset.options), x: r.left, y: r.bottom + 4, w: Math.max(r.width, 180) };
    render(); return;
  }
  if (menu) { menu = null; render(); if (!b || b.matches('[data-scrim]')) return; }
  if (!b) return;
  if (b.matches('[data-scrim]') && e.target === b) { newProject = false; notice = ''; render(); return; }
  const d = b.dataset, p = projectOf(view.id);
  if ('termTab' in d && !e.target.closest('[data-term-close]')) { tabsOf(view.id).active = d.termTab; render(); return; }
  if (d.termClose) {
    const t = tabsOf(view.id), i = t.list.findIndex((x) => x.id === d.termClose);
    closeTerminal(`${view.id}:${d.termClose}`);
    t.list.splice(i, 1);
    if (t.active === d.termClose) t.active = t.list[Math.max(0, i - 1)].id;
    render(); return;
  }
  if ('newTerm' in d) {
    const t = tabsOf(view.id), id = 's' + (++t.n);
    t.list.push({ id, shell: true, title: T('shell') }); t.active = id; render(); return;
  }
  if ('openPanel' in d) { post({ action: 'open-panel' }); return; }
  if ('appSettings' in d) { view = { type: 'app' }; save(); render(); loadLoginItem(); return; }
  if (d.theme) { prefs.theme = d.theme; post({ action: 'theme', theme: d.theme }); render(); rpc('settings.set', { theme: d.theme }).catch(failed); return; }
  if (d.permission) { const before = prefs.permission_mode; prefs.permission_mode = d.permission; render(); rpc('settings.set', { permission_mode: d.permission }).catch((e) => { prefs.permission_mode = before; failed(e); }); return; }
  if ('loginItem' in d) {
    const on = !loginItem?.enabled; loginItem = { ...loginItem, busy: true }; render();
    try { loginItem = await rpc(on ? 'login-item.on' : 'login-item.off'); } catch (error) { failed(error); await loadLoginItem(); }
    render(); return;
  }
  if ('signOut' in d) { try { await rpc('account.logout'); } catch (error) { failed(error); } return; }
  if ('dashboard' in d) { post({ action: 'open-url', url: dashboard }); return; }
  if (d.project) { view = { type: 'project', id: d.project }; openRequest = null; filter = 'all'; scope = 'open'; save(); render(); loadRequests(d.project).then(render); return; }
  if (d.session) { view = { type: 'session', id: d.session }; save(); render(); return; }
  if (d.goto) {
    const [pid, n] = d.goto.split('/');
    view = { type: 'project', id: pid }; openRequest = +n; save(); render();
    await loadThread(pid, +n); render(); return;
  }
  if (d.request) { openRequest = +d.request; render(); await loadThread(p.id, openRequest); render(); return; }
  if (d.scope) { scope = d.scope; render(); return; }
  if ('resolve' in d || 'reopen' in d) {
    try { threads[`${p.id}/${openRequest}`] = await board('PATCH', `/v1/projects/${p.id}/requests/${openRequest}`, { status: 'resolve' in d ? 'resolved' : 'you' }); } catch (error) { failed(error); }
    await loadRequests(p.id); render(); return;
  }
  if (d.settings) { view = { type: 'settings', id: d.settings }; save(); render(); return; }
  if (d.setTab) {
    settingsTab = d.setTab; render();
    if (d.setTab === 'knowledge') { await loadKnowledge(p.id); render(); }
    return;
  }
  if (d.color) { patchProject(p, { color: d.color, icon: '' }); return; }
  if ('iconClear' in d) { patchProject(p, { icon: '' }); return; }
  if (d.tagRemove) { patchProject(p, { tags: p.tags.filter((t) => t.id !== d.tagRemove) }); return; }
  if ('tagsReset' in d) { patchProject(p, { tags: [{ id: 'bug', label: T('tag_bug') }, { id: 'feature', label: T('tag_feature') }, { id: 'discussion', label: T('tag_discussion') }] }); return; }
  if (d.queue) { patchProject(p, { queue: Math.min(10, Math.max(1, p.queue + +d.queue)) }); return; }
  if (d.workerOn) { patchProject(p, { workers: p.workers.map((w, i) => (i === +d.workerOn ? { ...w, on: !w.on } : w)) }); return; }
  if (d.workerRemove) { patchProject(p, { workers: p.workers.filter((_, i) => i !== +d.workerRemove) }); return; }
  if (d.kfilter) { knowledgeFilter = d.kfilter; render(); return; }
  if (d.concept) { knowledgeId = d.concept; render(); return; }
  if (d.review) {
    const list = knowledge[p.id], c = list.find((x) => x.id === knowledgeId) || $('.k-row.sel') && list.find((x) => x.id === $('.k-row.sel').dataset.concept);
    if (!c) return;
    try { await board('POST', `/v1/projects/${p.id}/knowledge/review`, { id: c.id, action: d.review, person: me.name || 'me' }); } catch (error) { failed(error); }
    knowledgeId = c.id; await loadKnowledge(p.id); render(); return;
  }
  if ('newProject' in d) { newProject = true; notice = ''; render(); $('.modal input')?.focus(); return; }
  if ('cancel' in d) { newProject = false; notice = ''; render(); }
});

document.addEventListener('submit', async (e) => {
  e.preventDefault();
  const f = e.target, p = projectOf(view.id);
  if ('newProjectForm' in f.dataset) {
    const fd = new FormData(f), name = fd.get('name').trim();
    if (!name) return;
    busy = true; notice = ''; render();
    try {
      const created = await board('POST', '/v1/projects', { name, org, description: fd.get('description').trim(), repository: fd.get('repository').trim(), color: COLORS[(projects?.length || 0) % COLORS.length], workers: [{ agent: 'claude', model: '', on: true }] });
      newProject = false; view = { type: 'project', id: created.id }; save();
      await loadProjects();
    } catch (error) { notice = error.code === 'invalid_params' ? T('check_repository') : error.message || T('try_again'); }
    busy = false; render(); return;
  }
  if ('compose' in f.dataset) {
    const text = f.querySelector('textarea').value.trim();
    if (!text || !p) return;
    const [title, ...rest] = text.split('\n');
    try {
      const r = await board('POST', `/v1/projects/${p.id}/requests`, { author: me.name || 'me', title: title.slice(0, 200), body: rest.join('\n').trim() ? text : '' });
      f.querySelector('textarea').value = '';
      openRequest = r.number; threads[`${p.id}/${r.number}`] = r; scope = 'open';
      await loadRequests(p.id);
    } catch (error) { failed(error); }
    render(); return;
  }
  if ('reply' in f.dataset) {
    const text = f.querySelector('textarea').value.trim();
    if (!text || !p || !openRequest) return;
    try {
      threads[`${p.id}/${openRequest}`] = await board('POST', `/v1/projects/${p.id}/requests/${openRequest}/messages`, { author: me.name || 'me', body: text });
      f.querySelector('textarea').value = '';
      await loadRequests(p.id);
    } catch (error) { failed(error); }
    render();
  }
});

document.addEventListener('change', (e) => {
  const t = e.target, d = t.dataset, p = projectOf(view.id);
  if (!p) return;
  if (d.field) {
    let v = t.value.trim();
    if (d.field === 'key') v = v.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 6);
    if ((d.field === 'name' || d.field === 'key') && !v) { render(); return; }
    patchProject(p, { [d.field]: v });
  }
  if (d.workerModel !== undefined) patchProject(p, { workers: p.workers.map((w, i) => (i === +d.workerModel ? { ...w, model: t.value.trim() } : w)) });
  if ('iconFile' in d && t.files[0]) {
    // Icons are kept small: drawn to 128 px.
    const img = new Image();
    img.onload = () => {
      const c = document.createElement('canvas'), s = 128, r = Math.max(s / img.width, s / img.height);
      c.width = c.height = s;
      c.getContext('2d').drawImage(img, (s - img.width * r) / 2, (s - img.height * r) / 2, img.width * r, img.height * r);
      patchProject(p, { icon: c.toDataURL('image/png') });
      URL.revokeObjectURL(img.src);
    };
    img.src = URL.createObjectURL(t.files[0]);
  }
});

document.addEventListener('input', (e) => {
  if (e.target.tagName === 'TEXTAREA') grow(e.target);
  if ('search' in e.target.dataset) { query = e.target.value; render(); }
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape' && menu) { menu = null; render(); return; }
  if (e.key === 'Escape' && newProject) { newProject = false; notice = ''; render(); return; }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); $('[data-search]')?.focus(); $('[data-search]')?.select(); return; }
  if (e.key === 'Escape' && 'search' in (e.target.dataset || {})) { query = ''; render(); return; }
  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey) && e.target.dataset?.editor) { e.preventDefault(); e.target.form.requestSubmit(); return; }
  if (e.key === 'Enter' && 'tagAdd' in (e.target.dataset || {})) {
    e.preventDefault();
    const p = projectOf(view.id), label = e.target.value.trim();
    const id = label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 24) || 'tag-' + (p.tags.length + 1);
    if (label && !p.tags.some((t) => t.id === id)) patchProject(p, { tags: [...p.tags, { id, label }] });
  }
});

matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => { for (const t of terminals.values()) if (t.term) t.term.options.theme = termTheme(); render(); });

render();
post({ action: 'ready' });
})();
