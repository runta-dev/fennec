// Use your own server: deploy the Fennec cloud image on a server over the
// user's SSH and pair with it, or pair with one already running. The daemon
// runs the work (byoc.start) and the panel follows it through byoc.status.
// Errors arrive as codes; the copy below explains them, never raw SSH output.
(() => {
  const dialog = document.createElement('dialog');
  dialog.id = 'byoc-dialog';
  dialog.className = 'developer-dialog byoc-dialog';
  dialog.tabIndex = -1;
  dialog.setAttribute('aria-labelledby', 'byoc-title');
  dialog.innerHTML =
    '<div class="developer-dialog-head"><strong id="byoc-title">[[i18n:ui.use_your_own_server]]</strong>' +
    '<button type="button" class="developer-close" id="byoc-close" aria-label="[[i18n:ui.close]]">×</button></div>' +
    '<p class="byoc-intro">[[i18n:ui.byoc_intro]]</p>' +
    '<div class="developer-segments byoc-modes" role="radiogroup" aria-label="[[i18n:ui.use_your_own_server]]">' +
    '<button type="button" role="radio" data-mode="deploy" aria-checked="true">[[i18n:ui.byoc_mode_deploy]]</button>' +
    '<button type="button" role="radio" data-mode="pair" aria-checked="false">[[i18n:ui.byoc_mode_pair]]</button>' +
    '</div>' +
    '<form class="byoc-form" id="byoc-deploy" autocomplete="off">' +
    '<label class="byoc-field"><span>[[i18n:ui.byoc_ssh_destination]]</span><input id="byoc-target" spellcheck="false" placeholder="you@your-server"></label>' +
    '<div class="byoc-row">' +
    '<label class="byoc-field byoc-port"><span>[[i18n:ui.byoc_port]]</span><input id="byoc-port" inputmode="numeric" value="2222"></label>' +
    '<label class="byoc-field"><span>[[i18n:ui.byoc_mac_address]]</span><input id="byoc-address" spellcheck="false" placeholder="[[i18n:ui.byoc_optional]]"></label>' +
    '</div>' +
    '<p class="byoc-note">[[i18n:ui.byoc_deploy_note]]</p>' +
    '</form>' +
    '<form class="byoc-form" id="byoc-pair" autocomplete="off" hidden>' +
    '<div class="byoc-field"><span>[[i18n:ui.byoc_authorize_key]]</span>' +
    '<div class="byoc-key"><code id="byoc-key"></code><button type="button" id="byoc-key-copy">[[i18n:ui.gs_copy]]</button></div></div>' +
    '<label class="byoc-field"><span>[[i18n:ui.byoc_server_address]]</span><input id="byoc-pair-address" spellcheck="false" placeholder="your-server:2222"></label>' +
    '<label class="byoc-field"><span>[[i18n:ui.byoc_fingerprint]]</span><input id="byoc-fingerprint" spellcheck="false" placeholder="SHA256:…"></label>' +
    '<p class="byoc-note">[[i18n:ui.byoc_pair_note]]</p>' +
    '</form>' +
    '<p class="byoc-switch-note" id="byoc-switch-note" hidden>[[i18n:ui.byoc_switches_account]]</p>' +
    '<div class="byoc-footer"><p class="byoc-status" id="byoc-status" role="status"></p>' +
    '<button type="button" class="developer-custom-apply byoc-submit" id="byoc-submit">[[i18n:ui.byoc_deploy_and_pair]]</button></div>';
  document.body?.append(dialog);

  const $ = selector => dialog.querySelector(selector);
  const forms = { deploy: $('#byoc-deploy'), pair: $('#byoc-pair') };
  const submit = $('#byoc-submit');
  const status = $('#byoc-status');
  const keyEl = $('#byoc-key');
  const errors = {
    invalid: "[[i18n:ui.byoc_error_invalid]]",
    ssh_failed: "[[i18n:ui.byoc_error_ssh]]",
    docker_unavailable: "[[i18n:ui.byoc_error_docker]]",
    deploy_failed: "[[i18n:ui.byoc_error_deploy]]",
    unreachable: "[[i18n:ui.byoc_error_unreachable]]",
    fingerprint_mismatch: "[[i18n:ui.byoc_error_fingerprint]]",
    not_authorized: "[[i18n:ui.byoc_error_not_authorized]]",
    not_fennec: "[[i18n:ui.byoc_error_not_fennec]]",
    already_paired: "[[i18n:ui.byoc_error_already_paired]]",
    pairing_unreadable: "[[i18n:ui.byoc_error_already_paired]]",
    busy: "[[i18n:ui.byoc_error_busy]]",
  };
  let mode = 'deploy';
  let watching = 0; // id of the job this dialog started or resumed
  let poll = null;
  let starting = false;

  const running = job => job && (job.stage === 'deploying' || job.stage === 'pairing');
  const stopPoll = () => { if (poll) { clearTimeout(poll); poll = null; } };
  const setBusy = busy => {
    submit.disabled = busy;
    dialog.querySelectorAll('input,.byoc-modes button').forEach(el => { el.disabled = busy; });
    status.classList.toggle('is-busy', busy);
  };
  const showError = code => {
    status.classList.add('is-error');
    status.textContent = errors[code] || "[[i18n:ui.byoc_error_pairing]]";
  };
  const setMode = next => {
    mode = next;
    dialog.querySelectorAll('.byoc-modes button').forEach(b => b.setAttribute('aria-checked', String(b.dataset.mode === mode)));
    forms.deploy.hidden = mode !== 'deploy';
    forms.pair.hidden = mode !== 'pair';
    submit.textContent = mode === 'deploy' ? "[[i18n:ui.byoc_deploy_and_pair]]" : "[[i18n:ui.byoc_pair]]";
    status.textContent = ''; status.classList.remove('is-error');
    if (mode === 'pair' && !keyEl.textContent) send('byoc-key');
  };

  window.renderBYOCKey = value => { keyEl.textContent = value?.device_key || ''; };
  window.renderBYOCStatus = value => {
    const job = value?.job;
    if (!job || (watching && job.id !== watching)) return;
    if (!watching && !starting && !running(job)) return; // a finished job from an earlier visit
    watching = job.id;
    starting = false;
    stopPoll();
    status.classList.remove('is-error');
    if (running(job)) {
      setBusy(true);
      status.textContent = job.stage === 'deploying' ? "[[i18n:ui.byoc_deploying]]" : "[[i18n:ui.byoc_pairing]]";
      if (dialog.open) poll = setTimeout(() => send('byoc-status'), 1000);
      return;
    }
    setBusy(false);
    watching = 0;
    if (job.stage === 'done') {
      // The account state now reads paired and the panel shows Your server.
      if (dialog.open) dialog.close();
      return;
    }
    showError(job.error_code);
  };
  window.renderBYOCStartFailed = code => { starting = false; setBusy(false); showError(code); };

  const start = () => {
    if (starting || submit.disabled) return;
    let request;
    if (mode === 'deploy') {
      const port = Number($('#byoc-port').value.trim() || '2222');
      request = { mode, target: $('#byoc-target').value.trim(), port: Number.isInteger(port) ? port : 0, address: $('#byoc-address').value.trim() };
      if (!request.target) { showError('invalid'); $('#byoc-target').focus(); return; }
    } else {
      request = { mode, address: $('#byoc-pair-address').value.trim(), fingerprint: $('#byoc-fingerprint').value.trim() };
      if (!request.address || !request.fingerprint) { showError('invalid'); return; }
    }
    starting = true; watching = 0;
    setBusy(true);
    status.classList.remove('is-error');
    status.textContent = mode === 'deploy' ? "[[i18n:ui.byoc_deploying]]" : "[[i18n:ui.byoc_pairing]]";
    send('byoc-start:' + JSON.stringify(request));
  };

  window.openBYOCDialog = () => {
    if (dialog.open || typeof dialog.showModal !== 'function') return;
    const runtaSignedIn = typeof authorized !== 'undefined' && authorized && !(typeof selfHosted !== 'undefined' && selfHosted);
    $('#byoc-switch-note').hidden = !runtaSignedIn;
    if (!starting && !watching) { setBusy(false); status.textContent = ''; status.classList.remove('is-error'); }
    dialog.showModal();
    dialog.focus();
    send('byoc-status'); // resumes a deployment still running from an earlier visit
  };

  dialog.querySelectorAll('.byoc-modes button').forEach(b => { b.onclick = () => setMode(b.dataset.mode); });
  $('#byoc-close').onclick = () => dialog.close();
  dialog.addEventListener('close', stopPoll);
  submit.onclick = start;
  Object.values(forms).forEach(form => form.addEventListener('submit', event => { event.preventDefault(); start(); }));
  const copy = $('#byoc-key-copy');
  copy.onclick = () => {
    if (!keyEl.textContent) return;
    send('copy-text:' + keyEl.textContent);
    copy.textContent = "[[i18n:ui.gs_copied]]";
    setTimeout(() => { copy.textContent = "[[i18n:ui.gs_copy]]"; }, 1500);
  };
  document.addEventListener('click', event => {
    if (event.target.closest?.('[data-open-byoc]')) { event.preventDefault(); const menu = document.querySelector('#user-menu'); if (menu) menu.hidden = true; window.openBYOCDialog(); }
  });
})();
