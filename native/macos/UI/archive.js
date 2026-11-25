// Settings › Cloud computer: export the cloud computer's queues and sessions to
// a file, and import such a file (THI-188; the work itself is THI-165's
// cloud.archive.* daemon methods). Swift owns the file panels and the control
// calls; this file draws the dialog and polls the job while it runs.
(() => {
  const dialog = document.createElement('dialog');
  dialog.id = 'archive-dialog';
  dialog.className = 'developer-dialog archive-dialog';
  dialog.setAttribute('aria-labelledby', 'archive-title');
  document.body.append(dialog);

  // Copy with {name} placeholders filled (translate() is locale.js's).
  const tr = (source, values = {}) => Object.entries(values)
    .reduce((text, [name, value]) => text.split('{' + name + '}').join(String(value)), translate(source));
  const el = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };
  const size = bytes => {
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let value = Math.max(0, bytes || 0), unit = 0;
    while (value >= 1000 && unit < units.length - 1) { value /= 1000; unit++; }
    return (unit === 0 || value >= 100 ? Math.round(value) : value.toFixed(1)) + ' ' + units[unit];
  };
  const contents = counts => tr('[[i18n:ui.archive_queues_detail]]', { queues: counts.projects || 0, requests: counts.requests || 0, knowledge: counts.knowledge || 0 });

  let state = { mode: '', phase: '' };
  let poll = null;
  const stopPolling = () => { clearInterval(poll); poll = null; };
  const startPolling = () => { if (!poll) poll = setInterval(() => send('archive-status'), 700); };

  function button(text, onclick, primary) {
    const b = el('button', primary ? 'archive-primary' : '', text);
    b.type = 'button';
    b.onclick = onclick;
    return b;
  }
  function progressBar(done, total) {
    const bar = el('div', 'archive-progress');
    const fill = el('span');
    if (total > 0) fill.style.width = Math.min(100, Math.round(done / total * 100)) + '%';
    else bar.classList.add('indeterminate');
    bar.append(fill);
    return bar;
  }
  function failure(message, detail) {
    const box = el('div', 'archive-failure');
    box.append(el('strong', '', message));
    if (detail) box.append(el('p', 'archive-detail', detail));
    return box;
  }

  function render() {
    const head = el('div', 'developer-dialog-head');
    const title = el('strong', '', state.mode === 'export' ? translate('[[i18n:ui.archive_export_title]]') : translate('[[i18n:ui.archive_import_title]]'));
    title.id = 'archive-title';
    const close = el('button', 'icon ios-app-close');
    close.type = 'button';
    close.setAttribute('aria-label', translate('[[i18n:ui.close]]'));
    close.innerHTML = '<svg viewBox="0 0 20 20" aria-hidden="true"><path d="m6 6 8 8M14 6l-8 8"/></svg>';
    close.onclick = () => dialog.close();
    head.append(title, close);
    const body = el('div', 'archive-body');
    const actions = el('div', 'archive-actions');
    (state.mode === 'export' ? renderExport : renderImport)(body, actions);
    dialog.replaceChildren(head, body);
    if (actions.childElementCount) dialog.append(actions);
  }

  function renderExport(body, actions) {
    const { phase, plan, job } = state;
    if (phase === 'opening' || phase === 'planning') {
      body.append(el('p', 'archive-note', translate('[[i18n:ui.archive_counting]]')), progressBar(0, 0));
      actions.append(button(translate('[[i18n:ui.archive_cancel]]'), () => dialog.close()));
      return;
    }
    if (phase === 'ready') {
      const list = el('div', 'archive-items card');
      const queues = el('div', 'archive-item');
      const queuesText = el('div', 'archive-item-text');
      queuesText.append(el('span', 'archive-item-title', translate('[[i18n:ui.archive_queues]]')));
      if (plan) queuesText.append(el('span', 'archive-item-detail', contents(plan.queues.counts)));
      queues.append(queuesText);
      if (plan) queues.append(el('span', 'archive-size', size(plan.queues.counts.bytes)));
      const sessions = el('label', 'archive-item');
      const check = el('input');
      check.type = 'checkbox';
      check.checked = state.sessions;
      check.onchange = () => { state.sessions = check.checked; render(); };
      const sessionsText = el('div', 'archive-item-text');
      sessionsText.append(el('span', 'archive-item-title', translate('[[i18n:ui.archive_include_sessions]]')));
      if (plan) sessionsText.append(el('span', 'archive-item-detail', tr('[[i18n:ui.archive_sessions_detail]]', { sessions: plan.all.counts.sessions || 0 })));
      sessions.append(check, sessionsText);
      if (plan) sessions.append(el('span', 'archive-size', size(plan.all.counts.bytes - plan.queues.counts.bytes)));
      list.append(queues, sessions);
      body.append(list);
      body.append(el('p', 'archive-total', plan
        ? tr('[[i18n:ui.archive_size]]', { size: size(state.sessions ? plan.all.counts.bytes : plan.queues.counts.bytes) })
        : translate('[[i18n:ui.archive_plan_unavailable]]')));
      body.append(el('p', 'archive-note', translate('[[i18n:ui.archive_not_included]]')));
      body.append(el('p', 'archive-note archive-warning', translate('[[i18n:ui.archive_keep_safe]]')));
      actions.append(button(translate('[[i18n:ui.archive_cancel]]'), () => dialog.close()),
        button(translate('[[i18n:ui.archive_export_button]]'), () => {
          state.phase = 'running'; state.job = null; render();
          send('archive-export:' + (state.sessions ? 'sessions' : 'queues'));
        }, true));
      return;
    }
    if (phase === 'running') {
      const done = job?.bytes || 0, total = job?.total || 0;
      body.append(el('p', 'archive-note', total ? tr('[[i18n:ui.archive_exporting]]', { done: size(done), total: '~' + size(total) }) : translate('[[i18n:ui.archive_preparing]]')), progressBar(done, total));
      if (job?.manifest) body.append(el('p', 'archive-detail', contents(job.manifest.counts) + (job.manifest.sessions ? ' · ' + tr('[[i18n:ui.archive_sessions_detail]]', { sessions: job.manifest.counts.sessions || 0 }) : '')));
      actions.append(button(translate('[[i18n:ui.archive_cancel]]'), () => send('archive-cancel')));
      return;
    }
    if (phase === 'saving') {
      body.append(el('p', 'archive-note', translate('[[i18n:ui.archive_choose_location]]')));
      return;
    }
    if (phase === 'saved') body.append(el('p', 'archive-success', translate('[[i18n:ui.archive_saved]]')));
    if (phase === 'cancelled') body.append(el('p', 'archive-note', translate('[[i18n:ui.archive_cancelled]]')));
    if (phase === 'failed') body.append(failure(translate('[[i18n:ui.archive_export_failed]]'), state.error));
    if (phase === 'failed' || phase === 'cancelled') actions.append(button(translate('[[i18n:ui.archive_try_again]]'), () => openExport(), false));
    actions.append(button(translate('[[i18n:ui.archive_done]]'), () => dialog.close(), true));
  }

  function renderImport(body, actions) {
    const { phase, manifest, job } = state;
    if (phase === 'opening' || phase === 'choose' || phase === 'reading') {
      body.append(el('p', 'archive-note', phase === 'reading' ? translate('[[i18n:ui.archive_reading]]') : translate('[[i18n:ui.archive_choose_hint]]')));
      if (state.error) body.append(failure(state.error));
      actions.append(button(translate('[[i18n:ui.archive_cancel]]'), () => dialog.close()));
      if (phase !== 'reading') actions.append(button(translate('[[i18n:ui.archive_choose_file]]'), () => { state.error = ''; send('archive-choose'); }, true));
      return;
    }
    if (phase === 'preview') {
      const source = manifest.source_home === '/root' ? translate('[[i18n:ui.archive_source_runta]]') : translate('[[i18n:ui.archive_source_self_hosted]]');
      const created = new Date(manifest.created);
      const time = isNaN(created) ? '' : created.toLocaleString(document.documentElement.lang || undefined, { dateStyle: 'medium', timeStyle: 'short' });
      body.append(el('p', 'archive-file', state.name || ''));
      body.append(el('p', 'archive-note', tr('[[i18n:ui.archive_from]]', { source, time })));
      const list = el('div', 'archive-items card');
      const queues = el('div', 'archive-item');
      const queuesText = el('div', 'archive-item-text');
      queuesText.append(el('span', 'archive-item-title', translate('[[i18n:ui.archive_queues]]')), el('span', 'archive-item-detail', contents(manifest.counts)));
      queues.append(queuesText);
      const sessions = el('div', 'archive-item');
      const sessionsText = el('div', 'archive-item-text');
      sessionsText.append(el('span', 'archive-item-title', manifest.sessions ? tr('[[i18n:ui.archive_sessions_detail]]', { sessions: manifest.counts.sessions || 0 }) : translate('[[i18n:ui.archive_no_sessions]]')));
      sessions.append(sessionsText);
      list.append(queues, sessions);
      body.append(list, el('p', 'archive-note', translate('[[i18n:ui.archive_merge_note]]')));
      actions.append(button(translate('[[i18n:ui.archive_choose_file]]'), () => send('archive-choose')),
        button(translate('[[i18n:ui.archive_import_button]]'), () => {
          state.phase = 'running'; state.job = null; render();
          send('archive-import');
        }, true));
      return;
    }
    if (phase === 'running') {
      const done = job?.bytes || 0, total = job?.total || 0;
      const uploaded = total > 0 && done >= total;
      body.append(el('p', 'archive-note', uploaded ? translate('[[i18n:ui.archive_merging]]') : total ? tr('[[i18n:ui.archive_uploading]]', { done: size(done), total: size(total) }) : translate('[[i18n:ui.archive_preparing]]')),
        progressBar(uploaded ? 0 : done, uploaded ? 0 : total));
      actions.append(button(translate('[[i18n:ui.archive_cancel]]'), () => send('archive-cancel')));
      return;
    }
    if (phase === 'done') {
      const result = job?.result || {};
      body.append(el('p', 'archive-success', translate('[[i18n:ui.archive_import_done]]')));
      const list = el('ul', 'archive-results');
      list.append(el('li', '', tr('[[i18n:ui.archive_result_requests]]', { n: result.requests_added || 0 })));
      if (result.requests_existing) list.append(el('li', '', tr('[[i18n:ui.archive_result_existing]]', { n: result.requests_existing })));
      list.append(el('li', '', tr('[[i18n:ui.archive_result_sessions]]', { n: result.sessions_restored || 0 })));
      const conflicts = result.knowledge_conflicts || [];
      if (conflicts.length) list.append(el('li', '', tr('[[i18n:ui.archive_result_conflicts]]', { n: conflicts.length })));
      body.append(list);
    }
    if (phase === 'cancelled') body.append(el('p', 'archive-note', translate('[[i18n:ui.archive_cancelled]]')));
    if (phase === 'failed') body.append(failure(translate('[[i18n:ui.archive_import_failed]]'), state.error));
    if (phase === 'failed' || phase === 'cancelled') actions.append(button(translate('[[i18n:ui.archive_try_again]]'), () => openImport()));
    actions.append(button(translate('[[i18n:ui.archive_done]]'), () => dialog.close(), true));
  }

  function open(mode) {
    stopPolling();
    state = { mode, phase: 'opening', sessions: true };
    render();
    if (!dialog.open) dialog.showModal();
    // A job started earlier (or from the command line) shows its progress.
    send('archive-status');
  }
  const openExport = () => open('export');
  const openImport = () => open('import');
  window.openArchiveExport = openExport;
  window.openArchiveImport = openImport;
  dialog.addEventListener('close', () => { stopPolling(); state = { mode: '', phase: '' }; });

  window.renderArchiveStatus = job => {
    if (!state.mode) return;
    if (state.phase === 'opening') {
      if (job?.state === 'running') {
        state.mode = job.kind === 'export' ? 'export' : 'import';
        state.phase = 'running';
      } else if (state.mode === 'export') {
        state.phase = 'planning';
        send('archive-plan');
      } else {
        state.phase = 'choose';
      }
    }
    if (state.phase !== 'running') { render(); return; }
    if (!job || job.error_code) {
      state.phase = 'failed'; state.error = job?.message || '';
    } else if (job.state === 'running') {
      state.job = job; startPolling();
    } else if (job.state === 'done') {
      state.job = job;
      if (job.kind === 'export') { state.phase = 'saving'; send('archive-save'); } else state.phase = 'done';
    } else if (job.state === 'cancelled') {
      state.phase = 'cancelled';
    } else if (job.state === 'failed') {
      state.phase = 'failed'; state.error = job.error || '';
    }
    if (state.phase !== 'running') stopPolling();
    render();
  };
  // Starting a job failed before it ran (busy, signed out, not connected).
  window.renderArchiveStartFailed = (code, message) => {
    if (!state.mode) return;
    state.phase = 'failed';
    state.error = code === 'busy' ? translate('[[i18n:ui.archive_busy]]') : message || '';
    render();
  };
  window.renderArchivePlan = plan => {
    if (state.mode !== 'export' || state.phase !== 'planning') return;
    state.plan = plan && plan.all && plan.queues ? plan : null;
    state.phase = 'ready';
    render();
  };
  // Swift chose a file (or the user dismissed the panel) and read its manifest.
  window.renderArchiveInspect = (outcome, name, manifest) => {
    if (state.mode !== 'import') return;
    if (outcome === 'reading') { state.phase = 'reading'; state.error = ''; }
    else if (outcome === 'ok' && manifest) { state.phase = 'preview'; state.manifest = manifest; state.name = name; }
    else if (outcome === 'invalid') { state.phase = 'choose'; state.error = translate('[[i18n:ui.archive_not_a_backup]]'); }
    else if (state.phase === 'reading') state.phase = 'choose';
    render();
  };
  window.renderArchiveSaved = outcome => {
    if (state.mode !== 'export') return;
    state.phase = outcome === 'saved' ? 'saved' : outcome === 'cancelled' ? 'cancelled' : 'failed';
    if (outcome === 'failed') state.error = '';
    render();
  };

  // The whole row (title and description) is the target, and it highlights
  // on hover like the other Settings rows.
  for (const entry of document.querySelectorAll('#archive-card .archive-entry')) {
    entry.addEventListener('click', entry.dataset.archive === 'export' ? openExport : openImport);
  }
})();
