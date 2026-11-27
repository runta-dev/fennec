// Tailscale on the cloud computer (FEN-104). Settings › Tailscale signs the
// cloud computer in or out of the user's tailnet; Computers lists the
// tailnet's devices, and "Add" signs in to one of them once with a
// password or private key the user gives, authorizes the cloud computer's
// public key there, and from then on signs in with that key (FEN-123). Swift polls tailnet.status and calls
// window.renderTailnet; errors arrive as codes and the copy here explains them.
(() => {
  const t = token => (typeof translate === 'function' ? translate(token) : token);
  const el = (tag, className, text) => { const node = document.createElement(tag); if (className) node.className = className; if (text !== undefined) node.textContent = text; return node; };
  const svg = body => '<svg viewBox="0 0 20 20" aria-hidden="true">' + body + '</svg>';
  // Nine dots: the tailnet mark used for this integration.
  const markSVG = '<svg viewBox="0 0 20 20" aria-hidden="true" class="tailnet-mark">' + [[4,4,.35],[10,4,.35],[16,4,.35],[4,10,1],[10,10,1],[16,10,1],[4,16,.35],[10,16,1],[16,16,.35]].map(([x,y,o])=>'<circle cx="'+x+'" cy="'+y+'" r="2.1" fill="currentColor" stroke="none" opacity="'+o+'"/>').join('') + '</svg>';
  const osIcons = {
    macOS: '<svg viewBox="0 0 20 20" aria-hidden="true" class="os-logo"><path d="M13.6 10.7c0-1.9 1.6-2.8 1.6-2.9-.9-1.3-2.2-1.5-2.7-1.5-1.2-.1-2.2.7-2.8.7-.6 0-1.5-.7-2.4-.6-1.3 0-2.4.7-3.1 1.9-1.3 2.3-.3 5.6.9 7.5.6.9 1.3 1.9 2.3 1.8.9 0 1.3-.6 2.4-.6s1.4.6 2.4.6c1 0 1.6-.9 2.2-1.8.7-1 1-2 1-2.1 0 0-1.8-.7-1.8-3ZM11.8 5c.5-.6.8-1.4.7-2.3-.7 0-1.6.5-2.1 1.1-.5.5-.9 1.4-.8 2.2.8.1 1.6-.4 2.2-1Z"/></svg>',
    // Simple Icons "linux" (CC0-1.0, simple-icons 16.38.1); see THIRD_PARTY_NOTICES.md.
    linux: '<svg viewBox="0 0 24 24" aria-hidden="true" class="os-logo"><path d="M12.504 0c-.155 0-.315.008-.48.021-4.226.333-3.105 4.807-3.17 6.298-.076 1.092-.3 1.953-1.05 3.02-.885 1.051-2.127 2.75-2.716 4.521-.278.832-.41 1.684-.287 2.489a.424.424 0 00-.11.135c-.26.268-.45.6-.663.839-.199.199-.485.267-.797.4-.313.136-.658.269-.864.68-.09.189-.136.394-.132.602 0 .199.027.4.055.536.058.399.116.728.04.97-.249.68-.28 1.145-.106 1.484.174.334.535.47.94.601.81.2 1.91.135 2.774.6.926.466 1.866.67 2.616.47.526-.116.97-.464 1.208-.946.587-.003 1.23-.269 2.26-.334.699-.058 1.574.267 2.577.2.025.134.063.198.114.333l.003.003c.391.778 1.113 1.132 1.884 1.071.771-.06 1.592-.536 2.257-1.306.631-.765 1.683-1.084 2.378-1.503.348-.199.629-.469.649-.853.023-.4-.2-.811-.714-1.376v-.097l-.003-.003c-.17-.2-.25-.535-.338-.926-.085-.401-.182-.786-.492-1.046h-.003c-.059-.054-.123-.067-.188-.135a.357.357 0 00-.19-.064c.431-1.278.264-2.55-.173-3.694-.533-1.41-1.465-2.638-2.175-3.483-.796-1.005-1.576-1.957-1.56-3.368.026-2.152.236-6.133-3.544-6.139zm.529 3.405h.013c.213 0 .396.062.584.198.19.135.33.332.438.533.105.259.158.459.166.724 0-.02.006-.04.006-.06v.105a.086.086 0 01-.004-.021l-.004-.024a1.807 1.807 0 01-.15.706.953.953 0 01-.213.335.71.71 0 00-.088-.042c-.104-.045-.198-.064-.284-.133a1.312 1.312 0 00-.22-.066c.05-.06.146-.133.183-.198.053-.128.082-.264.088-.402v-.02a1.21 1.21 0 00-.061-.4c-.045-.134-.101-.2-.183-.333-.084-.066-.167-.132-.267-.132h-.016c-.093 0-.176.03-.262.132a.8.8 0 00-.205.334 1.18 1.18 0 00-.09.4v.019c.002.089.008.179.02.267-.193-.067-.438-.135-.607-.202a1.635 1.635 0 01-.018-.2v-.02a1.772 1.772 0 01.15-.768c.082-.22.232-.406.43-.533a.985.985 0 01.594-.2zm-2.962.059h.036c.142 0 .27.048.399.135.146.129.264.288.344.465.09.199.14.4.153.667v.004c.007.134.006.2-.002.266v.08c-.03.007-.056.018-.083.024-.152.055-.274.135-.393.2.012-.09.013-.18.003-.267v-.015c-.012-.133-.04-.2-.082-.333a.613.613 0 00-.166-.267.248.248 0 00-.183-.064h-.021c-.071.006-.13.04-.186.132a.552.552 0 00-.12.27.944.944 0 00-.023.33v.015c.012.135.037.2.08.334.046.134.098.2.166.268.01.009.02.018.034.024-.07.057-.117.07-.176.136a.304.304 0 01-.131.068 2.62 2.62 0 01-.275-.402 1.772 1.772 0 01-.155-.667 1.759 1.759 0 01.08-.668 1.43 1.43 0 01.283-.535c.128-.133.26-.2.418-.2zm1.37 1.706c.332 0 .733.065 1.216.399.293.2.523.269 1.052.468h.003c.255.136.405.266.478.399v-.131a.571.571 0 01.016.47c-.123.31-.516.643-1.063.842v.002c-.268.135-.501.333-.775.465-.276.135-.588.292-1.012.267a1.139 1.139 0 01-.448-.067 3.566 3.566 0 01-.322-.198c-.195-.135-.363-.332-.612-.465v-.005h-.005c-.4-.246-.616-.512-.686-.71-.07-.268-.005-.47.193-.6.224-.135.38-.271.483-.336.104-.074.143-.102.176-.131h.002v-.003c.169-.202.436-.47.839-.601.139-.036.294-.065.466-.065zm2.8 2.142c.358 1.417 1.196 3.475 1.735 4.473.286.534.855 1.659 1.102 3.024.156-.005.33.018.513.064.646-1.671-.546-3.467-1.089-3.966-.22-.2-.232-.335-.123-.335.59.534 1.365 1.572 1.646 2.757.13.535.16 1.104.021 1.67.067.028.135.06.205.067 1.032.534 1.413.938 1.23 1.537v-.043c-.06-.003-.12 0-.18 0h-.016c.151-.467-.182-.825-1.065-1.224-.915-.4-1.646-.336-1.77.465-.008.043-.013.066-.018.135-.068.023-.139.053-.209.064-.43.268-.662.669-.793 1.187-.13.533-.17 1.156-.205 1.869v.003c-.02.334-.17.838-.319 1.35-1.5 1.072-3.58 1.538-5.348.334a2.645 2.645 0 00-.402-.533 1.45 1.45 0 00-.275-.333c.182 0 .338-.03.465-.067a.615.615 0 00.314-.334c.108-.267 0-.697-.345-1.163-.345-.467-.931-.995-1.788-1.521-.63-.4-.986-.87-1.15-1.396-.165-.534-.143-1.085-.015-1.645.245-1.07.873-2.11 1.274-2.763.107-.065.037.135-.408.974-.396.751-1.14 2.497-.122 3.854a8.123 8.123 0 01.647-2.876c.564-1.278 1.743-3.504 1.836-5.268.048.036.217.135.289.202.218.133.38.333.59.465.21.201.477.335.876.335.039.003.075.006.11.006.412 0 .73-.134.997-.268.29-.134.52-.334.74-.4h.005c.467-.135.835-.402 1.044-.7zm2.185 8.958c.037.6.343 1.245.882 1.377.588.134 1.434-.333 1.791-.765l.211-.01c.315-.007.577.01.847.268l.003.003c.208.199.305.53.391.876.085.4.154.78.409 1.066.486.527.645.906.636 1.14l.003-.007v.018l-.003-.012c-.015.262-.185.396-.498.595-.63.401-1.746.712-2.457 1.57-.618.737-1.37 1.14-2.036 1.191-.664.053-1.237-.2-1.574-.898l-.005-.003c-.21-.4-.12-1.025.056-1.69.176-.668.428-1.344.463-1.897.037-.714.076-1.335.195-1.814.12-.465.308-.797.641-.984l.045-.022zm-10.814.049h.01c.053 0 .105.005.157.014.376.055.706.333 1.023.752l.91 1.664.003.003c.243.533.754 1.064 1.189 1.637.434.598.77 1.131.729 1.57v.006c-.057.744-.48 1.148-1.125 1.294-.645.135-1.52.002-2.395-.464-.968-.536-2.118-.469-2.857-.602-.369-.066-.61-.2-.723-.4-.11-.2-.113-.602.123-1.23v-.004l.002-.003c.117-.334.03-.752-.027-1.118-.055-.401-.083-.71.043-.94.16-.334.396-.4.69-.533.294-.135.64-.202.915-.47h.002v-.002c.256-.268.445-.601.668-.838.19-.201.38-.336.663-.336zm7.159-9.074c-.435.201-.945.535-1.488.535-.542 0-.97-.267-1.28-.466-.154-.134-.28-.268-.373-.335-.164-.134-.144-.333-.074-.333.109.016.129.134.199.2.096.066.215.2.36.333.292.2.68.467 1.167.467.485 0 1.053-.267 1.398-.466.195-.135.445-.334.648-.467.156-.136.149-.267.279-.267.128.016.034.134-.147.332a8.097 8.097 0 01-.69.468zm-1.082-1.583V5.64c-.006-.02.013-.042.029-.05.074-.043.18-.027.26.004.063 0 .16.067.15.135-.006.049-.085.066-.135.066-.055 0-.092-.043-.141-.068-.052-.018-.146-.008-.163-.065zm-.551 0c-.02.058-.113.049-.166.066-.047.025-.086.068-.14.068-.05 0-.13-.02-.136-.068-.01-.066.088-.133.15-.133.08-.031.184-.047.259-.005.019.009.036.03.03.05v.02h.003z"/></svg>',
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
    shields_up: '[[i18n:ui.tailnet_ssh_shields_up]]',
    host_key_changed: '[[i18n:ui.tailnet_ssh_host_key_changed]]',
    ssh_failed: '[[i18n:ui.tailnet_ssh_failed]]',
    ssh_check: '[[i18n:ui.tailnet_ssh_check]]',
  };
  // The tailnet policy rule that lets Tailscale SSH sign-ins from the cloud
  // computer through without a browser check every 12 hours (FEN-123).
  const acceptRule = '{\n  "action": "accept",\n  "src": ["autogroup:member"],\n  "dst": ["autogroup:self"],\n  "users": ["autogroup:nonroot", "root"]\n}';
  const policyURL = 'https://login.tailscale.com/admin/acls/file';
  // The add dialog's own wording for a failed first sign-in.
  const addErrors = {
    ...sshErrors,
    auth_failed: '[[i18n:ui.tailnet_add_auth_failed]]',
    key_invalid: '[[i18n:ui.tailnet_add_key_invalid]]',
    key_passphrase: '[[i18n:ui.tailnet_add_key_passphrase]]',
  };

  let status = null;      // last tailnet.status payload
  let requestError = '';  // control error code when status couldn't be read
  let busy = '';          // 'login' | 'logout' while a button's call runs

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
  // One card for every worker computer: this Mac first (its live metrics,
  // drawn by drawHostMetrics in panel.js), then the tailnet's computers.
  const card = el('div', 'card runtime-metrics-card tailnet-devices-card');
  card.id = 'tailnet-card';
  const cardHead = el('div', 'tailnet-card-top');
  const hostSlot = el('div', 'tailnet-host');
  const cardBody = el('div', 'tailnet-body');
  card.append(cardHead, hostSlot, cardBody);
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
  // The user's password or private key signs in once; the cloud computer
  // then authorizes its own key there. Port and key passphrase are advanced.
  dialog.innerHTML =
    '<div class="developer-dialog-head"><strong id="tailnet-add-title"></strong><button type="button" class="developer-close" id="tailnet-add-close" aria-label="[[i18n:ui.close]]">×</button></div>' +
    '<p class="byoc-note tailnet-add-intro">[[i18n:ui.tailnet_add_intro]]</p>' +
    '<form class="byoc-form tailnet-add-form" id="tailnet-add-form" autocomplete="off">' +
    '<label class="byoc-field"><span>[[i18n:ui.tailnet_add_user]]</span><input id="tailnet-add-user" spellcheck="false" autocomplete="off"></label>' +
    '<div class="developer-segments byoc-modes" role="radiogroup" aria-label="[[i18n:ui.tailnet_add_sign_in]]">' +
    '<button type="button" role="radio" data-auth="password" aria-checked="true">[[i18n:ui.tailnet_add_password]]</button>' +
    '<button type="button" role="radio" data-auth="key" aria-checked="false">[[i18n:ui.tailnet_add_private_key]]</button>' +
    '</div>' +
    // The segment above names the field, so the fields carry no label.
    '<div class="byoc-field" id="tailnet-add-password-field"><input id="tailnet-add-password" type="password" autocomplete="off" aria-label="[[i18n:ui.tailnet_add_password]]"></div>' +
    '<div class="byoc-field" id="tailnet-add-key-field" hidden><textarea id="tailnet-add-key" spellcheck="false" rows="4" aria-label="[[i18n:ui.tailnet_add_private_key]]" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"></textarea></div>' +
    '<details class="tailnet-advanced"><summary><svg class="disclosure-arrow" viewBox="0 0 16 16" aria-hidden="true"><path d="m6 4 4 4-4 4"/></svg><span>[[i18n:ui.tailnet_add_advanced]]</span></summary>' +
    '<div class="byoc-row"><label class="byoc-field byoc-port"><span>[[i18n:ui.byoc_port]]</span><input id="tailnet-add-port" inputmode="numeric" value="22"></label>' +
    '<label class="byoc-field" id="tailnet-add-passphrase-field"><span>[[i18n:ui.tailnet_add_passphrase]]</span><input id="tailnet-add-passphrase" type="password" autocomplete="off" placeholder="[[i18n:ui.byoc_optional]]"></label></div>' +
    '</details>' +
    '<p class="byoc-note" id="tailnet-add-note"></p>' +
    // Linux without Tailscale SSH: turning it on there skips the password.
    '<div class="tailnet-ssh-hint" id="tailnet-add-ssh-hint" hidden><p class="byoc-note">[[i18n:ui.tailnet_add_or_tailscale_ssh]]</p>' +
    '<div class="byoc-key"><code>sudo tailscale set --ssh</code><button type="button" data-copy="sudo tailscale set --ssh">[[i18n:ui.gs_copy]]</button></div></div>' +
    // The policy says "check": show the rule to change and where.
    '<div class="tailnet-check" id="tailnet-add-check" hidden><p class="byoc-note">[[i18n:ui.tailnet_add_check]]</p>' +
    '<div class="byoc-key tailnet-command"><code id="tailnet-add-rule"></code><button type="button" id="tailnet-add-rule-copy">[[i18n:ui.gs_copy]]</button></div>' +
    '<div class="tailnet-check-actions"><button type="button" id="tailnet-add-policy">[[i18n:ui.tailnet_open_policy]]</button><button type="button" class="gs-link" id="tailnet-add-approve">[[i18n:ui.tailnet_approve_once]]</button></div></div>' +
    '</form>' +
    '<div class="byoc-footer"><p class="byoc-status" id="tailnet-add-status" role="status"></p>' +
    '<button type="button" class="developer-custom-apply" id="tailnet-add-connect">[[i18n:ui.tailnet_connect]]</button></div>';
  document.body?.append(dialog);
  const $ = selector => dialog.querySelector(selector);
  let adding = null;      // the device the dialog adds
  let connecting = false;
  let auth = 'password';  // 'password' | 'key'
  let checkLink = '';     // one-time approval URL from a "check" rule

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
      if (computer.error === 'ssh_check') {
        const fix = el('button', 'gs-link', t('[[i18n:ui.tailnet_open_policy]]'));
        fix.type = 'button';
        fix.onclick = () => send('tailnet-open-url:' + policyURL);
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
    const running = s?.state === 'running' && !requestError;
    const head = el('div', 'runtime-metrics-head');
    const title = el('span', 'runtime-metrics-title-wrap');
    const icon = el('span', 'billing-field-icon tailnet-icon');
    icon.innerHTML = markSVG;
    title.append(icon, el('span', 'runtime-metrics-title', t('[[i18n:ui.tailnet_title]]')));
    head.append(title);
    // Before Tailscale is signed in, Add opens Settings › Tailscale. After,
    // the card lists every tailnet device with its own "Add", so
    // there is no button to unfold them (FEN-123).
    if (s && !running && requestError !== 'login_required') {
      const add = el('button', 'tailnet-head-add');
      add.type = 'button';
      add.innerHTML = svg('<path d="M10 4.5v11M4.5 10h11"/>');
      add.append(el('span', '', t('[[i18n:ui.tailnet_add_worker]]')));
      add.onclick = open;
      head.append(add);
    }
    cardHead.append(head);
    // Say what worker computers are for until the tailnet is set up.
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
    // This Mac is the first worker computer above; then the other workers,
    // then the rest of the tailnet, each with "Add".
    const devices = (Array.isArray(s.devices) ? s.devices : []).filter(d => d.id !== s.this_device);
    const isWorker = d => d.computer && !d.computer.error && d.computer.alias;
    const workers = devices.filter(isWorker);
    const list = el('div', 'tailnet-devices');
    for (const device of [...workers, ...devices.filter(d => !isWorker(d))]) list.append(deviceRow(device, s.this_device));
    if (!devices.length) list.append(el('p', 'tailnet-message tailnet-empty', t('[[i18n:ui.tailnet_no_devices]]')));
    cardBody.append(list);
    window.tailnetComputerCount = workers.length;
    window.updateComputerCount?.();
  }

  function setAuth(value) {
    auth = value;
    for (const b of dialog.querySelectorAll('[data-auth]')) b.setAttribute('aria-checked', String(b.dataset.auth === value));
    $('#tailnet-add-password-field').hidden = value !== 'password';
    $('#tailnet-add-key-field').hidden = value !== 'key';
    $('#tailnet-add-passphrase-field').hidden = value !== 'key';
    $('#tailnet-add-status').textContent = '';
    $('#tailnet-add-status').classList.remove('is-error');
    (value === 'key' ? $('#tailnet-add-key') : $('#tailnet-add-password')).focus();
  }
  for (const b of dialog.querySelectorAll('[data-auth]')) b.onclick = () => setAuth(b.dataset.auth);

  // Secrets never stay in the page longer than the dialog is open.
  function clearSecrets() {
    for (const id of ['#tailnet-add-password', '#tailnet-add-key', '#tailnet-add-passphrase']) $(id).value = '';
  }
  dialog.addEventListener('close', clearSecrets);

  // Tailscale SSH devices need only a user name; the rest of the form hides.
  function setTailscaleSSH(on) {
    dialog.classList.toggle('is-tailscale-ssh', on);
    $('.tailnet-add-intro').hidden = on;
    for (const id of ['.byoc-modes', '#tailnet-add-password-field', '#tailnet-add-key-field', '.tailnet-advanced']) dialog.querySelector(id).hidden = on || (id === '#tailnet-add-password-field' && auth !== 'password') || (id === '#tailnet-add-key-field' && auth !== 'key');
  }
  function showCheck(link) {
    checkLink = link || '';
    $('#tailnet-add-check').hidden = false;
    $('#tailnet-add-approve').hidden = !checkLink;
  }
  $('#tailnet-add-rule').textContent = acceptRule;
  const copy = (text, b) => { send('copy-text:' + text); const label = b.textContent; b.textContent = t('[[i18n:ui.gs_copied]]'); setTimeout(() => { b.textContent = label; }, 1500); };
  $('#tailnet-add-rule-copy').onclick = event => copy(acceptRule, event.currentTarget);
  for (const b of dialog.querySelectorAll('[data-copy]')) b.onclick = () => copy(b.dataset.copy, b);
  $('#tailnet-add-policy').onclick = () => send('tailnet-open-url:' + policyURL);
  $('#tailnet-add-approve').onclick = () => { if (checkLink) send('tailnet-open-url:' + checkLink); };

  function openAdd(device) {
    adding = device;
    const windows = device.os === 'windows';
    const tailscaleSSH = !!device.tailscale_ssh;
    $('#tailnet-add-title').textContent = t('[[i18n:ui.tailnet_add_title]]').replace('{name}', device.name);
    $('#tailnet-add-note').textContent = t(windows ? '[[i18n:ui.tailnet_add_need_windows]]' : device.os === 'macOS' ? '[[i18n:ui.tailnet_add_need_mac]]' : '[[i18n:ui.tailnet_add_need_linux]]');
    const user = $('#tailnet-add-user');
    // The same user name as on this Mac, until the user types another.
    if (!user.value || user.dataset.auto === 'true') { user.value = windows ? '' : (status?.local_user || ''); user.dataset.auto = 'true'; }
    $('#tailnet-add-port').value = '22';
    clearSecrets();
    $('#tailnet-add-status').textContent = '';
    $('#tailnet-add-status').classList.remove('is-error', 'is-busy');
    connecting = false;
    $('#tailnet-add-connect').disabled = false;
    $('#tailnet-add-check').hidden = true;
    $('#tailnet-add-ssh-hint').hidden = tailscaleSSH || device.os !== 'linux';
    if (tailscaleSSH) $('#tailnet-add-note').textContent = t('[[i18n:ui.tailnet_add_tailscale_ssh_note]]');
    if (!dialog.open && typeof dialog.showModal === 'function') dialog.showModal();
    setAuth(auth);
    setTailscaleSSH(tailscaleSSH);
    if (!user.value || tailscaleSSH) user.focus();
  }
  $('#tailnet-add-user').oninput = event => { event.target.dataset.auto = 'false'; };
  $('#tailnet-add-close').onclick = () => dialog.close();
  $('#tailnet-add-form').onsubmit = event => { event.preventDefault(); $('#tailnet-add-connect').click(); };
  $('#tailnet-add-password').onkeydown = event => { if (event.key === 'Enter') { event.preventDefault(); $('#tailnet-add-connect').click(); } };
  $('#tailnet-add-connect').onclick = () => {
    const user = $('#tailnet-add-user').value.trim();
    const state = $('#tailnet-add-status');
    if (!adding || connecting) return;
    const fail = token => { state.classList.remove('is-busy'); state.classList.add('is-error'); state.textContent = t(token); };
    if (!/^[A-Za-z0-9._][A-Za-z0-9._@-]{0,63}$/.test(user)) return fail('[[i18n:ui.tailnet_add_user_needed]]');
    const portText = $('#tailnet-add-port').value.trim() || '22';
    const port = Number(portText);
    if (!/^\d+$/.test(portText) || port < 1 || port > 65535) { $('#tailnet-add-form .tailnet-advanced').open = true; return fail('[[i18n:ui.tailnet_add_port_invalid]]'); }
    const request = { id: adding.id, user, port };
    if (adding.tailscale_ssh) {
      delete request.port;
    } else if (auth === 'key') {
      const key = $('#tailnet-add-key').value.trim();
      if (!key) return fail('[[i18n:ui.tailnet_add_key_needed]]');
      request.private_key = key + '\n';
      const passphrase = $('#tailnet-add-passphrase').value;
      if (passphrase) request.passphrase = passphrase;
    } else {
      const password = $('#tailnet-add-password').value;
      if (!password) return fail('[[i18n:ui.tailnet_add_password_needed]]');
      request.password = password;
    }
    connecting = true;
    $('#tailnet-add-connect').disabled = true;
    state.classList.remove('is-error');
    state.classList.add('is-busy');
    state.textContent = t('[[i18n:ui.tailnet_connecting]]');
    send('tailnet-add:' + JSON.stringify(request));
  };

  window.renderTailnetAdded = result => {
    connecting = false;
    $('#tailnet-add-connect').disabled = false;
    const state = $('#tailnet-add-status');
    const code = result?.error || result?.computer?.error || (!result?.computer ? 'ssh_failed' : '');
    state.classList.remove('is-busy');
    if (code === 'ssh_check') {
      state.classList.remove('is-error');
      state.textContent = '';
      showCheck(result?.computer?.check_url);
      return;
    }
    if (code) {
      state.classList.add('is-error');
      state.textContent = t(addErrors[code] || loginErrors[code] || sshErrors.ssh_failed);
      if (code === 'key_passphrase') $('#tailnet-add-form .tailnet-advanced').open = true;
      return;
    }
    clearSecrets();
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
