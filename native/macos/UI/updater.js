// Magpie-style quiet header update pill and Settings version bar. One click
// updates: no confirmation dialog.
(() => {
  const pill = document.querySelector('#update-pill');
  const pillLabel = document.querySelector('#update-pill-label');
  const autoSwitch = document.querySelector('#auto-update');
  const versionBox = document.querySelector('.settings-version');
  const versionNumber = document.querySelector('.settings-version-number');
  const versionSub = document.querySelector('.settings-version-sub');
  const versionActions = document.querySelector('.settings-version-actions');

  let currentUpdate = { state: '', current: '' };
  let autoUpdateEnabled = true;
  let actionPending = false;
  let restarting = false;
  // Set by a click on an update that is still downloading: restart into it
  // as soon as it is ready.
  let installWhenReady = false;

  const t = token => (typeof translate === 'function' ? translate(token) : token);

  function formatSpeed(bytesPerSec) {
    if (!bytesPerSec || bytesPerSec <= 0) return '';
    if (bytesPerSec >= 1e6) return (bytesPerSec / 1e6).toFixed(1) + ' MB/s';
    return Math.max(1, Math.round(bytesPerSec / 1e3)) + ' KB/s';
  }

  function stepText(u) {
    const steps = {
      retry: '[[i18n:ui.reconnecting]]',
      verify: '[[i18n:ui.verifying]]',
      // Cloning and checking an installed component the update reuses.
      reuse: '[[i18n:ui.verifying]]',
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

  // install downloads the update when needed and restarts into it.
  function install() {
    if (restarting || actionPending) return;
    const u = currentUpdate;
    if (u.state === 'ready') {
      installWhenReady = false;
      restarting = true;
      draw();
      send('update-install');
    } else if (u.state === 'found' || u.state === 'downloading') {
      installWhenReady = true;
      if (u.state === 'found') {
        actionPending = true;
        send('update-download');
      }
      draw();
    }
  }

  function drawPill() {
    if (!pill || !pillLabel) return;
    const u = currentUpdate;
    const showStates = ['ready', 'found', 'downloading'];
    const visible = showStates.includes(u.state);
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
    pill.onclick = install;
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
        addBtn(t('[[i18n:ui.restart_to_update]]'), install, false, true);
        break;
      case 'found':
        versionSub.textContent = ' · ' + u.latest + (u.size > 0 ? ' (' + (u.size / 1e6).toFixed(1) + ' MB)' : '');
        addBtn(t('[[i18n:ui.update]]'), install, actionPending, true);
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
    draw();
  };

  window.renderUpdateState = state => {
    actionPending = false;
    if (!state || typeof state !== 'object') return;
    currentUpdate = state;
    if (state.state === 'error' || state.state === 'latest') installWhenReady = false;
    if (installWhenReady && state.state === 'ready' && !state.error) {
      install();
      return;
    }
    draw();
  };

  window.renderUpdateInstallFailed = () => {
    restarting = false;
    actionPending = false;
    installWhenReady = false;
    currentUpdate = { ...currentUpdate, error: 'install_failed' };
    draw();
  };

  window.rerenderUpdater = draw;
  draw();
})();
