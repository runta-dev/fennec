// Minimalist full-screen Sign-in & ~5s Cloud Setup stage.
// Let UI explain itself: Fennec peeks out from the Cloud node linked to Mac.
(() => {
  const t = token => (typeof translate === 'function' ? translate(token) : token);
  const stage = document.createElement('section');
  stage.id = 'onboarding-stage';
  stage.className = 'onboarding-stage';
  stage.hidden = true;

  const foxSVG =
    '<svg class="onboarding-fox-svg" viewBox="0 0 64 64" fill="none" aria-hidden="true">' +
    '<path d="M5 39V9c0-3 2-4 4-2l23 15L55 7c2-2 4-1 4 2v30L34 57c-1.2.9-2.8.9-4 0Z" fill="#FFD8A1"/>' +
    '<path class="ob-ear-l" d="m8 6 24 16-12 9Z" fill="#FFB953"/>' +
    '<path class="ob-ear-r" d="m56 6-24 16 12 9Z" fill="#F7A136"/>' +
    '<path d="M5 39 30 22c1.2-.8 2.8-.8 4 0l25 17-25 18c-1.2.9-2.8.9-4 0Z" fill="#F38D25"/>' +
    '<path d="m5 39 27-17v36L30 57Z" fill="#FFA83F"/>' +
    '<circle class="ob-eye" cx="24" cy="40" r="2.6" fill="#3B332E"/>' +
    '<circle class="ob-eye" cx="40" cy="40" r="2.6" fill="#3B332E"/>' +
    '<ellipse cx="32" cy="52" rx="3.2" ry="3.3" fill="#3B332E"/>' +
    '</svg>';

  const macIconSVG =
    '<svg class="scene-node-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">' +
    '<rect x="4" y="5" width="16" height="11" rx="2"/>' +
    '<path d="M2.5 19h19"/>' +
    '<path d="m9 9.5 2 2-2 2m4 0h2.5"/>' +
    '</svg>';

  const cloudIconSVG =
    '<svg class="scene-node-icon scene-cloud-svg" viewBox="0 0 24 24" fill="none" aria-hidden="true">' +
    '<path d="M7.5 18h9.2a4.3 4.3 0 0 0 .5-8.57A6 6 0 0 0 5.6 8a4.5 4.5 0 0 0 1.9 10Z"/>' +
    '</svg>';

  const bridgeSVG =
    '<svg class="scene-bridge-svg" viewBox="0 0 88 28" fill="none" aria-hidden="true">' +
    '<path d="M6 10h76" class="arch-rail"/>' +
    '<path d="M82 18H6" class="arch-rail"/>' +
    '<circle cx="10" cy="10" r="2.6" class="arch-pulse arch-pulse-fwd"/>' +
    '<circle cx="78" cy="18" r="2.6" class="arch-pulse arch-pulse-rev"/>' +
    '</svg>';

  stage.innerHTML =
    '<div class="onboarding-signin" id="onboarding-signin">' +
    '<div class="onboarding-logo-hit" id="onboarding-logo-hit">' + foxSVG + '</div>' +
    '<div class="onboarding-wordmark"><strong>Fennec</strong><button type="button" class="env-badge" id="onboarding-env-badge" hidden>INT</button></div>' +
    '<button type="button" class="onboarding-cta" id="onboarding-login-btn"></button>' +
    '<p class="onboarding-login-hint" id="onboarding-login-hint" hidden></p>' +
    '</div>' +
    '<div class="onboarding-setup" id="onboarding-setup" hidden>' +
    '<div class="onboarding-scene">' +
    '<div class="scene-col">' +
    '<div class="scene-box scene-box-mac">' + macIconSVG + '</div>' +
    '<span class="scene-caption">Mac</span>' +
    '</div>' +
    '<div class="scene-bridge">' + bridgeSVG + '</div>' +
    '<div class="scene-col">' +
    '<div class="scene-cloud-wrap">' +
    '<div class="onboarding-peek-stage" id="onboarding-peek-stage">' +
    '<div class="onboarding-peek-fox">' + foxSVG + '</div>' +
    '</div>' +
    '<div class="scene-box scene-box-cloud">' +
    '<span class="fox-paw fox-paw-l" aria-hidden="true"></span>' +
    '<span class="fox-paw fox-paw-r" aria-hidden="true"></span>' +
    cloudIconSVG +
    '</div>' +
    '</div>' +
    '<span class="scene-caption">Cloud</span>' +
    '</div>' +
    '</div>' +
    '<div class="onboarding-footer">' +
    '<div class="onboarding-status-title" id="onboarding-stage-text"></div>' +
    '<div class="onboarding-bar"><div class="onboarding-bar-fill" id="onboarding-bar-fill"></div></div>' +
    '<div class="onboarding-action-wrap"><button type="button" class="onboarding-action-btn" id="onboarding-action-btn" hidden></button></div>' +
    '</div>' +
    '</div>';
  document.body?.append(stage);

  const signinPane = stage.querySelector('#onboarding-signin');
  const setupPane = stage.querySelector('#onboarding-setup');
  const logoHit = stage.querySelector('#onboarding-logo-hit');
  const envBadge = stage.querySelector('#onboarding-env-badge');
  const loginBtn = stage.querySelector('#onboarding-login-btn');
  const loginHint = stage.querySelector('#onboarding-login-hint');
  const peekStage = stage.querySelector('#onboarding-peek-stage');
  const barFill = stage.querySelector('#onboarding-bar-fill');
  const stageText = stage.querySelector('#onboarding-stage-text');
  const actionBtn = stage.querySelector('#onboarding-action-btn');

  let isAuthorized = false;
  let signedInTransition = false;
  let setupDismissedForSession = false;
  let loginMessage = '';
  let loginBusy = false;
  let prepState = {};
  let finishTimer = null;
  let delayedSetupTimer = null;
  let asyncHandoffTimer = null;
  let ctrlClicks = [];

  function stageProgress(st) {
    const weights = {
      allocating: 22,
      starting: 42,
      connecting: 62,
      checking_resources: 72,
      installing_session_service: 80,
      installing_magpie: 84,
      starting_magpie: 88,
      migrating_credentials: 92,
      agents_installed: 100
    };
    return weights[st] || 18;
  }

  function stageLabel(st, status) {
    if (st === 'offline' || status === 'network_offline') return t('[[i18n:ui.offline_waiting_for_network]]');
    if (status === 'billing_restricted') return t('[[i18n:ui.add_credits_in_runta_to_create_cloud_computer]]');
    if (st === 'failed') return t('[[i18n:ui.environment_preparation_failed]]');
    if (st === 'agents_installed') return t('[[i18n:ui.cloud_computer_ready]]');
    return t('[[i18n:ui.preparing_cloud_computer]]');
  }

  function syncEnvironmentBadge(envName, apiEndpoint) {
    if (!envBadge) return;
    const nonProd = envName && envName !== 'production';
    envBadge.hidden = !nonProd;
    if (nonProd) {
      envBadge.textContent = envName === 'integration' ? 'INT' : 'DEV';
      envBadge.title = (apiEndpoint || envName) + ' · ' + t('[[i18n:ui.developer_options]]');
    }
  }

  function setMainChromeHidden(hidden) {
    const wasHidden = document.body?.classList.contains('is-onboarding');
    document.body?.classList.toggle('is-onboarding', hidden);
    if (wasHidden && !hidden) {
      window.syncTabIndicator?.();
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => window.syncTabIndicator?.());
    }
  }

  function renderOnboarding() {
    if (!isAuthorized) {
      if (delayedSetupTimer) { clearTimeout(delayedSetupTimer); delayedSetupTimer = null; }
      if (asyncHandoffTimer) { clearTimeout(asyncHandoffTimer); asyncHandoffTimer = null; }
      stage.hidden = false;
      stage.classList.remove('is-finishing');
      signinPane.hidden = false;
      setupPane.hidden = true;
      setMainChromeHidden(true);
      loginBtn.disabled = loginBusy;
      loginBtn.textContent = loginBusy ? t('[[i18n:ui.connecting_to_runta]]') : t('[[i18n:ui.sign_in_to_runta]]');
      loginHint.hidden = !loginMessage;
      loginHint.textContent = loginMessage || '';
      return;
    }

    if (setupDismissedForSession) {
      stage.hidden = true;
      setMainChromeHidden(false);
      return;
    }

    const st = prepState.stage || '';
    const isFailed = st === 'failed';
    const isOffline = st === 'offline' || prepState.status === 'network_offline';
    const done = st === 'agents_installed' || st === 'disabled';
    if (!done && finishTimer) {
      clearTimeout(finishTimer);
      finishTimer = null;
      stage.classList.remove('is-finishing');
    }
    if (done && !signedInTransition && stage.hidden) {
      setupDismissedForSession = true;
      setMainChromeHidden(false);
      return;
    }

    if (done) {
      if (asyncHandoffTimer) { clearTimeout(asyncHandoffTimer); asyncHandoffTimer = null; }
      if (barFill?.style) barFill.style.width = '100%';
      stageText.textContent = stageLabel('agents_installed', '');
      actionBtn.hidden = true;
      if (!finishTimer) {
        stage.classList.add('is-finishing');
        finishTimer = setTimeout(() => {
          finishTimer = null;
          setupDismissedForSession = true;
          signedInTransition = false;
          stage.hidden = true;
          stage.classList.remove('is-finishing');
          setMainChromeHidden(false);
        }, 380);
      }
      return;
    }

    // Show setup screen immediately after interactive sign-in, or if initial
    // cold runtime setup takes longer than 300ms on launch.
    if (!signedInTransition && stage.hidden) {
      if (!st || st === 'idle') return;
      if (!delayedSetupTimer) {
        delayedSetupTimer = setTimeout(() => {
          delayedSetupTimer = null;
          if (!setupDismissedForSession && isAuthorized) {
            signedInTransition = true;
            renderOnboarding();
          }
        }, 300);
      }
      return;
    }

    stage.hidden = false;
    signinPane.hidden = true;
    setupPane.hidden = false;
    setMainChromeHidden(true);

    // Bounded ~5s onboarding experience: once Runta has allocated the cloud
    // computer (runtime_id assigned, no error/offline), smoothly hand off to the
    // main workspace after ~5s while remaining cold-boot/install work runs async!
    if (!isFailed && !isOffline && prepState.runtime_id && !asyncHandoffTimer && !finishTimer) {
      asyncHandoffTimer = setTimeout(() => {
        asyncHandoffTimer = null;
        if (isAuthorized && prepState.stage !== 'failed' && prepState.stage !== 'offline') {
          if (barFill?.style) barFill.style.width = '100%';
          stage.classList.add('is-finishing');
          finishTimer = setTimeout(() => {
            finishTimer = null;
            setupDismissedForSession = true;
            signedInTransition = false;
            stage.hidden = true;
            stage.classList.remove('is-finishing');
            setMainChromeHidden(false);
          }, 360);
        }
      }, 4600);
    }
    if (isFailed || isOffline) {
      if (asyncHandoffTimer) { clearTimeout(asyncHandoffTimer); asyncHandoffTimer = null; }
    }

    const isBilling = prepState.status === 'billing_restricted';
    peekStage?.classList.toggle('is-paused', isFailed || isOffline);
    const pct = isFailed ? 100 : stageProgress(st);
    if (barFill?.style) barFill.style.width = pct + '%';
    barFill?.classList.toggle('is-error', isFailed && !isBilling);
    stageText.textContent = stageLabel(st, prepState.status || '');

    if (isBilling) {
      actionBtn.hidden = false;
      actionBtn.textContent = t('[[i18n:ui.open_in_browser]]');
      actionBtn.onclick = () => send('open-dashboard');
    } else if (isFailed) {
      actionBtn.hidden = false;
      actionBtn.textContent = t('[[i18n:ui.retry]]');
      actionBtn.onclick = () => send('retry-runtime');
    } else {
      actionBtn.hidden = true;
    }
  }

  loginBtn.onclick = () => {
    if (!loginBusy) send('login');
  };
  if (envBadge) {
    envBadge.onclick = event => {
      event.stopPropagation();
      window.openDeveloperOptions?.();
    };
  }
  if (logoHit) {
    logoHit.addEventListener('mousedown', event => {
      if (!event.ctrlKey) {
        ctrlClicks = [];
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      const now = Date.now();
      ctrlClicks = ctrlClicks.filter(ts => now - ts <= 1800);
      ctrlClicks.push(now);
      if (ctrlClicks.length >= 5) {
        ctrlClicks = [];
        window.openDeveloperOptions?.();
      }
    });
  }

  // Wrap existing panel hooks cleanly without altering panel.js unit contracts.
  const origSignedOut = window.renderSignedOut;
  window.renderSignedOut = () => {
    origSignedOut?.();
    isAuthorized = false;
    signedInTransition = false;
    setupDismissedForSession = false;
    prepState = {};
    if (finishTimer) { clearTimeout(finishTimer); finishTimer = null; }
    renderOnboarding();
  };

  const origAuthorized = window.renderAuthorized;
  window.renderAuthorized = () => {
    const wasSignedOut = !isAuthorized && !stage.hidden;
    origAuthorized?.();
    isAuthorized = true;
    if (wasSignedOut) {
      signedInTransition = true;
      setupDismissedForSession = false;
    }
    renderOnboarding();
  };

  const origLogin = window.renderLogin;
  window.renderLogin = (message, busy) => {
    origLogin?.(message, busy);
    loginMessage = message || '';
    loginBusy = !!busy;
    if (!isAuthorized) renderOnboarding();
  };

  const origPrep = window.renderPreparation;
  window.renderPreparation = st => {
    origPrep?.(st);
    prepState = st || {};
    renderOnboarding();
  };

  const origDevPrefs = window.renderDeveloperPreferences;
  window.renderDeveloperPreferences = prefs => {
    origDevPrefs?.(prefs);
    if (prefs) syncEnvironmentBadge(prefs.environment || 'production', prefs.api_endpoint || '');
  };

  const origPrefs = window.renderPreferences;
  window.renderPreferences = prefs => {
    origPrefs?.(prefs);
    if (prefs) syncEnvironmentBadge(prefs.environment || 'production', prefs.api_endpoint || '');
    if (!stage.hidden) {
      renderOnboarding();
    }
  };

  window.showOnboardingPreview = mode => {
    if (finishTimer) { clearTimeout(finishTimer); finishTimer = null; stage.classList.remove('is-finishing'); }
    if (asyncHandoffTimer) { clearTimeout(asyncHandoffTimer); asyncHandoffTimer = null; }
    if (delayedSetupTimer) { clearTimeout(delayedSetupTimer); delayedSetupTimer = null; }
    if (mode === 'signin') {
      isAuthorized = false;
      setupDismissedForSession = false;
      renderOnboarding();
    } else if (mode === 'setup') {
      isAuthorized = true;
      signedInTransition = true;
      setupDismissedForSession = false;
      prepState = { stage: 'connecting', status: '' };
      renderOnboarding();
    }
  };

  renderOnboarding();
})();
