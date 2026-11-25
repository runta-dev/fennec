// Get started (docs/get-started.md): a checklist above the Agents list that
// ends with the user's own agent running on the cloud computer from their
// usual terminal. Incomplete steps use current evidence; once all steps have
// succeeded, completion is durable and independent of runtime health. Nothing is
// required: the card can be hidden, and Fennec works the same without it.
// It reads panel.js's state (detectedAgents, takeoverAgents,
// activeTakeoverAgents, routeResults, lastSessionTerminals) and re-renders
// after each panel render that changes it.
(() => {
  const agentsView = document.querySelector('#agents');
  const list = document.querySelector('#agent-list');
  if (!agentsView || !list) return;
  const card = document.createElement('section');
  card.id = 'get-started';
  card.className = 'card get-started';
  card.hidden = true;
  card.setAttribute('aria-labelledby', 'get-started-title');
  agentsView.insertBefore(card, list);

  const commandOf = id => (id === 'cursor' ? 'cursor-agent' : id);
  const prefs = { hidden: false, done: false, known: false };
  // Values this card saved that the preferences have not echoed back yet. A
  // reply read before the save would otherwise undo it and bring the card back.
  const saving = {};
  // Panel state drops for a moment (a takeover or session refresh, the
  // preparation re-checking), so a step that was done stays done for HOLD and
  // only undoes itself when its condition stays false.
  const HOLD = 15_000;
  const lastTrue = {};
  let holdTimer = null;
  let lastActive = [];
  let preparedBefore = false;
  let coverage = null; // last renderTerminalCoverage reply
  let coverageAsked = 0;
  let coverageKey = '';
  let checking = false;
  let fixing = false;
  let finishing = null;
  // What the card last showed. The panel re-renders every few seconds (the
  // session list), and rebuilding unchanged content made the card flash.
  let shown = '';

  const el = (tag, cls, text) => { const node = document.createElement(tag); if (cls) node.className = cls; if (text !== undefined) node.textContent = text; return node; };
  const code = text => el('code', 'gs-code', text);
  const copyButton = text => {
    const b = el('button', 'gs-copy', "[[i18n:ui.gs_copy]]");
    b.type = 'button';
    b.onclick = () => { send('copy-text:' + text); b.textContent = "[[i18n:ui.gs_copied]]"; setTimeout(() => { b.textContent = "[[i18n:ui.gs_copy]]"; }, 1500); };
    return b;
  };
  const save = value => { Object.assign(saving, value); send('settings:' + JSON.stringify(value)); };
  const held = (name, value, keep = true) => {
    const now = Date.now();
    if (value) { lastTrue[name] = now; return true; }
    const since = now - (lastTrue[name] ?? -Infinity);
    if (!keep || since >= HOLD) { delete lastTrue[name]; return false; }
    if (!holdTimer) holdTimer = setTimeout(() => { holdTimer = null; render(); }, HOLD - since + 50);
    return true;
  };

  // Installed agents the cloud computer can run, Codex and Claude Code first.
  const candidates = () => {
    const order = ['codex', 'claude'];
    return (typeof detectedAgents === 'undefined' ? [] : detectedAgents)
      .filter(a => a.installed && a.cloudSupported && a.id !== 'codex-desktop')
      .sort((a, b) => ((order.indexOf(a.id) + 1) || 9) - ((order.indexOf(b.id) + 1) || 9));
  };
  const activeAgents = () => candidates().filter(a => activeTakeoverAgents.has(a.id));
  const hasSession = ids => (typeof lastSessionTerminals === 'undefined' ? [] : lastSessionTerminals)
    .some(t => t.state === 'live' && ids.includes(t.labels?.['fennec.agent'] || t.agent));

  function requestCoverage(force, active = activeAgents()) {
    const key = active.map(a => a.id).sort().join(',');
    const now = Date.now();
    if (!force && key === coverageKey && now - coverageAsked < 60_000) return;
    coverageKey = key; coverageAsked = now; checking = true;
    send(force ? 'terminal-coverage-force' : 'terminal-coverage');
  }

  function step(n, title, done, current, body) {
    const row = el('li', 'gs-step' + (done ? ' done' : '') + (current ? ' current' : ''));
    const mark = el('span', 'gs-mark', done ? '' : String(n));
    mark.setAttribute('aria-hidden', 'true');
    if (done) mark.innerHTML = '<svg viewBox="0 0 16 16"><path d="M4 8.1 6.6 10.6 11.6 5.4"/></svg>';
    const main = el('div', 'gs-main');
    const head = el('div', 'gs-title', title);
    main.append(head);
    if (current && body) main.append(body);
    row.append(mark, main);
    return row;
  }

  function stepAgents(ready) {
    const body = el('div', 'gs-body');
    const found = candidates();
    if (!found.length) {
      body.append(el('p', 'gs-note', "[[i18n:ui.gs_install_an_agent]]"));
      const links = el('div', 'gs-links');
      for (const [label, url] of [["Codex CLI", 'https://github.com/openai/codex'], ["Claude Code", 'https://github.com/anthropics/claude-code']]) {
        const a = el('button', 'gs-link', label); a.type = 'button';
        a.onclick = () => send('update-open-url:' + JSON.stringify({ url }));
        links.append(a);
      }
      body.append(links);
      return body;
    }
    body.append(el('p', 'gs-note', "[[i18n:ui.gs_pick_an_agent]]"));
    const chips = el('div', 'gs-chips');
    for (const agent of found) {
      const chip = el('button', 'gs-chip'); chip.type = 'button';
      const icon = el('img'); icon.alt = ''; icon.src = 'icons/' + agent.icon; icon.draggable = false;
      const name = el('span', '', agent.name.split(' / ')[0]);
      chip.append(icon, name);
      const route = routeResults.get(agent.id);
      const selected = takeoverAgents.has(agent.id);
      if (!ready) { chip.disabled = true; chip.title = "[[i18n:ui.gs_wait_for_cloud]]"; }
      else if (route && ['credentials_required', 'credentials_unavailable'].includes(route.status) && typeof window.startAgentLogin === 'function') {
        chip.append(el('span', 'gs-chip-note', "[[i18n:ui.gs_sign_in]]"));
        chip.title = "[[i18n:ui.gs_no_sign_in_to_copy]]";
        chip.onclick = () => window.startAgentLogin(agent.id);
      } else if (selected) {
        chip.disabled = true; chip.append(el('span', 'gs-chip-note', "[[i18n:ui.gs_connecting]]"));
      } else {
        chip.disabled = takeoverChanging || !takeoverSelectionKnown;
        chip.onclick = () => send('toggle-takeover:' + agent.id);
      }
      chips.append(chip);
    }
    body.append(chips);
    return body;
  }

  function stepTerminal(active) {
    const body = el('div', 'gs-body');
    body.append(el('p', 'gs-note', "[[i18n:ui.gs_open_new_terminal]]"));
    for (const agent of active) {
      const line = el('div', 'gs-command');
      line.append(code(commandOf(agent.id)), copyButton(commandOf(agent.id)));
      body.append(line);
    }
    if (checking && !coverage) { body.append(el('p', 'gs-note gs-checking', "[[i18n:ui.gs_checking_terminal]]")); return body; }
    if (!coverage || coverage.error) return body;
    let snippetNeeded = false;
    for (const agent of active) {
      const c = coverage.agents?.[agent.id];
      if (!c || ['covered_by_function', 'covered_by_shim', 'covered_by_entry', 'not_installed'].includes(c.coverage)) continue;
      const problem = el('p', 'gs-problem');
      if (c.coverage === 'shadowed_by_alias' || c.coverage === 'shadowed_by_function') {
        problem.append(code(commandOf(agent.id)), ' ', c.coverage === 'shadowed_by_alias' ? "[[i18n:ui.gs_is_your_alias]]" : "[[i18n:ui.gs_is_your_function]]");
      } else {
        problem.append(code(commandOf(agent.id)), ' ', "[[i18n:ui.gs_still_runs_on_mac]]");
        snippetNeeded = true;
      }
      body.append(problem);
    }
    if (snippetNeeded) {
      const fix = el('button', 'gs-fix', fixing ? "[[i18n:ui.gs_fixing]]" : "[[i18n:ui.gs_fix]]"); fix.type = 'button';
      fix.disabled = fixing;
      fix.onclick = () => { fixing = true; render(); send('terminal-fix'); };
      body.append(fix);
      if (coverage.snippet) {
        body.append(el('p', 'gs-note', "[[i18n:ui.gs_add_this_line]]"));
        const line = el('div', 'gs-command');
        line.append(code(coverage.snippet), copyButton(coverage.snippet));
        body.append(line);
      }
    }
    return body;
  }

  function render() {
    const signedIn = typeof authorized !== 'undefined' && authorized;
    const selectionKnown = typeof takeoverSelectionKnown !== 'undefined' && takeoverSelectionKnown;
    if (!signedIn) for (const name of Object.keys(lastTrue)) delete lastTrue[name];
    if (!signedIn || !prefs.known || prefs.hidden || (prefs.done && !finishing)) { card.hidden = true; shown = ''; return; }
    // Keep the confirmed completion view stable until its short display ends.
    if (prefs.done && finishing) return;
    // A cloud computer prepared before counts as ready while Fennec checks it
    // again after launch, so the agents show at once instead of seconds later.
    const stage = window.fennecPreparationStage || '';
    const ready = held('ready', stage === 'agents_installed'
      || (preparedBefore && !['failed', 'offline', 'disabled', 'credentials_required'].includes(stage)));
    const activeNow = activeAgents();
    // Only an agent that is still selected keeps step 2: removing it is the
    // user's choice, not a refresh.
    const stillSelected = lastActive.filter(a => takeoverAgents.has(a.id) && candidates().some(c => c.id === a.id));
    const running = held('running', activeNow.length > 0, stillSelected.length > 0);
    if (activeNow.length) lastActive = activeNow;
    const active = activeNow.length ? activeNow : (running ? stillSelected : []);
    const started = running && held('started', hasSession(active.map(a => a.id)));
    const doneCount = [ready, running, started].filter(Boolean).length;
    if (running && !started) requestCoverage(false, active);
    // Before the key, so starting the timer does not rebuild the card.
    if (doneCount === 3) {
      prefs.done = true;
      save({ get_started_done: true });
      if (!finishing) finishing = setTimeout(() => { finishing = null; render(); }, 8000);
    }

    // Only what the card shows: a newer coverage check with the same result
    // (checked_at) or a re-check behind a shown result changes nothing.
    const coverageShown = !coverage ? null : coverage.error ? 'error'
      : [active.map(a => coverage.agents?.[a.id]?.coverage || ''), coverage.snippet || ''];
    const key = JSON.stringify({
      ready, doneCount, finishing: !!finishing, checking: checking && !coverage, fixing, coverage: coverageShown,
      active: active.map(a => a.id),
      changing: takeoverChanging, known: selectionKnown,
      agents: candidates().map(a => [a.id, a.name, a.icon, takeoverAgents.has(a.id), routeResults.get(a.id)?.status || '']),
    });
    if (key === shown && !card.hidden) return;
    shown = key;
    card.replaceChildren();
    const head = el('div', 'gs-head');
    const title = el('h2', 'gs-heading', "[[i18n:ui.gs_get_started]]"); title.id = 'get-started-title';
    const count = el('span', 'gs-count', doneCount + ' / 3');
    const close = el('button', 'icon ios-app-close gs-close'); close.type = 'button';
    close.setAttribute('aria-label', "[[i18n:ui.gs_hide]]"); close.title = "[[i18n:ui.gs_hide]]";
    close.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="m6 6 8 8M14 6l-8 8"/></svg>';
    close.onclick = () => { prefs.hidden = true; card.hidden = true; shown = ''; save({ get_started_hidden: true }); };
    head.append(title, count, close);
    const progress = el('div', 'gs-progress');
    progress.setAttribute('aria-hidden', 'true');
    for (let i = 0; i < 3; i++) progress.append(el('span', i < doneCount ? 'on' : ''));
    const steps = el('ol', 'gs-steps');
    const prep = el('div', 'gs-body');
    prep.append(el('p', 'gs-note', "[[i18n:ui.gs_preparing]]"));
    steps.append(
      step(1, "[[i18n:ui.gs_cloud_ready]]", ready, !ready, prep),
      step(2, "[[i18n:ui.gs_run_on_cloud]]", running, ready && !running, stepAgents(ready)),
      step(3, "[[i18n:ui.gs_start_from_terminal]]", started, running && !started, stepTerminal(active)),
    );
    const phone = el('button', 'gs-phone'); phone.type = 'button';
    phone.onclick = () => document.querySelector('#get-ios-app')?.click();
    const phoneIcon = el('span', 'gs-phone-icon'); phoneIcon.setAttribute('aria-hidden', 'true');
    phoneIcon.innerHTML = '<svg viewBox="0 0 16 16"><rect x="4.5" y="1.75" width="7" height="12.5" rx="1.75"/><path d="M7 11.75h2"/></svg>';
    const chevron = el('span', 'gs-chevron'); chevron.setAttribute('aria-hidden', 'true');
    chevron.innerHTML = '<svg viewBox="0 0 16 16"><path d="m6 4 4 4-4 4"/></svg>';
    phone.append(phoneIcon, el('span', 'gs-phone-title', "[[i18n:ui.gs_iphone]]"), el('span', 'gs-tag', "[[i18n:ui.gs_optional]]"), chevron);
    const iPhoneAvailable = typeof selfHosted === 'undefined' || !selfHosted;
    card.append(head, progress, steps);
    // A fresh cloud computer (no agent has run on it yet) can start from a
    // backup of the old one instead (THI-188).
    if (!running && typeof window.openArchiveImport === 'function') {
      const restore = el('button', 'gs-secondary', "[[i18n:ui.gs_import_backup]]"); restore.type = 'button';
      restore.onclick = () => window.openArchiveImport();
      card.append(restore);
    }
    if (iPhoneAvailable) card.append(phone);
    if (doneCount === 3) card.append(el('p', 'gs-finished', "[[i18n:ui.gs_detach_hint]]"));
    card.hidden = false;
  }

  const after = name => {
    const original = window[name];
    if (typeof original !== 'function') return;
    window[name] = (...args) => { const out = original(...args); render(); return out; };
  };
  for (const name of ['renderAgents', 'renderTakeover', 'renderHerdrSessions', 'renderRouteResults', 'renderAuthorized', 'renderSignedOut']) after(name);
  const preparation = window.renderPreparation;
  if (typeof preparation === 'function') {
    window.renderPreparation = state => { const out = preparation(state); preparedBefore = state?.prepared_before === 'true'; render(); return out; };
  }
  const preferences = window.renderPreferences;
  window.renderPreferences = value => {
    preferences(value);
    prefs.known = true;
    // A value this card saved wins until the preferences echo it back.
    for (const [name, field] of [['hidden', 'get_started_hidden'], ['done', 'get_started_done']]) {
      if (field in saving && (value[field] === true) === saving[field]) delete saving[field];
      if (field in saving) prefs[name] = saving[field];
      else prefs[name] = prefs[name] || value[field] === true;
    }
    render();
  };
  window.renderTerminalCoverage = value => {
    checking = false; fixing = false;
    coverage = value || { error: true };
    render();
  };
})();
