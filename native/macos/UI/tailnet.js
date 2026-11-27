// Tailscale on the cloud computer (FEN-104). Settings › Tailscale signs the
// cloud computer in or out of the user's tailnet; Computers lists the
// tailnet's devices, and "Add" authorizes the cloud computer's public key on
// one of them so it can sign in over SSH. Swift polls tailnet.status and calls
// window.renderTailnet; errors arrive as codes and the copy here explains them.
(() => {
  const t = token => (typeof translate === 'function' ? translate(token) : token);
  const el = (tag, className, text) => { const node = document.createElement(tag); if (className) node.className = className; if (text !== undefined) node.textContent = text; return node; };
  const svg = body => '<svg viewBox="0 0 20 20" aria-hidden="true">' + body + '</svg>';
  // Nine dots: the tailnet mark used for this integration.
  const markSVG = '<svg viewBox="0 0 20 20" aria-hidden="true" class="tailnet-mark">' + [[4,4,.35],[10,4,.35],[16,4,.35],[4,10,1],[10,10,1],[16,10,1],[4,16,.35],[10,16,1],[16,16,.35]].map(([x,y,o])=>'<circle cx="'+x+'" cy="'+y+'" r="2.1" fill="currentColor" stroke="none" opacity="'+o+'"/>').join('') + '</svg>';
  const osIcons = {
    macOS: '<svg viewBox="0 0 20 20" aria-hidden="true" class="os-logo"><path d="M13.6 10.7c0-1.9 1.6-2.8 1.6-2.9-.9-1.3-2.2-1.5-2.7-1.5-1.2-.1-2.2.7-2.8.7-.6 0-1.5-.7-2.4-.6-1.3 0-2.4.7-3.1 1.9-1.3 2.3-.3 5.6.9 7.5.6.9 1.3 1.9 2.3 1.8.9 0 1.3-.6 2.4-.6s1.4.6 2.4.6c1 0 1.6-.9 2.2-1.8.7-1 1-2 1-2.1 0 0-1.8-.7-1.8-3ZM11.8 5c.5-.6.8-1.4.7-2.3-.7 0-1.6.5-2.1 1.1-.5.5-.9 1.4-.8 2.2.8.1 1.6-.4 2.2-1Z"/></svg>',
    linux: svg('<rect x="3" y="4" width="14" height="12" rx="2"/><path d="m6.5 8.5 2 1.5-2 1.5M10.5 12h3"/>'),
    windows: svg('<path d="M3 5.2 9 4.3v5.2H3ZM10.5 4.1 17 3v6.5h-6.5ZM3 10.9h6v5.2l-6-.9ZM10.5 10.9H17V17l-6.5-1.1Z"/>'),
    phone: svg('<rect x="6" y="2.5" width="8" height="15" rx="2"/><path d="M9 15h2"/>'),
    other: svg('<rect x="2.5" y="4" width="15" height="10" rx="1.5"/><path d="M7 17h6M10 14v3"/>'),
  };
  const osIcon = os => osIcons[os === 'macOS' ? 'macOS' : os === 'linux' ? 'linux' : os === 'windows' ? 'windows' : (os === 'iOS' || os === 'android') ? 'phone' : 'other'];
  const chevron = svg('<path d="m8 5 5 5-5 5"/>');
  const loginErrors = {
    network_blocked: '[[i18n:ui.tailnet_error_network_blocked]]',
    login_failed: '[[i18n:ui.tailnet_error_login_failed]]',
    install_failed: '[[i18n:ui.tailnet_error_install_failed]]',
    start_failed: '[[i18n:ui.tailnet_error_start_failed]]',
    unsupported: '[[i18n:ui.tailnet_error_unsupported]]',
    not_ready: '[[i18n:ui.tailnet_error_not_ready]]',
    login_required: '[[i18n:ui.sign_in_to_runta]]',
  };
  const sshErrors = {
    auth_failed: '[[i18n:ui.tailnet_ssh_auth_failed]]',
    refused: '[[i18n:ui.tailnet_ssh_refused]]',
    unreachable: '[[i18n:ui.tailnet_ssh_unreachable]]',
    host_key_changed: '[[i18n:ui.tailnet_ssh_host_key_changed]]',
    ssh_failed: '[[i18n:ui.tailnet_ssh_failed]]',
  };

  let status = null;      // last tailnet.status payload
  let requestError = '';  // control error code when status couldn't be read
  let busy = '';
  let addOpen = false;    // the card's list of tailnet devices to add          // 'login' | 'logout' while a button's call runs

  // Settings: an entry row that opens the Tailscale page.
  const preferences = document.querySelector('#preferences');
  const entryCard = el('div', 'card tailnet-entry-card');
  const entry = el('button', 'preference-row preference-action tailnet-entry');
  entry.type = 'button';
  entry.id = 'tailscale-entry';
  entry.innerHTML = '<span class="preference-icon tailnet-icon">' + markSVG + '</span><span>Tailscale</span><span class="tailnet-entry-state" id="tailscale-entry-state"></span><span class="tailnet-chevron">' + chevron + '</span>';
  entryCard.append(entry);
  preferences?.querySelector('.permission-card')?.after(entryCard);

  // Settings › Tailscale.
  const page = el('section');
  page.id = 'tailscale';
  page.hidden = true;
  page.innerHTML =
    '<div class="session-detail-toolbar"><button type="button" class="tailnet-back" id="tailscale-back" title="[[i18n:ui.settings]]">' + svg('<path d="m12 5-5 5 5 5"/>') + '<span class="tailnet-page-title">Tailscale</span></button></div>' +
    '<div class="card tailnet-card"><div class="tailnet-head"><span class="tailnet-icon tailnet-head-icon">' + markSVG + '</span><div class="tailnet-head-text"><strong>Tailscale</strong><span id="tailscale-summary"></span></div></div>' +
    '<p class="tailnet-intro">[[i18n:ui.tailnet_intro]]</p>' +
    '<div id="tailscale-body"></div></div>';
  preferences?.before(page);

  // Computers: the tailnet's devices below This Mac.
  // One card for every worker machine: this Mac first (its live metrics,
  // drawn by drawHostMetrics in panel.js), then the tailnet's computers.
  const card = el('div', 'card runtime-metrics-card tailnet-devices-card');
  card.id = 'tailnet-card';
  const cardHead = el('div', 'tailnet-card-top');
  const hostSlot = el('div', 'tailnet-host');
  const cardBody = el('div', 'tailnet-body');
  const cardSlot = el('div', 'tailnet-add-slot');
  card.append(cardHead, cardSlot, hostSlot, cardBody);
  // This Mac gets the same compact row as the other machines (one line of
  // meters), so the card stays short. panel.js still draws the sparkline
  // card, hidden, from the same per-second samples.
  const hostCard = document.querySelector('#host-metrics-card');
  if (hostCard) hostCard.hidden = true;
  const hostRow = el('div', 'tailnet-device tailnet-local');
  hostSlot.append(hostRow);
  let hostSample = null;
  const drawHostRow = () => {
    const p = hostSample || {};
    hostRow.replaceChildren();
    const main = el('div', 'tailnet-device-main');
    const icon = el('span', 'tailnet-os');
    icon.innerHTML = osIcons.macOS;
    // Name and a muted "This Mac" on the left, the OS and memory badge on the
    // right, like the cloud computer card's head (FEN-104).
    const text = el('div', 'tailnet-device-text tailnet-local-text');
    const name = el('span', 'tailnet-device-name', p.name || t('[[i18n:ui.this_mac]]'));
    name.title = [p.name, p.chip].filter(Boolean).join(' · ');
    text.append(name);
    if (p.name) text.append(el('span', 'host-local-tag', t('[[i18n:ui.this_mac]]')));
    main.append(icon, text);
    const os = [p.os_name || 'macOS', p.os_version].filter(Boolean).join(' ');
    const ram = Number(p.memory_total_bytes) > 0 ? Math.round(p.memory_total_bytes / 1073741824) + ' GB' : '';
    if (hostSample) main.append(el('span', 'runtime-metrics-spec tailnet-local-spec', [os, ram].filter(Boolean).join(' · ')));
    hostRow.append(main);
    if (hostSample) hostRow.append(meters({ cpu_percent: p.cpu_percent, memory_used_bytes: p.memory_used_bytes, memory_total_bytes: p.memory_total_bytes, disk_used_bytes: p.disk_used_bytes, disk_total_bytes: p.disk_total_bytes }));
  };
  const renderHost = window.renderHostMetrics;
  window.renderHostMetrics = payload => {
    renderHost?.(payload);
    if (payload && typeof payload === 'object') { hostSample = payload; drawHostRow(); }
  };
  document.querySelector('#computers')?.append(card);

  // Add a computer.
  const dialog = el('dialog', 'developer-dialog tailnet-dialog');
  dialog.id = 'tailnet-add-dialog';
  dialog.tabIndex = -1;
  dialog.setAttribute('aria-labelledby', 'tailnet-add-title');
  dialog.innerHTML =
    '<div class="developer-dialog-head"><strong id="tailnet-add-title"></strong><button type="button" class="developer-close" id="tailnet-add-close" aria-label="[[i18n:ui.close]]">×</button></div>' +
    '<p class="byoc-note tailnet-add-intro">[[i18n:ui.tailnet_add_intro]]</p>' +
    '<ol class="tailnet-steps">' +
    '<li><span class="tailnet-step-mark">1</span><div class="tailnet-step-main"><span id="tailnet-add-run"></span>' +
    '<div class="byoc-key tailnet-command"><code id="tailnet-add-command"></code><button type="button" id="tailnet-add-copy">[[i18n:ui.gs_copy]]</button></div>' +
    '<p class="byoc-note" id="tailnet-add-note"></p></div></li>' +
    '<li><span class="tailnet-step-mark">2</span><div class="tailnet-step-main"><label class="byoc-field"><span>[[i18n:ui.tailnet_add_user]]</span><input id="tailnet-add-user" spellcheck="false" autocomplete="off"></label></div></li>' +
    '</ol>' +
    '<p class="byoc-note tailnet-fennec-note" id="tailnet-add-fennec" hidden>[[i18n:ui.tailnet_add_fennec_note]]</p>' +
    '<div class="byoc-footer"><p class="byoc-status" id="tailnet-add-status" role="status"></p>' +
    '<button type="button" class="developer-custom-apply" id="tailnet-add-connect">[[i18n:ui.tailnet_connect]]</button></div>';
  document.body?.append(dialog);
  const $ = selector => dialog.querySelector(selector);
  let adding = null;      // the device the dialog adds
  let connecting = false;

  function open() { if (typeof tab === 'function') tab('tailscale'); document.querySelector('#settings')?.classList.add('active'); send('tailnet-status'); }
  entry.onclick = open;
  page.querySelector('#tailscale-back').onclick = () => { if (typeof tab === 'function') tab('preferences'); };

  function stateLabel(s) {
    if (requestError) return t('[[i18n:ui.tailnet_unavailable]]');
    if (!s) return '';
    if (s.state === 'running') return t('[[i18n:ui.tailnet_on]]');
    if (s.error) return t('[[i18n:ui.tailnet_problem]]');
    if (['installing', 'starting'].includes(s.state) || (s.state === 'needs_login' && s.auth_url)) return t('[[i18n:ui.tailnet_signing_in]]');
    if (s.state === 'needs_approval') return t('[[i18n:ui.tailnet_waiting_approval]]');
    return t('[[i18n:ui.tailnet_off]]');
  }

  function button(label, className, onClick, disabled = false) {
    const b = el('button', className, t(label));
    b.type = 'button';
    b.disabled = disabled;
    b.onclick = onClick;
    return b;
  }

  function row(label, value, copyable = false) {
    const r = el('div', 'tailnet-row');
    r.append(el('span', 'tailnet-row-label', t(label)));
    const v = el('span', 'tailnet-row-value', value || '—');
    if (copyable && value) { v.classList.add('is-copyable'); v.title = t('[[i18n:ui.gs_copy]]'); v.onclick = () => { send('copy-text:' + value); v.classList.add('copied'); setTimeout(() => v.classList.remove('copied'), 1200); }; }
    r.append(v);
    return r;
  }

  function drawPage() {
    const body = page.querySelector('#tailscale-body');
    const summary = page.querySelector('#tailscale-summary');
    const entryState = document.querySelector('#tailscale-entry-state');
    if (entryState) entryState.textContent = stateLabel(status);
    summary.textContent = stateLabel(status);
    summary.className = 'tailnet-summary' + (status?.state === 'running' && !requestError ? ' is-on' : '');
    body.replaceChildren();
    const s = status || {};
    const actions = el('div', 'tailnet-actions');
    const message = el('p', 'tailnet-message');
    if (requestError) {
      message.textContent = t(loginErrors[requestError] || '[[i18n:ui.tailnet_error_not_ready]]');
      body.append(message);
      return;
    }
    if (!status) { message.textContent = t('[[i18n:ui.loading]]'); body.append(message); return; }
    if (s.state === 'running') {
      body.append(row('[[i18n:ui.tailnet_signed_in_as]]', s.user || s.tailnet));
      body.append(row('[[i18n:ui.tailnet_cloud_name]]', s.self?.dns_name || s.self?.name, true));
      const ipv4 = (s.self?.ips || []).find(ip => ip.includes('.'));
      body.append(row('[[i18n:ui.tailnet_ip]]', ipv4, true));
      actions.append(button('[[i18n:ui.tailnet_sign_out]]', 'tailnet-secondary', () => { busy = 'logout'; drawPage(); send('tailnet-logout'); }, busy === 'logout'));
      body.append(actions);
      return;
    }
    const pending = busy === 'login' || ['installing', 'starting'].includes(s.state) || (s.state === 'needs_login' && s.auth_url && !s.error);
    if (s.error) {
      message.classList.add('is-error');
      message.textContent = t(loginErrors[s.error] || '[[i18n:ui.tailnet_error_login_failed]]');
      if (s.detail) message.title = s.detail;
    } else if (s.state === 'installing') {
      message.textContent = t('[[i18n:ui.tailnet_installing]]');
    } else if (s.state === 'starting' || (busy === 'login' && !s.auth_url)) {
      message.textContent = t('[[i18n:ui.tailnet_starting]]');
    } else if (s.state === 'needs_login' && s.auth_url) {
      message.textContent = t('[[i18n:ui.tailnet_finish_in_browser]]');
      actions.append(button('[[i18n:ui.open_again]]', 'tailnet-secondary', () => send('tailnet-open-login')));
    } else if (s.state === 'needs_approval') {
      message.textContent = t('[[i18n:ui.tailnet_needs_approval]]');
    } else {
      message.textContent = t('[[i18n:ui.tailnet_signin_note]]');
    }
    if (pending && !s.error) message.prepend(el('span', 'tailnet-spinner'));
    body.append(message);
    if (s.error || !pending) actions.prepend(button(s.error ? '[[i18n:ui.retry]]' : '[[i18n:ui.tailnet_sign_in]]', 'developer-custom-apply', () => { busy = 'login'; drawPage(); send('tailnet-login'); }, busy === 'login'));
    if (pending || s.state === 'needs_approval' || (s.error && s.installed)) actions.append(button('[[i18n:ui.cancel]]', 'tailnet-secondary', () => { busy = 'logout'; drawPage(); send('tailnet-logout'); }, busy === 'logout'));
    body.append(actions);
  }

  // "28/48 GB": both numbers share one unit so three meters fit one row.
  function usage(used, total) {
    const unit = total >= 1e12 ? ['TB', 1e12] : ['GB', 1073741824];
    const n = value => { const v = value / unit[1]; return v >= 10 ? v.toFixed(0) : v.toFixed(1); };
    return n(used) + '/' + n(total) + ' ' + unit[0];
  }
  const osNames = { linux: 'Linux', windows: 'Windows', android: 'Android', freebsd: 'FreeBSD', openbsd: 'OpenBSD' };
  const sshCapable = os => !['iOS', 'android', 'tvOS'].includes(os);
  function meter(label, value, percent) {
    const cell = el('div', 'tailnet-meter');
    const top = el('div', 'tailnet-meter-top');
    top.append(el('span', 'tailnet-meter-label', label), el('span', 'tailnet-meter-value', value));
    const track = el('div', 'runtime-disk-track');
    const fill = el('div');
    fill.style.width = Math.max(0, Math.min(100, percent || 0)) + '%';
    track.append(fill);
    cell.append(top, track);
    return cell;
  }

  function meters(metrics) {
    const grid = el('div', 'tailnet-meters');
    grid.append(meter('CPU', (Number(metrics.cpu_percent) || 0).toFixed(1) + '%', Number(metrics.cpu_percent)));
    if (metrics.memory_total_bytes) grid.append(meter(t('[[i18n:ui.memory]]'), usage(metrics.memory_used_bytes || 0, metrics.memory_total_bytes), 100 * (metrics.memory_used_bytes || 0) / metrics.memory_total_bytes));
    if (metrics.disk_total_bytes) grid.append(meter(t('[[i18n:ui.disk]]'), usage(metrics.disk_used_bytes || 0, metrics.disk_total_bytes), 100 * (metrics.disk_used_bytes || 0) / metrics.disk_total_bytes));
    return grid;
  }

  function deviceRow(device, thisDevice) {
    const item = el('div', 'tailnet-device' + (device.online ? '' : ' is-offline'));
    const main = el('div', 'tailnet-device-main');
    const icon = el('span', 'tailnet-os');
    icon.innerHTML = osIcon(device.os);
    const text = el('div', 'tailnet-device-text');
    const name = el('span', 'tailnet-device-name', device.name);
    name.title = device.dns_name || device.name;
    const metrics = device.computer?.metrics;
    const ip = (device.ips || []).find(value => value.includes('.')) || '';
    const os = metrics?.os_name ? [metrics.os_name, metrics.os_version].filter(Boolean).join(' ') : osNames[device.os] || device.os || '';
    const sub = el('span', 'tailnet-device-sub');
    const dot = el('span', 'tailnet-dot' + (device.online ? ' is-online' : ''));
    sub.append(dot, document.createTextNode([device.online ? t('[[i18n:ui.tailnet_online]]') : t('[[i18n:ui.tailnet_offline]]'), os, ip].filter(Boolean).join(' · ')));
    text.append(name, sub);
    main.append(icon, text);
    const side = el('div', 'tailnet-device-side');
    const computer = device.computer;
    const connected = computer && !computer.error && computer.alias;
    if (device.id === thisDevice) side.append(el('span', 'runtime-metrics-spec', t('[[i18n:ui.this_mac]]')));
    if (connected) {
      const tag = el('span', 'runtime-metrics-spec tailnet-connected', t('[[i18n:ui.tailnet_worker]]'));
      tag.title = 'ssh ' + computer.alias;
      side.append(tag);
      const remove = el('button', 'tailnet-remove');
      remove.type = 'button';
      remove.innerHTML = svg('<path d="m6 6 8 8M14 6l-8 8"/>');
      remove.title = remove.ariaLabel = t('[[i18n:ui.tailnet_remove]]');
      remove.onclick = () => send('tailnet-remove:' + JSON.stringify({ id: device.id }));
      side.append(remove);
    } else if ((device.id !== thisDevice || computer?.error) && sshCapable(device.os)) {
      const add = button('[[i18n:ui.tailnet_add]]', 'tailnet-add', () => openAdd(device), !device.online);
      if (!device.online) add.title = t('[[i18n:ui.tailnet_offline]]');
      side.append(add);
    }
    main.append(side);
    item.append(main);
    if (computer?.error) {
      const problem = el('p', 'tailnet-device-problem', t(sshErrors[computer.error] || sshErrors.ssh_failed));
      if (computer.error === 'refused' && device.id === thisDevice) {
        const fix = el('button', 'gs-link', t('[[i18n:ui.tailnet_open_sharing]]'));
        fix.type = 'button';
        fix.onclick = () => send('open-remote-login');
        problem.append(' ', fix);
      }
      item.append(problem);
    }
    // This Mac's own numbers are in the card above.
    if (connected && metrics && device.online && device.id !== thisDevice) item.append(meters(metrics));
    return item;
  }

  function drawCard() {
    const s = status;
    window.tailnetComputerCount = 0;
    cardHead.replaceChildren();
    cardBody.replaceChildren();
    cardSlot.replaceChildren();
    const running = s?.state === 'running' && !requestError;
    const head = el('div', 'runtime-metrics-head');
    const title = el('span', 'runtime-metrics-title-wrap');
    const icon = el('span', 'billing-field-icon tailnet-icon');
    icon.innerHTML = markSVG;
    title.append(icon, el('span', 'runtime-metrics-title', t('[[i18n:ui.tailnet_title]]')));
    head.append(title);
    // Adding lives in the header so it is on the first screen however many
    // machines the card lists. Before Tailscale is signed in it opens
    // Settings › Tailscale; after, it unfolds the devices that can be added.
    const others = running ? (s.devices || []).filter(d => d.id !== s.this_device && !(d.computer && !d.computer.error && d.computer.alias)) : [];
    if (s && requestError !== 'login_required') {
      const add = el('button', 'tailnet-head-add' + (running && addOpen ? ' is-open' : ''));
      add.type = 'button';
      add.innerHTML = svg('<path d="M10 4.5v11M4.5 10h11"/>');
      add.append(el('span', '', t('[[i18n:ui.tailnet_add_worker]]')));
      const available = others.filter(d => sshCapable(d.os)).length;
      if (running && available) add.append(el('span', 'tailnet-head-count', String(available)));
      if (running) add.setAttribute('aria-expanded', String(addOpen));
      add.onclick = () => { if (!running) { open(); return; } addOpen = !addOpen; drawCard(); };
      head.append(add);
    }
    cardHead.append(head);
    // Say what worker machines are for until the tailnet is set up.
    if (!running) cardHead.append(el('p', 'tailnet-card-intro' + (s?.error ? ' is-error' : ''), t(s?.error ? '[[i18n:ui.tailnet_problem_open]]' : '[[i18n:ui.tailnet_card_intro]]')));
    const self = running ? (s.devices || []).find(d => d.id === s.this_device) : null;
    if (self?.computer?.error) {
      const problem = el('p', 'tailnet-device-problem tailnet-host-problem', t(sshErrors[self.computer.error] || sshErrors.ssh_failed));
      if (self.computer.error === 'refused') {
        const fix = el('button', 'gs-link', t('[[i18n:ui.tailnet_open_sharing]]'));
        fix.type = 'button';
        fix.onclick = () => send('open-remote-login');
        problem.append(' ', fix);
      }
      hostSlot.querySelector('.tailnet-host-problem')?.remove();
      hostSlot.append(problem);
    } else {
      hostSlot.querySelector('.tailnet-host-problem')?.remove();
    }
    if (!s || requestError === 'login_required') { window.updateComputerCount?.(); return; }
    if (!running) { window.updateComputerCount?.(); return; }
    // This Mac is the first worker machine above; the card lists the other
    // workers, and the header's Add unfolds the rest of the tailnet here.
    const devices = (Array.isArray(s.devices) ? s.devices : []).filter(d => d.id !== s.this_device);
    const workers = devices.filter(d => d.computer && !d.computer.error && d.computer.alias);
    if (workers.length) {
      const list = el('div', 'tailnet-devices');
      for (const device of workers) list.append(deviceRow(device, s.this_device));
      cardBody.append(list);
    }
    if (addOpen) {
      const list = el('div', 'tailnet-devices tailnet-candidates');
      for (const device of others) list.append(deviceRow(device, s.this_device));
      if (!others.length) list.append(el('p', 'tailnet-message tailnet-empty', t('[[i18n:ui.tailnet_no_devices]]')));
      cardSlot.replaceChildren(list);
    } else cardSlot.replaceChildren();
    window.tailnetComputerCount = workers.length;
    window.updateComputerCount?.();
  }

  function openAdd(device) {
    adding = device;
    const commands = status?.commands || {};
    const windows = device.os === 'windows';
    $('#tailnet-add-title').textContent = t('[[i18n:ui.tailnet_add_title]]').replace('{name}', device.name);
    $('#tailnet-add-run').textContent = t('[[i18n:ui.tailnet_add_run]]').replace('{name}', device.name);
    $('#tailnet-add-command').textContent = (windows ? commands.windows : commands.posix) || '';
    $('#tailnet-add-note').textContent = t(windows ? '[[i18n:ui.tailnet_add_note_windows]]' : device.os === 'macOS' ? '[[i18n:ui.tailnet_add_note_mac]]' : '[[i18n:ui.tailnet_add_note_linux]]');
    $('#tailnet-add-fennec').hidden = device.os !== 'macOS';
    const user = $('#tailnet-add-user');
    if (!user.value || user.dataset.auto === 'true') { user.value = windows ? '' : (status?.local_user || ''); user.dataset.auto = 'true'; }
    $('#tailnet-add-status').textContent = '';
    $('#tailnet-add-status').classList.remove('is-error');
    connecting = false;
    $('#tailnet-add-connect').disabled = false;
    if (!dialog.open && typeof dialog.showModal === 'function') dialog.showModal();
  }
  $('#tailnet-add-user').oninput = event => { event.target.dataset.auto = 'false'; };
  $('#tailnet-add-close').onclick = () => dialog.close();
  $('#tailnet-add-copy').onclick = () => {
    const text = $('#tailnet-add-command').textContent;
    if (!text) return;
    send('copy-text:' + text);
    const b = $('#tailnet-add-copy');
    b.textContent = t('[[i18n:ui.gs_copied]]');
    setTimeout(() => { b.textContent = t('[[i18n:ui.gs_copy]]'); }, 1500);
  };
  $('#tailnet-add-connect').onclick = () => {
    const user = $('#tailnet-add-user').value.trim();
    const state = $('#tailnet-add-status');
    if (!adding || connecting) return;
    if (!/^[A-Za-z0-9._][A-Za-z0-9._@-]{0,63}$/.test(user)) { state.textContent = t('[[i18n:ui.tailnet_add_user_needed]]'); state.classList.add('is-error'); return; }
    connecting = true;
    $('#tailnet-add-connect').disabled = true;
    state.classList.remove('is-error');
    state.textContent = t('[[i18n:ui.tailnet_connecting]]');
    send('tailnet-add:' + JSON.stringify({ id: adding.id, user }));
  };

  window.renderTailnetAdded = result => {
    connecting = false;
    $('#tailnet-add-connect').disabled = false;
    const state = $('#tailnet-add-status');
    const code = result?.error || result?.computer?.error || (!result?.computer ? 'ssh_failed' : '');
    if (code) {
      state.classList.add('is-error');
      state.textContent = t(sshErrors[code] || loginErrors[code] || sshErrors.ssh_failed);
      return;
    }
    state.classList.remove('is-error');
    state.textContent = t('[[i18n:ui.tailnet_connected]]');
    setTimeout(() => dialog.close(), 700);
    send('tailnet-status');
  };

  window.renderTailnet = payload => {
    if (payload && typeof payload === 'object' && typeof payload.error === 'string' && !payload.state) {
      requestError = payload.error;
      busy = '';
    } else if (payload && typeof payload === 'object') {
      requestError = '';
      status = payload;
      // A finished call clears its button; the polled status shows the rest.
      if (busy === 'login' && (status.state !== 'not_installed' && status.state !== 'stopped' || status.error)) busy = '';
      if (busy === 'logout' && status.state !== 'running' && !['installing', 'starting'].includes(status.state)) busy = '';
    }
    drawPage();
    drawCard();
  };
  window.renderTailnetBusy = value => { busy = value || ''; drawPage(); };
  window.rerenderTailnet = () => { drawPage(); drawCard(); drawHostRow(); };
  drawPage();
  drawCard();
  drawHostRow();
})();
