// Full-screen sign-in splash and ~5s cloud setup value-narrative onboarding
// with peeking Fennec mascot and live Edge <-> Cloud SVG pulse diagram.
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

  const diagramSVG =
    '<svg class="onboarding-arch-svg" viewBox="0 0 320 76" fill="none" aria-hidden="true">' +
    '<g class="arch-node arch-edge">' +
    '<rect x="12" y="14" width="84" height="48" rx="11" class="arch-box"/>' +
    '<rect x="39" y="23" width="30" height="19" rx="2.5" class="arch-stroke"/>' +
    '<path d="M34 45h40" class="arch-stroke"/>' +
    '</g>' +
    '<g class="arch-link">' +
    '<path d="M104 33h112" class="arch-rail"/>' +
    '<path d="M216 43H104" class="arch-rail"/>' +
    '<circle cx="110" cy="33" r="3" class="arch-pulse arch-pulse-fwd"/>' +
    '<circle cx="210" cy="43" r="2.6" class="arch-pulse arch-pulse-rev"/>' +
    '</g>' +
    '<g class="arch-node arch-cloud">' +
    '<rect x="224" y="14" width="84" height="48" rx="11" class="arch-box arch-box-cloud"/>' +
    '<path d="M254 44h25a7 7 0 0 0 .8-13.9A9.5 9.5 0 0 0 262 24a8.5 8.5 0 0 0-8.2 6.4A6.8 6.8 0 0 0 254 44Z" class="arch-cloud-icon"/>' +
    '</g>' +
    '</svg>';

  stage.innerHTML =
    '<div class="onboarding-signin" id="onboarding-signin">' +
    '<div class="onboarding-logo-hit" id="onboarding-logo-hit">' + foxSVG + '</div>' +
    '<div class="onboarding-wordmark"><strong>Fennec</strong><button type="button" class="env-badge" id="onboarding-env-badge" hidden>INT</button></div>' +
    '<p class="onboarding-tagline" id="onboarding-tagline"></p>' +
    '<button type="button" class="onboarding-cta" id="onboarding-login-btn"></button>' +
    '<p class="onboarding-login-hint" id="onboarding-login-hint" hidden></p>' +
    '</div>' +
    '<div class="onboarding-setup" id="onboarding-setup" hidden>' +
    '<div class="onboarding-arch-wrap">' +
    diagramSVG +
    '<div class="onboarding-arch-labels"><span id="arch-edge-label"></span><span id="arch-cloud-label"></span></div>' +
    '</div>' +
    '<div class="onboarding-story">' +
    '<strong class="onboarding-story-title" id="onboarding-story-title"></strong>' +
    '<p class="onboarding-story-body" id="onboarding-story-body"></p>' +
    '<div class="onboarding-dots" id="onboarding-dots"><span class="active"></span><span></span><span></span></div>' +
    '</div>' +
    '<div class="onboarding-footer">' +
    '<div class="onboarding-peek-stage" id="onboarding-peek-stage"><div class="onboarding-peek-fox">' + foxSVG + '</div></div>' +
    '<div class="onboarding-bar"><div class="onboarding-bar-fill" id="onboarding-bar-fill"></div></div>' +
    '<div class="onboarding-status-row"><span id="onboarding-stage-text"></span><button type="button" class="onboarding-action-btn" id="onboarding-action-btn" hidden></button></div>' +
    '</div>' +
    '</div>';
  document.body?.append(stage);

  const signinPane = stage.querySelector('#onboarding-signin');
  const setupPane = stage.querySelector('#onboarding-setup');
  const logoHit = stage.querySelector('#onboarding-logo-hit');
  const envBadge = stage.querySelector('#onboarding-env-badge');
  const taglineEl = stage.querySelector('#onboarding-tagline');
  const loginBtn = stage.querySelector('#onboarding-login-btn');
  const loginHint = stage.querySelector('#onboarding-login-hint');
  const edgeLabel = stage.querySelector('#arch-edge-label');
  const cloudLabel = stage.querySelector('#arch-cloud-label');
  const storyTitle = stage.querySelector('#onboarding-story-title');
  const storyBody = stage.querySelector('#onboarding-story-body');
  const storyDots = [...stage.querySelectorAll('#onboarding-dots span')];
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
  let storyIndex = 0;
  let storyTimer = null;
  let finishTimer = null;
  let delayedSetupTimer = null;
  let asyncHandoffTimer = null;
  let ctrlClicks = [];

  const stories = () => [
    {
      title: t('[[i18n:ui.onboarding_story_1_title]]'),
      body: t('[[i18n:ui.onboarding_story_1_body]]')
    },
    {
      title: t('[[i18n:ui.onboarding_story_2_title]]'),
      body: t('[[i18n:ui.onboarding_story_2_body]]')
    },
    {
      title: t('[[i18n:ui.onboarding_story_3_title]]'),
      body: t('[[i18n:ui.onboarding_story_3_body]]')
    }
  ];

  function syncStorySlide() {
    const list = stories();
    const item = list[storyIndex % list.length];
    storyTitle.textContent = item.title;
    storyBody.textContent = item.body;
    storyDots.forEach((dot, idx) => dot.classList.toggle('active', idx === storyIndex % list.length));
  }

  function startStoryCarousel() {
    if (storyTimer) return;
    storyIndex = 0;
    syncStorySlide();
    storyTimer = setInterval(() => {
      storyIndex = (storyIndex + 1) % 3;
      syncStorySlide();
    }, 1850);
  }

  function stopStoryCarousel() {
    if (storyTimer) {
      clearInterval(storyTimer);
      storyTimer = null;
    }
  }

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
    const map = {
      allocating: '[[i18n:ui.allocating_cloud_computer]]',
      starting: '[[i18n:ui.starting_cloud_computer]]',
      connecting: '[[i18n:ui.connecting_to_cloud_computer]]',
      installing_session_service: '[[i18n:ui.preparing_session_service]]',
      migrating_credentials: '[[i18n:ui.migrating_model_credentials]]',
      agents_installed: '[[i18n:ui.cloud_computer_ready]]'
    };
    return t(map[st] || '[[i18n:ui.preparing_2]]');
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
    if (wasHidden && !hidden && typeof Event === 'function') {
      window.dispatchEvent?.(new Event('resize'));
    }
  }

  function renderOnboarding() {
    taglineEl.textContent = t('[[i18n:ui.onboarding_signin_tagline]]');
    edgeLabel.textContent = t('[[i18n:ui.edge_connector]]');
    cloudLabel.textContent = t('[[i18n:ui.cloud_agent_loop]]');

    if (!isAuthorized) {
      stopStoryCarousel();
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
      stopStoryCarousel();
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
          stopStoryCarousel();
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
    startStoryCarousel();

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
            stopStoryCarousel();
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
      syncStorySlide();
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
