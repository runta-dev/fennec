// Get started (docs/get-started.md): a checklist above the Agents list that
// ends with the user's own agent running on the cloud computer from their
// usual terminal. Every tick is computed from current state, never from a
// click, so a step undoes itself when its condition stops holding. Nothing is
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
  const save = value => send('settings:' + JSON.stringify(value));

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

  function requestCoverage(force) {
    const key = activeAgents().map(a => a.id).sort().join(',');
    const now = Date.now();
    if (!force && key === coverageKey && now - coverageAsked < 60_000) return;
    coverageKey = key; coverageAsked = now; checking = true;
    send(force ? 'terminal-coverage-force' : 'terminal-coverage');
  }

  function step(n, title, done, current, body) {
    const row = el('li', 'gs-step' + (done ? ' done' : '') + (current ? ' current' : ''));
    const mark = el('span', 'gs-mark', done ? '' : String(n));
    mark.setAttribute('aria-hidden', 'true');
    if (done) mark.innerHTML = '<svg viewBox="0 0 16 16"><path d="m3.5 8.5 3 3 6-7"/></svg>';
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

  function stepTerminal() {
    const body = el('div', 'gs-body');
    const active = activeAgents();
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
    // Finished, then nothing selected any more: the card comes back.
    if (prefs.known && prefs.done && selectionKnown && takeoverAgents.size === 0) { prefs.done = false; save({ get_started_done: false }); }
    if (!signedIn || !prefs.known || prefs.hidden || (prefs.done && !finishing)) { card.hidden = true; shown = ''; return; }
    const ready = window.fennecPreparationStage === 'agents_installed';
    const active = activeAgents();
    const running = active.length > 0;
    const started = running && hasSession(active.map(a => a.id));
    const doneCount = [ready, running, started].filter(Boolean).length;
    if (running && !started) requestCoverage(false);

    const key = JSON.stringify({
      ready, doneCount, finishing: !!finishing, checking, fixing, coverage,
      changing: takeoverChanging, known: selectionKnown,
      agents: candidates().map(a => [a.id, a.name, a.icon, takeoverAgents.has(a.id), activeTakeoverAgents.has(a.id), routeResults.get(a.id)?.status || '']),
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
    const steps = el('ol', 'gs-steps');
    const prep = el('div', 'gs-body');
    prep.append(el('p', 'gs-note', "[[i18n:ui.gs_preparing]]"));
    steps.append(
      step(1, "[[i18n:ui.gs_cloud_ready]]", ready, !ready, prep),
      step(2, "[[i18n:ui.gs_run_on_cloud]]", running, ready && !running, stepAgents(ready)),
      step(3, "[[i18n:ui.gs_start_from_terminal]]", started, running && !started, stepTerminal()),
    );
    const phone = el('li', 'gs-step optional');
    const phoneButton = el('button', 'gs-link', "[[i18n:ui.gs_iphone]]"); phoneButton.type = 'button';
    phoneButton.onclick = () => document.querySelector('#get-ios-app')?.click();
    phone.append(el('span', 'gs-mark', ''), el('div', 'gs-main'));
    phone.lastChild.append(el('div', 'gs-title gs-optional', "[[i18n:ui.gs_optional]]"), phoneButton);
    steps.append(phone);
    card.append(head, steps);
    if (doneCount === 3) {
      card.append(el('p', 'gs-finished', "[[i18n:ui.gs_detach_hint]]"));
      if (!finishing) finishing = setTimeout(() => { prefs.done = true; save({ get_started_done: true }); finishing = null; render(); }, 8000);
    } else if (finishing) { clearTimeout(finishing); finishing = null; }
    card.hidden = false;
  }

  const after = name => {
    const original = window[name];
    if (typeof original !== 'function') return;
    window[name] = (...args) => { const out = original(...args); render(); return out; };
  };
  for (const name of ['renderAgents', 'renderTakeover', 'renderHerdrSessions', 'renderPreparation', 'renderRouteResults', 'renderAuthorized', 'renderSignedOut']) after(name);
  const preferences = window.renderPreferences;
  window.renderPreferences = value => {
    preferences(value);
    prefs.known = true;
    prefs.hidden = value.get_started_hidden === true;
    if (!finishing) prefs.done = value.get_started_done === true;
    render();
  };
  window.renderTerminalCoverage = value => {
    checking = false; fixing = false;
    coverage = value || { error: true };
    render();
  };
})();
