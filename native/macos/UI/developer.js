// Developer debug modal: Ctrl + 5 clicks on the Fennec avatar switches Runta
// environment (production / integration / custom HTTPS) and SSH TCP_NODELAY.
(() => {
  const brand = document.querySelector('.brand');
  const badge = document.querySelector('#env-badge');
  const REQUIRED_CLICKS = 5;
  const CLICK_WINDOW_MS = 1800;

  let ctrlClicks = [];
  let state = {
    environment: 'production',
    api_endpoint: 'https://api.runta.com',
    dashboard_url: 'https://dashboard.runta.com',
    ssh_host: 'ssh.runta.com',
    ssh_nodelay: true
  };

  const t = token => (typeof translate === 'function' ? translate(token) : token);

  const dialog = document.createElement('dialog');
  dialog.id = 'developer-dialog';
  dialog.className = 'developer-dialog';
  dialog.setAttribute('aria-labelledby', 'developer-dialog-title');
  dialog.innerHTML =
    '<div class="developer-dialog-head">' +
    '<strong id="developer-dialog-title"></strong>' +
    '<button type="button" class="developer-close" aria-label="Close">×</button>' +
    '</div>' +
    '<div class="developer-section-label" id="developer-env-label"></div>' +
    '<div class="developer-segments" role="radiogroup" aria-labelledby="developer-env-label">' +
    '<button type="button" role="radio" data-env="production" aria-checked="true">Production</button>' +
    '<button type="button" role="radio" data-env="integration" aria-checked="false">Integration</button>' +
    '<button type="button" role="radio" data-env="custom" aria-checked="false"></button>' +
    '</div>' +
    '<form class="developer-custom-form" hidden>' +
    '<input type="url" class="developer-custom-input" placeholder="https://api.runta.xyz" spellcheck="false" autocomplete="off">' +
    '<button type="submit" class="developer-custom-apply"></button>' +
    '</form>' +
    '<div class="developer-endpoints">' +
    '<div class="developer-endpoint-row"><span>API</span><code id="developer-api-value"></code></div>' +
    '<div class="developer-endpoint-row"><span>SSH</span><code id="developer-ssh-value"></code></div>' +
    '</div>' +
    '<div class="developer-toggle-row">' +
    '<span id="developer-nodelay-label">SSH TCP_NODELAY</span>' +
    '<button type="button" class="switch" id="developer-nodelay" role="switch" aria-checked="true" aria-labelledby="developer-nodelay-label"></button>' +
    '</div>';
  document.body?.append(dialog);

  const titleEl = dialog.querySelector('#developer-dialog-title');
  const closeBtn = dialog.querySelector('.developer-close');
  const envLabel = dialog.querySelector('#developer-env-label');
  const prodBtn = dialog.querySelector('[data-env="production"]');
  const intBtn = dialog.querySelector('[data-env="integration"]');
  const customBtn = dialog.querySelector('[data-env="custom"]');
  const customForm = dialog.querySelector('.developer-custom-form');
  const customInput = dialog.querySelector('.developer-custom-input');
  const customApply = dialog.querySelector('.developer-custom-apply');
  const apiValue = dialog.querySelector('#developer-api-value');
  const sshValue = dialog.querySelector('#developer-ssh-value');
  const nodelayBtn = dialog.querySelector('#developer-nodelay');

  let customExpanded = false;

  function modeOf(envName) {
    if (!envName || envName === 'production') return 'production';
    if (envName === 'integration') return 'integration';
    return 'custom';
  }

  function derivePreviewEndpoints(spec) {
    if (!spec || spec === 'production') {
      return { api: 'https://api.runta.com', ssh: 'ssh.runta.com' };
    }
    if (spec === 'integration') {
      return { api: 'https://api.runta.xyz', ssh: 'ssh.runta.xyz' };
    }
    try {
      const u = new URL(spec);
      if (u.protocol === 'https:' && u.hostname) {
        const zone = u.hostname.replace(/^api\./, '');
        return { api: 'https://' + u.host, ssh: (zone === u.hostname ? u.hostname : 'ssh.' + zone) };
      }
    } catch (_) {}
    return { api: state.api_endpoint, ssh: state.ssh_host };
  }

  function applyEnvironment(nextSpec) {
    const derived = derivePreviewEndpoints(nextSpec);
    state = {
      ...state,
      environment: nextSpec || 'production',
      api_endpoint: derived.api,
      ssh_host: derived.ssh
    };
    customExpanded = modeOf(state.environment) === 'custom';
    draw();
    send('settings:' + JSON.stringify({ environment: nextSpec || 'production' }));
  }

  function draw() {
    titleEl.textContent = t('[[i18n:ui.developer_options]]');
    envLabel.textContent = t('[[i18n:ui.environment]]');
    customBtn.textContent = t('[[i18n:ui.custom]]');
    customApply.textContent = t('[[i18n:ui.apply]]');

    const mode = customExpanded ? 'custom' : modeOf(state.environment);
    prodBtn.setAttribute('aria-checked', String(mode === 'production'));
    intBtn.setAttribute('aria-checked', String(mode === 'integration'));
    customBtn.setAttribute('aria-checked', String(mode === 'custom'));
    customForm.hidden = mode !== 'custom';
    if (mode === 'custom' && !customInput.value) {
      customInput.value = state.api_endpoint || '';
    }
    apiValue.textContent = state.api_endpoint || 'https://api.runta.com';
    sshValue.textContent = state.ssh_host || 'ssh.runta.com';
    nodelayBtn.setAttribute('aria-checked', String(state.ssh_nodelay !== false));

    if (badge) {
      const nonProd = state.environment && state.environment !== 'production';
      badge.hidden = !nonProd;
      if (nonProd) {
        badge.textContent = state.environment === 'integration' ? 'INT' : 'DEV';
        badge.title = (state.api_endpoint || state.environment) + ' · ' + t('[[i18n:ui.developer_options]]');
      }
    }
  }

  function openDeveloperDialog() {
    customExpanded = modeOf(state.environment) === 'custom';
    if (customExpanded) customInput.value = state.api_endpoint || '';
    draw();
    if (!dialog.open && typeof dialog.showModal === 'function') {
      dialog.showModal();
    }
    const activeBtn = modeOf(state.environment) === 'integration' ? intBtn : modeOf(state.environment) === 'custom' ? customBtn : prodBtn;
    activeBtn?.focus?.();
  }

  closeBtn.onclick = () => dialog.close();
  prodBtn.onclick = () => { customExpanded = false; applyEnvironment('production'); };
  intBtn.onclick = () => { customExpanded = false; applyEnvironment('integration'); };
  customBtn.onclick = () => {
    customExpanded = true;
    if (!customInput.value || customInput.value === 'https://api.runta.com' || customInput.value === 'https://api.runta.xyz') {
      customInput.value = state.api_endpoint || 'https://api.runta.xyz';
    }
    draw();
    customInput.focus();
    customInput.select?.();
  };
  customForm.onsubmit = event => {
    event.preventDefault();
    const raw = String(customInput.value || '').trim();
    if (!raw.startsWith('https://')) {
      customInput.focus();
      return;
    }
    applyEnvironment(raw);
  };
  nodelayBtn.onclick = () => {
    const next = state.ssh_nodelay === false;
    state = { ...state, ssh_nodelay: next };
    draw();
    send('settings:' + JSON.stringify({ ssh_nodelay: next }));
  };

  function recordCtrlClick(event) {
    if (!event.ctrlKey) {
      ctrlClicks = [];
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    const now = Date.now();
    ctrlClicks = ctrlClicks.filter(ts => now - ts <= CLICK_WINDOW_MS);
    ctrlClicks.push(now);
    if (ctrlClicks.length >= REQUIRED_CLICKS) {
      ctrlClicks = [];
      openDeveloperDialog();
    }
  }

  if (brand) {
    brand.addEventListener('mousedown', recordCtrlClick);
  }
  document.addEventListener?.('contextmenu', event => {
    const tag = String(event.target?.tagName || '').toLowerCase();
    if (tag !== 'input' && tag !== 'textarea') event.preventDefault();
  });
  if (badge) {
    badge.addEventListener('mousedown', event => event.stopPropagation());
    badge.onclick = event => {
      event.stopPropagation();
      openDeveloperDialog();
    };
  }

  window.renderDeveloperPreferences = prefs => {
    if (!prefs || typeof prefs !== 'object') return;
    const envName = prefs.environment || 'production';
    const derived = derivePreviewEndpoints(envName);
    state = {
      environment: envName,
      api_endpoint: prefs.api_endpoint || derived.api,
      dashboard_url: prefs.dashboard_url || state.dashboard_url,
      ssh_host: prefs.ssh_host || derived.ssh,
      ssh_nodelay: prefs.ssh_nodelay !== undefined ? prefs.ssh_nodelay !== false : prefs.no_ssh_nodelay !== true
    };
    draw();
  };
  window.openDeveloperOptions = openDeveloperDialog;
  window.rerenderDeveloper = draw;
  draw();
})();
