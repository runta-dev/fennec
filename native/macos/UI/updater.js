// Magpie-style quiet header update pill, release-notes dialog, and Settings version bar.
(() => {
  const pill = document.querySelector('#update-pill');
  const pillLabel = document.querySelector('#update-pill-label');
  const hideBtn = document.querySelector('#update-hide');
  const autoSwitch = document.querySelector('#auto-update');
  const versionBox = document.querySelector('.settings-version');
  const versionNumber = document.querySelector('.settings-version-number');
  const versionSub = document.querySelector('.settings-version-sub');
  const versionActions = document.querySelector('.settings-version-actions');

  let currentUpdate = { state: '', current: '' };
  let autoUpdateEnabled = true;
  let skippedVersion = '';
  let actionPending = false;
  let restarting = false;

  const t = token => (typeof translate === 'function' ? translate(token) : token);

  const dialog = document.createElement('dialog');
  dialog.id = 'update-dialog';
  dialog.className = 'update-dialog';
  dialog.setAttribute('aria-labelledby', 'update-dialog-title');
  dialog.innerHTML =
    '<div class="update-dialog-head"><strong id="update-dialog-title"></strong><span class="update-dialog-version"></span></div>' +
    '<div class="update-dialog-notes"></div>' +
    '<p class="update-dialog-hint"></p>' +
    '<div class="update-dialog-actions">' +
    '<button type="button" class="update-dialog-cancel"></button>' +
    '<button type="button" class="update-dialog-confirm"></button>' +
    '</div>';
  document.body?.append(dialog);

  const dialogTitle = dialog.querySelector('#update-dialog-title');
  const dialogVersion = dialog.querySelector('.update-dialog-version');
  const dialogNotes = dialog.querySelector('.update-dialog-notes');
  const dialogHint = dialog.querySelector('.update-dialog-hint');
  const dialogCancel = dialog.querySelector('.update-dialog-cancel');
  const dialogConfirm = dialog.querySelector('.update-dialog-confirm');

  dialogCancel.onclick = () => { if (!restarting) dialog.close(); };
  dialog.addEventListener('cancel', event => { if (restarting) event.preventDefault(); });

  function formatSpeed(bytesPerSec) {
    if (!bytesPerSec || bytesPerSec <= 0) return '';
    if (bytesPerSec >= 1e6) return (bytesPerSec / 1e6).toFixed(1) + ' MB/s';
    return Math.max(1, Math.round(bytesPerSec / 1e3)) + ' KB/s';
  }

  function stepText(u) {
    const steps = {
      retry: '[[i18n:ui.reconnecting]]',
      verify: '[[i18n:ui.verifying]]',
      unpack: '[[i18n:ui.unpacking]]',
      signature: '[[i18n:ui.checking_signature]]'
    };
    if (steps[u.step] && u.step !== 'retry') return t(steps[u.step]);
    const pct = u.total > 0 ? Math.min(100, Math.max(0, Math.floor(((u.done || 0) / u.total) * 100))) : null;
    let text = t('[[i18n:ui.downloading]]');
    if (pct !== null) text = text.replace(/…$/, '') + ' ' + pct + '%';
    if (u.step === 'retry') text += ' · ' + t('[[i18n:ui.reconnecting]]');
    else if (u.speed > 0) text += ' · ' + formatSpeed(u.speed);
    return text;
  }

  function appendInlineMarkdown(parent, rawText) {
    const cleaned = String(rawText || '').replace(/[.。]$/, '');
    const tokenRe = /`([^`]+)`|\[([^\]]+)\]\((https:\/\/[^\s)]+)\)/g;
    let cursor = 0;
    let match;
    while ((match = tokenRe.exec(cleaned)) !== null) {
      if (match.index > cursor) {
        parent.append(document.createTextNode(cleaned.slice(cursor, match.index)));
      }
      if (match[1] !== undefined) {
        const code = document.createElement('code');
        code.textContent = match[1];
        parent.append(code);
      } else {
        const url = match[3];
        const link = document.createElement('button');
        link.type = 'button';
        link.className = 'update-note-link';
        link.textContent = match[2];
        link.onclick = () => send('update-open-url:' + JSON.stringify({ url }));
        parent.append(link);
      }
      cursor = match.index + match[0].length;
    }
    if (cursor < cleaned.length) {
      parent.append(document.createTextNode(cleaned.slice(cursor)));
    }
  }

  function renderMarkdownNotes(container, md) {
    container.replaceChildren();
    const raw = String(md || '').trim();
    if (!raw) {
      container.hidden = true;
      return;
    }
    container.hidden = false;
    let list = null;
    for (const line of raw.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed) { list = null; continue; }
      const heading = /^#{1,6}\s+(.*?)\s*#*\s*$/.exec(trimmed);
      const bullet = /^[-*+]\s+(.*)$/.exec(trimmed);
      if (heading) {
        list = null;
        const h = document.createElement('div');
        h.className = 'update-note-heading';
        appendInlineMarkdown(h, heading[1]);
        container.append(h);
      } else if (bullet) {
        if (!list) { list = document.createElement('ul'); container.append(list); }
        const li = document.createElement('li');
        appendInlineMarkdown(li, bullet[1]);
        list.append(li);
      } else {
        list = null;
        const p = document.createElement('p');
        appendInlineMarkdown(p, trimmed);
        container.append(p);
      }
    }
  }

  function triggerInstall() {
    if (restarting) return;
    restarting = true;
    dialogConfirm.disabled = true;
    dialogCancel.disabled = true;
    dialogConfirm.textContent = t('[[i18n:ui.restarting]]');
    draw();
    send('update-install');
  }

  function openConfirmDialog() {
    if (!currentUpdate.latest) return;
    dialogTitle.textContent = t('[[i18n:ui.restart_to_update]]');
    dialogVersion.textContent = (currentUpdate.current ? currentUpdate.current + ' → ' : '') + currentUpdate.latest;
    renderMarkdownNotes(dialogNotes, currentUpdate.notes);
    dialogHint.textContent = t('[[i18n:ui.update_keeps_sessions]]');
    dialogCancel.textContent = t('[[i18n:ui.cloud_cancel]]');
    dialogConfirm.textContent = t('[[i18n:ui.restart_to_update]]');
    dialogCancel.disabled = false;
    dialogConfirm.disabled = false;
    dialogConfirm.onclick = triggerInstall;
    if (!dialog.open && typeof dialog.showModal === 'function') dialog.showModal();
    dialogConfirm.focus();
  }

  function drawPill() {
    if (!pill || !pillLabel) return;
    const u = currentUpdate;
    const showStates = ['ready', 'found', 'downloading'];
    const hiddenBySkip = !!u.latest && skippedVersion === u.latest && u.state !== 'downloading' && !restarting;
    const visible = showStates.includes(u.state) && !hiddenBySkip;
    pill.hidden = !visible;
    pill.classList.toggle('busy', u.state === 'downloading' || actionPending || restarting);
    if (!visible) return;

    if (restarting) {
      pillLabel.textContent = t('[[i18n:ui.restarting]]');
      pill.title = pillLabel.textContent;
      return;
    }
    if (u.state === 'downloading') {
      const progress = stepText(u);
      const pct = u.total > 0 ? Math.min(100, Math.max(0, Math.floor(((u.done || 0) / u.total) * 100))) : null;
      pillLabel.textContent = pct !== null ? pct + '%' : t('[[i18n:ui.downloading]]');
      pill.title = progress;
      return;
    }
    pillLabel.textContent = u.state === 'ready' && u.error ? t('[[i18n:ui.update_failed]]') : t('[[i18n:ui.update]]');
    pill.title = u.state === 'ready'
      ? t('[[i18n:ui.restart_to_update]]') + ' · ' + u.latest
      : u.latest;
    pill.onclick = () => {
      if (restarting || actionPending) return;
      if (u.state === 'ready') {
        openConfirmDialog();
      } else if (u.state === 'found') {
        actionPending = true;
        draw();
        send('update-download');
      }
    };
  }

  function drawSettingsBar() {
    if (!versionBox || !versionNumber || !versionSub || !versionActions) return;
    const u = currentUpdate;
    if (u.current && u.current !== 'dev') {
      versionNumber.textContent = 'Fennec ' + u.current;
    }
    versionActions.replaceChildren();
    const addBtn = (label, onClick, disabled = false, primary = false) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'version-action' + (primary ? ' is-primary' : '');
      b.textContent = label;
      b.disabled = disabled;
      b.onclick = onClick;
      versionActions.append(b);
      return b;
    };
    const checkNow = () => {
      if (actionPending || restarting || u.state === 'checking' || u.state === 'downloading') return;
      actionPending = true;
      currentUpdate = { ...currentUpdate, state: 'checking', error: '' };
      draw();
      send('update-check');
    };

    if (restarting) {
      versionSub.textContent = ' · ' + t('[[i18n:ui.restarting]]');
      addBtn(t('[[i18n:ui.restarting]]'), () => {}, true, true);
      return;
    }

    switch (u.state) {
      case 'checking':
        versionSub.textContent = ' · ' + t('[[i18n:ui.checking_for_updates]]');
        addBtn(t('[[i18n:ui.check]]'), checkNow, true);
        break;
      case 'downloading':
        versionSub.textContent = ' · ' + stepText(u);
        break;
      case 'ready':
        versionSub.textContent = ' · ' + u.latest + ' ' + t('[[i18n:ui.update_ready]]');
        addBtn(t('[[i18n:ui.restart_to_update]]'), openConfirmDialog, false, true);
        break;
      case 'found':
        versionSub.textContent = ' · ' + u.latest + (u.size > 0 ? ' (' + (u.size / 1e6).toFixed(1) + ' MB)' : '');
        addBtn(t('[[i18n:ui.download]]'), () => {
          actionPending = true;
          draw();
          send('update-download');
        }, actionPending, true);
        break;
      case 'available':
        versionSub.textContent = u.latest ? ' · ' + u.latest + ' ' + t('[[i18n:ui.update_available]]') : '';
        if (u.url) {
          addBtn(t('[[i18n:ui.download]]'), () => send('update-open-url:' + JSON.stringify({ url: u.url })));
        } else {
          addBtn(t('[[i18n:ui.check]]'), checkNow, actionPending);
        }
        break;
      case 'latest':
        versionSub.textContent = ' · ' + t('[[i18n:ui.up_to_date]]');
        addBtn(t('[[i18n:ui.check]]'), checkNow, actionPending);
        break;
      case 'error':
        versionSub.textContent = ' · ' + (u.latest ? t('[[i18n:ui.update_download_failed]]') : t('[[i18n:ui.update_check_failed]]'));
        addBtn(t('[[i18n:ui.check]]'), checkNow, actionPending);
        break;
      case 'source':
        versionSub.textContent = '';
        addBtn(t('[[i18n:ui.check]]'), checkNow, actionPending);
        break;
      default:
        versionSub.textContent = !autoUpdateEnabled ? ' · ' + t('[[i18n:ui.automatic_updates_off]]') : '';
        addBtn(t('[[i18n:ui.check]]'), checkNow, actionPending);
        break;
    }
  }

  function draw() {
    if (autoSwitch) autoSwitch.setAttribute('aria-checked', String(autoUpdateEnabled));
    drawPill();
    drawSettingsBar();
  }

  if (hideBtn) {
    hideBtn.onclick = event => {
      event.stopPropagation();
      if (!currentUpdate.latest) return;
      skippedVersion = currentUpdate.latest;
      drawPill();
      send('settings:' + JSON.stringify({ update_skip: skippedVersion }));
    };
  }

  if (autoSwitch) {
    autoSwitch.onclick = () => {
      autoUpdateEnabled = !autoUpdateEnabled;
      draw();
      send('settings:' + JSON.stringify({ auto_update: autoUpdateEnabled }));
    };
  }

  window.renderAppVersion = text => {
    if (versionNumber && text) versionNumber.textContent = text;
  };

  window.renderUpdatePreferences = prefs => {
    if (!prefs) return;
    autoUpdateEnabled = prefs.no_auto_update !== true;
    skippedVersion = typeof prefs.update_skip === 'string' ? prefs.update_skip : '';
    draw();
  };

  window.renderUpdateState = state => {
    actionPending = false;
    if (!state || typeof state !== 'object') return;
    currentUpdate = state;
    draw();
  };

  window.renderUpdateInstallFailed = () => {
    restarting = false;
    actionPending = false;
    if (dialog.open) {
      dialogConfirm.disabled = false;
      dialogCancel.disabled = false;
      dialogConfirm.textContent = t('[[i18n:ui.restart_to_update]]');
    }
    currentUpdate = { ...currentUpdate, error: 'install_failed' };
    draw();
  };

  window.rerenderUpdater = draw;
  draw();
})();
