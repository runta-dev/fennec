// Crafted full-screen Sign-in & ~5s Cloud Setup stage.
// Let UI explain itself: local Mac CLI streams into an always-on Cloud pod
// running Claude / Codex / Pi while Fennec peeks out from behind the Cloud pod.
(() => {
  const t = token => (typeof translate === 'function' ? translate(token) : token);
  const stage = document.createElement('section');
  stage.id = 'onboarding-stage';
  stage.className = 'onboarding-stage';
  stage.hidden = true;

  const makeFoxSVG = cls =>
    '<svg class="onboarding-fox-svg ' + cls + '" viewBox="0 0 64 64" fill="none" aria-hidden="true">' +
    '<path class="ob-fox-base" d="M5 39V9c0-3 2-4 4-2l23 15L55 7c2-2 4-1 4 2v30L34 57c-1.2.9-2.8.9-4 0Z" fill="#FFD8A1"/>' +
    '<path class="ob-fox-ear-l" d="m8 6 24 16-12 9Z" fill="#FFB953"/>' +
    '<path class="ob-fox-ear-r" d="m56 6-24 16 12 9Z" fill="#F7A136"/>' +
    '<path d="M5 39 30 22c1.2-.8 2.8-.8 4 0l25 17-25 18c-1.2.9-2.8.9-4 0Z" fill="#F38D25"/>' +
    '<path d="m5 39 27-17v36L30 57Z" fill="#FFA83F"/>' +
    '<circle class="ob-fox-eye" cx="24" cy="40" r="2.6" fill="#3B332E"/>' +
    '<circle class="ob-fox-eye" cx="40" cy="40" r="2.6" fill="#3B332E"/>' +
    '<ellipse cx="32" cy="52" rx="3.2" ry="3.3" fill="#3B332E"/>' +
    '</svg>';

  const cloudMiniSVG =
    '<svg class="pod-cloud-icon" viewBox="0 0 20 20" fill="none" aria-hidden="true">' +
    '<path d="M5.8 15.2h8.6a3.4 3.4 0 0 0 .35-6.78A4.9 4.9 0 0 0 5.1 7.2a3.9 3.9 0 0 0 .7 8Z"/>' +
    '</svg>';

  const codexMarkSVG =
    '<svg class="pod-agent-svg" viewBox="0 0 24 24" fill="none" aria-hidden="true">' +
    '<defs><linearGradient id="ob-codex-g" x1="12" x2="12" y1="3" y2="21" gradientUnits="userSpaceOnUse"><stop stop-color="#9B90FF"/><stop offset=".5" stop-color="#6B8CFF"/><stop offset="1" stop-color="#434BFF"/></linearGradient></defs>' +
    '<path d="M9.064 3.344a4.578 4.578 0 012.285-.312c1 .115 1.891.54 2.673 1.275.01.01.024.017.037.021a.09.09 0 00.043 0 4.55 4.55 0 013.046.275l.047.022.116.057a4.581 4.581 0 012.188 2.399c.209.51.313 1.041.315 1.595a4.24 4.24 0 01-.134 1.223.123.123 0 00.03.115c.594.607.988 1.33 1.183 2.17.289 1.425-.007 2.71-.887 3.854l-.136.166a4.548 4.548 0 01-2.201 1.388.123.123 0 00-.081.076c-.191.551-.383 1.023-.74 1.494-.9 1.187-2.222 1.846-3.711 1.838-1.187-.006-2.239-.44-3.157-1.302a.107.107 0 00-.105-.024c-.388.125-.78.143-1.204.138a4.441 4.441 0 01-1.945-.466 4.544 4.544 0 01-1.61-1.335c-.152-.202-.303-.392-.414-.617a5.81 5.81 0 01-.37-.961 4.582 4.582 0 01-.014-2.298.124.124 0 00.006-.056.085.085 0 00-.027-.048 4.467 4.467 0 01-1.034-1.651 3.896 3.896 0 01-.251-1.192 5.189 5.189 0 01.141-1.6c.337-1.112.982-1.985 1.933-2.618.212-.141.413-.251.601-.33.215-.089.43-.164.646-.227a.098.098 0 00.065-.066 4.51 4.51 0 01.829-1.615 4.535 4.535 0 011.837-1.388zm3.482 10.565a.637.637 0 000 1.272h3.636a.637.637 0 100-1.272h-3.636zM8.462 9.23a.637.637 0 00-1.106.631l1.272 2.224-1.266 2.136a.636.636 0 101.095.649l1.454-2.455a.636.636 0 00.005-.64L8.462 9.23z" fill="url(#ob-codex-g)"/>' +
    '</svg>';

  const conduitSVG =
    '<svg class="scene-conduit-svg" viewBox="0 0 60 56" fill="none" aria-hidden="true">' +
    '<defs>' +
    '<path id="ob-line-top" d="M 3 23 H 57"/>' +
    '<path id="ob-line-bot" d="M 57 33 H 3"/>' +
    '</defs>' +
    '<use href="#ob-line-top" class="conduit-rail"/>' +
    '<use href="#ob-line-bot" class="conduit-rail"/>' +
    '<circle r="2.4" class="conduit-orb">' +
    '<animateMotion dur="1.7s" repeatCount="indefinite" keyPoints="0;1" keyTimes="0;1" calcMode="spline" keySplines="0.4 0 0.2 1"><mpath href="#ob-line-top"/></animateMotion>' +
    '</circle>' +
    '<circle r="2.4" class="conduit-orb conduit-orb-soft">' +
    '<animateMotion dur="1.7s" begin="-0.85s" repeatCount="indefinite" keyPoints="0;1" keyTimes="0;1" calcMode="spline" keySplines="0.4 0 0.2 1"><mpath href="#ob-line-bot"/></animateMotion>' +
    '</circle>' +
    '</svg>';

  stage.innerHTML =
    '<div class="onboarding-signin" id="onboarding-signin">' +
    '<div class="onboarding-logo-hit" id="onboarding-logo-hit">' + makeFoxSVG('ob-fox-signin') + '</div>' +
    '<div class="onboarding-wordmark"><strong>Fennec</strong><button type="button" class="env-badge" id="onboarding-env-badge" hidden>INT</button></div>' +
    '<p class="onboarding-tagline" id="onboarding-tagline"></p>' +
    '<div class="onboarding-cta-wrap">' +
    '<button type="button" class="onboarding-cta" id="onboarding-login-btn"></button>' +
    '<p class="onboarding-login-hint" id="onboarding-login-hint" hidden></p>' +
    '</div>' +
    '</div>' +
    '<div class="onboarding-setup" id="onboarding-setup" hidden>' +
    '<div class="onboarding-scene">' +
    '<div class="scene-col scene-col-mac">' +
    '<div class="edge-term-card">' +
    '<div class="edge-term-bar"><span class="term-dot"></span><span class="term-dot"></span><span class="term-dot"></span><span class="term-bar-label">~</span></div>' +
    '<div class="edge-term-body">' +
    '<div class="edge-term-line"><span class="term-prompt">❯</span><span class="term-cmd" id="onboarding-term-cmd">codex</span><span class="term-caret"></span></div>' +
    '</div>' +
    '</div>' +
    '<span class="scene-caption">Mac</span>' +
    '</div>' +
    '<div class="scene-bridge">' + conduitSVG + '</div>' +
    '<div class="scene-col scene-col-cloud">' +
    '<div class="cloud-pod-wrap">' +
    '<div class="onboarding-peek-stage" id="onboarding-peek-stage">' +
    '<div class="onboarding-peek-fox">' + makeFoxSVG('ob-fox-setup') + '</div>' +
    '</div>' +
    '<div class="cloud-pod-card">' +
    '<div class="cloud-pod-head">' +
    '<span class="cloud-pod-title">' + cloudMiniSVG + '<span id="onboarding-always-on"></span></span>' +
    '<span class="cloud-pod-live" aria-hidden="true"></span>' +
    '</div>' +
    '<div class="cloud-pod-agents">' +
    '<span class="pod-agent-tile is-active" data-agent-slot="0">' + codexMarkSVG + '</span>' +
    '<span class="pod-agent-tile" data-agent-slot="1"><img src="icons/claude-color.svg" alt=""/></span>' +
    '<span class="pod-agent-tile" data-agent-slot="2"><img src="icons/pi.svg" class="pod-pi-icon" alt=""/></span>' +
    '</div>' +
    '</div>' +
    '</div>' +
    '<span class="scene-caption scene-caption-cloud" id="onboarding-cloud-caption"></span>' +
    '</div>' +
    '</div>' +
    '<div class="onboarding-story">' +
    '<strong class="onboarding-story-title" id="onboarding-story-title"></strong>' +
    '</div>' +
    '<div class="onboarding-footer">' +
    '<div class="onboarding-bar"><div class="onboarding-bar-fill" id="onboarding-bar-fill"></div></div>' +
    '<div class="onboarding-feedback-slot">' +
    '<div class="onboarding-status-title" id="onboarding-stage-text" hidden></div>' +
    '<div class="onboarding-action-wrap"><button type="button" class="onboarding-action-btn" id="onboarding-action-btn" hidden></button></div>' +
    '</div>' +
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
  const termCmdEl = stage.querySelector('#onboarding-term-cmd');
  const alwaysOnEl = stage.querySelector('#onboarding-always-on');
  const cloudCaptionEl = stage.querySelector('#onboarding-cloud-caption');
  const storyTitle = stage.querySelector('#onboarding-story-title');
  const agentTiles = [...(stage.querySelectorAll?.('.pod-agent-tile') || [])];
  const peekStage = stage.querySelector('#onboarding-peek-stage');
  const barFill = stage.querySelector('#onboarding-bar-fill');
  const stageText = stage.querySelector('#onboarding-stage-text');
  const actionBtn = stage.querySelector('#onboarding-action-btn');
  const termCommands = ['codex', 'claude', 'pi'];

  // Authentic vector ear-morph + blink keyframes matching Fennec's native mascot.
  const foxKeyframes = [
    [0.00, 0.0, 0.0, 1.0, 0.0],
    [0.12, 1.0, 0.0, 1.0, -2.2],
    [0.24, 0.0, 0.0, 1.0, 0.0],
    [0.40, 0.0, 1.0, 1.0, 2.2],
    [0.54, 0.0, 0.0, 1.0, 0.0],
    [0.74, 0.0, 0.0, 1.0, 0.0],
    [0.80, 0.0, 0.0, 0.12, 0.0],
    [0.87, 0.0, 0.0, 1.0, 0.0],
    [1.00, 0.0, 0.0, 1.0, 0.0]
  ];
  let foxRAF = null;

  function applyFoxPose(svg, left, right, eye, tilt) {
    if (!svg?.querySelector) return;
    if (svg.style) svg.style.transform = tilt ? `rotate(${tilt.toFixed(2)}deg)` : '';
    const lx = left * 4.27, ly = left * 4.98, rx = right * 4.27, ry = right * 4.98;
    svg.querySelector('.ob-fox-base')?.setAttribute(
      'd',
      `M5 39L${(5 + lx).toFixed(2)} ${(9 + ly).toFixed(2)}C${(5 + lx).toFixed(2)} ${(6 + ly).toFixed(2)} ${(7 + lx).toFixed(2)} ${(5 + ly).toFixed(2)} ${(9 + lx).toFixed(2)} ${(7 + ly).toFixed(2)}L32 22L${(55 - rx).toFixed(2)} ${(7 + ry).toFixed(2)}C${(57 - rx).toFixed(2)} ${(5 + ry).toFixed(2)} ${(59 - rx).toFixed(2)} ${(6 + ry).toFixed(2)} ${(59 - rx).toFixed(2)} ${(9 + ry).toFixed(2)}L59 39L34 57c-1.2.9-2.8.9-4 0Z`
    );
    svg.querySelector('.ob-fox-ear-l')?.setAttribute('d', `M${(8 + lx).toFixed(2)} ${(6 + ly).toFixed(2)} 32 22 20 31Z`);
    svg.querySelector('.ob-fox-ear-r')?.setAttribute('d', `M${(56 - rx).toFixed(2)} ${(6 + ry).toFixed(2)} 32 22 44 31Z`);
    const eyes = svg.querySelectorAll?.('.ob-fox-eye') || [];
    eyes.forEach(node => { if (node.style) node.style.transform = `scaleY(${eye.toFixed(2)})`; });
  }

  function ensureFoxLoop() {
    if (foxRAF || typeof requestAnimationFrame !== 'function') return;
    if (typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    const cycleMs = 3100;
    const started = Date.now();
    const tick = () => {
      if (stage.hidden || document.hidden) {
        foxRAF = null;
        return;
      }
      const elapsed = (Date.now() - started) % cycleMs;
      const p = elapsed / cycleMs;
      let idx = 1;
      while (idx < foxKeyframes.length - 1 && foxKeyframes[idx][0] < p) idx++;
      const a = foxKeyframes[idx - 1];
      const b = foxKeyframes[idx];
      const rawT = Math.max(0, Math.min(1, (p - a[0]) / Math.max(1e-4, b[0] - a[0])));
      const ease = rawT * rawT * (3 - 2 * rawT);
      const pose = [1, 2, 3, 4].map(i => a[i] + (b[i] - a[i]) * ease);
      const activeFox = signinPane.hidden
        ? stage.querySelector?.('.ob-fox-setup')
        : stage.querySelector?.('.ob-fox-signin');
      if (activeFox && !peekStage?.classList.contains('is-paused')) {
        applyFoxPose(activeFox, pose[0], pose[1], pose[2], pose[3]);
      }
      foxRAF = requestAnimationFrame(tick);
    };
    foxRAF = requestAnimationFrame(tick);
  }

  function stopFoxLoop() {
    if (foxRAF && typeof cancelAnimationFrame === 'function') {
      cancelAnimationFrame(foxRAF);
      foxRAF = null;
    }
  }

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

  function syncStorySlide() {
    const slot = storyIndex % termCommands.length;
    storyTitle.textContent = t('[[i18n:ui.onboarding_signin_tagline]]');
    agentTiles.forEach((tile, idx) => tile.classList.toggle('is-active', idx === slot));
    if (termCmdEl) termCmdEl.textContent = termCommands[slot % termCommands.length];
    if (setupPane?.dataset) setupPane.dataset.beat = String(slot);
  }

  function startStoryCarousel() {
    if (storyTimer) return;
    storyIndex = 0;
    syncStorySlide();
    storyTimer = setInterval(() => {
      storyIndex = (storyIndex + 1) % 3;
      syncStorySlide();
    }, 1700);
  }

  function stopStoryCarousel() {
    if (storyTimer) {
      clearInterval(storyTimer);
      storyTimer = null;
    }
  }

  function stageBounds(st) {
    const table = {
      allocating: [14, 38],
      starting: [36, 58],
      connecting: [62, 78],
      checking_resources: [74, 85],
      installing_session_service: [80, 90],
      installing_magpie: [85, 92],
      starting_magpie: [89, 94],
      migrating_credentials: [92, 96.5],
      agents_installed: [100, 100]
    };
    return table[st] || [10, 34];
  }

  let progressCurrent = 0;
  let progressFloor = 0;
  let progressCeiling = 0;
  let progressCompleting = false;
  let progressPaused = false;
  let progressRAF = null;
  let progressLastTS = 0;
  let onProgressFilled = null;

  function applyBarWidth(pct) {
    const clamped = Math.max(0, Math.min(100, pct));
    progressCurrent = clamped;
    if (barFill?.style) {
      barFill.style.width = Number.isInteger(clamped) ? clamped + '%' : clamped.toFixed(2) + '%';
    }
  }

  function resetProgressBar() {
    if (progressRAF && typeof cancelAnimationFrame === 'function') {
      cancelAnimationFrame(progressRAF);
      progressRAF = null;
    }
    progressCurrent = 0;
    progressFloor = 0;
    progressCeiling = 0;
    progressCompleting = false;
    progressPaused = false;
    progressLastTS = 0;
    onProgressFilled = null;
    applyBarWidth(8);
  }

  function stepProgressFrame() {
    if (stage.hidden || setupPane.hidden || progressPaused) {
      progressRAF = null;
      progressLastTS = 0;
      return;
    }
    const now = Date.now();
    const dt = progressLastTS ? Math.min(0.08, Math.max(0.004, (now - progressLastTS) / 1000)) : 0.016;
    progressLastTS = now;

    let next = progressCurrent;
    if (progressCompleting) {
      // Decisive sprint to 100% once backend or async handoff completes.
      const rem = 100 - next;
      next += Math.max(150, rem * 9.5) * dt;
      if (next >= 99.7) {
        applyBarWidth(100);
        progressRAF = null;
        progressLastTS = 0;
        const cb = onProgressFilled;
        onProgressFilled = null;
        cb?.();
        return;
      }
    } else if (next < progressFloor) {
      // Real stage milestone jumped ahead: accelerate smoothly through the checkpoint.
      const gap = progressFloor - next;
      next += Math.max(36, gap * 5.6) * dt;
    } else if (next < progressCeiling) {
      // Asymptotic trickle between real checkpoints so the bar never stalls.
      const headroom = progressCeiling - next;
      const baseRate = prepState.runtime_id ? 3.2 : 1.4;
      next += Math.min(headroom, Math.max(baseRate, headroom * 0.62) * dt);
    }
    applyBarWidth(Math.max(progressCurrent, Math.min(progressCompleting ? 100 : 97.5, next)));
    if (typeof requestAnimationFrame === 'function') {
      progressRAF = requestAnimationFrame(stepProgressFrame);
    }
  }

  function syncProgressTarget(st, { paused = false, complete = false, onComplete = null } = {}) {
    const [fl, ceil] = complete ? [100, 100] : stageBounds(st);
    progressPaused = paused;
    if (complete) {
      progressCompleting = true;
      progressFloor = 100;
      progressCeiling = 100;
      if (onComplete) onProgressFilled = onComplete;
    } else {
      progressCompleting = false;
      onProgressFilled = null;
      progressFloor = Math.max(progressFloor, fl);
      progressCeiling = Math.max(progressCeiling, ceil);
    }
    const reduced = typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (typeof requestAnimationFrame !== 'function' || reduced) {
      applyBarWidth(complete ? 100 : fl);
      if (complete) {
        const cb = onProgressFilled;
        onProgressFilled = null;
        cb?.();
      }
      return;
    }
    if (progressCurrent < 6) applyBarWidth(8);
    if (!paused && !progressRAF) {
      progressLastTS = Date.now();
      progressRAF = requestAnimationFrame(stepProgressFrame);
    }
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
    taglineEl.textContent = t('[[i18n:ui.onboarding_signin_tagline]]');
    if (alwaysOnEl) alwaysOnEl.textContent = t('[[i18n:ui.always_on]]');
    if (cloudCaptionEl) cloudCaptionEl.textContent = t('[[i18n:ui.cloud_computer]]');

    if (!isAuthorized) {
      stopStoryCarousel();
      resetProgressBar();
      if (delayedSetupTimer) { clearTimeout(delayedSetupTimer); delayedSetupTimer = null; }
      if (asyncHandoffTimer) { clearTimeout(asyncHandoffTimer); asyncHandoffTimer = null; }
      stage.hidden = false;
      stage.classList.remove('is-finishing');
      signinPane.hidden = false;
      setupPane.hidden = true;
      setMainChromeHidden(true);
      ensureFoxLoop();
      loginBtn.disabled = loginBusy;
      loginBtn.textContent = loginBusy ? t('[[i18n:ui.connecting_to_runta]]') : t('[[i18n:ui.sign_in_to_runta]]');
      const connectingText = t('[[i18n:ui.connecting_to_runta]]');
      const showHint = !!loginMessage && loginMessage !== '[[i18n:ui.connecting_to_runta]]' && loginMessage !== connectingText;
      loginHint.hidden = !showHint;
      loginHint.textContent = showHint ? loginMessage : '';
      return;
    }

    if (setupDismissedForSession) {
      stopStoryCarousel();
      stopFoxLoop();
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
      stageText.hidden = true;
      actionBtn.hidden = true;
      if (setupPane.hidden || progressCurrent <= 10) {
        setupDismissedForSession = true;
        signedInTransition = false;
        stopStoryCarousel();
        stopFoxLoop();
        resetProgressBar();
        stage.hidden = true;
        stage.classList.remove('is-finishing');
        setMainChromeHidden(false);
        return;
      }
      if (!finishTimer && !progressCompleting) {
        syncProgressTarget('agents_installed', {
          complete: true,
          onComplete: () => {
            if (finishTimer) return;
            stage.classList.add('is-finishing');
            finishTimer = setTimeout(() => {
              finishTimer = null;
              setupDismissedForSession = true;
              signedInTransition = false;
              stopStoryCarousel();
              stopFoxLoop();
              resetProgressBar();
              stage.hidden = true;
              stage.classList.remove('is-finishing');
              setMainChromeHidden(false);
            }, 300);
          }
        });
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
    ensureFoxLoop();

    // Bounded ~5s onboarding experience: once Runta has allocated the cloud
    // computer (runtime_id assigned, no error/offline), smoothly hand off to the
    // main workspace after ~5s while remaining cold-boot/install work runs async!
    if (!isFailed && !isOffline && prepState.runtime_id && !asyncHandoffTimer && !finishTimer) {
      asyncHandoffTimer = setTimeout(() => {
        asyncHandoffTimer = null;
        if (isAuthorized && prepState.stage !== 'failed' && prepState.stage !== 'offline') {
          syncProgressTarget('agents_installed', {
            complete: true,
            onComplete: () => {
              if (finishTimer) return;
              stage.classList.add('is-finishing');
              finishTimer = setTimeout(() => {
                finishTimer = null;
                setupDismissedForSession = true;
                signedInTransition = false;
                stopStoryCarousel();
                stopFoxLoop();
                resetProgressBar();
                stage.hidden = true;
                stage.classList.remove('is-finishing');
                setMainChromeHidden(false);
              }, 300);
            }
          });
        }
      }, 4600);
    }
    if (isFailed || isOffline) {
      if (asyncHandoffTimer) { clearTimeout(asyncHandoffTimer); asyncHandoffTimer = null; }
    }

    const isBilling = prepState.status === 'billing_restricted';
    peekStage?.classList.toggle('is-paused', isFailed || isOffline);
    if (isFailed) {
      progressPaused = true;
      applyBarWidth(100);
    } else {
      syncProgressTarget(st, { paused: isOffline });
    }
    barFill?.classList.toggle('is-error', isFailed && !isBilling);
    // Let UI explain itself during normal setup; only surface actionable error/offline note.
    if (isFailed || isOffline) {
      stageText.hidden = false;
      stageText.textContent = stageLabel(st, prepState.status || '');
    } else {
      stageText.hidden = true;
    }

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
    resetProgressBar();
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
    resetProgressBar();
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
