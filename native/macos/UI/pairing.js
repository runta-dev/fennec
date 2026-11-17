// Footer > Get Fennec iOS App: download, scan, then approve in the browser.
// The Mac starts a device authorization for the iPhone and opens the Runta
// dashboard's approval page; the QR code carries only the short-lived device
// code, which the iPhone redeems for a token of its own.
(() => {
  const dialog = document.querySelector('#ios-app-dialog');
  const open = document.querySelector('#get-ios-app');
  if (!dialog || !open) return;
  const image = dialog.querySelector('#pairing-qr');
  const loading = dialog.querySelector('#pairing-loading');
  const expiry = dialog.querySelector('#pairing-expiry');
  const message = dialog.querySelector('#pairing-message');
  const retry = dialog.querySelector('#pairing-retry');
  const reopen = dialog.querySelector('#pairing-open');
  let signedIn = false;
  let timer = null;
  let expiresAt = 0;
  let pending = false;

  const stopTimer = () => { if (timer) { clearInterval(timer); timer = null; } };
  const live = () => !!image.getAttribute('src') && expiresAt > Date.now();
  const showCode = visible => { image.hidden = !visible; loading.hidden = visible || !pending; reopen.hidden = !visible; };
  const fail = text => {
    stopTimer(); pending = false;
    image.removeAttribute('src'); showCode(false);
    expiry.textContent = ''; message.textContent = text; retry.hidden = !signedIn;
  };
  const tick = () => {
    const remaining = Math.max(0, Math.round((expiresAt - Date.now()) / 1000));
    expiry.textContent = "[[i18n:ui.expires_in]] " + Math.floor(remaining / 60) + ':' + String(remaining % 60).padStart(2, '0');
    // Each code counts against Runta's per-network limit, so an expired code
    // is replaced only when the user asks.
    if (remaining <= 0) fail("[[i18n:ui.code_expired]]");
  };
  const request = () => {
    if (pending) return;
    if (!signedIn) { fail("[[i18n:ui.sign_in_on_this_mac_first]]"); return; }
    pending = true; retry.hidden = true; message.textContent = ''; showCode(false);
    send('pair-device');
  };

  window.renderPairing = result => {
    pending = false;
    if (!dialog.open) return;
    if (!result || result.error) { fail(result?.error || "[[i18n:ui.pairing_failed]]"); return; }
    expiresAt = Date.parse(result.expires_at) || Date.now() + 900000;
    image.src = result.qr; showCode(true); message.textContent = '';
    stopTimer(); tick(); timer = setInterval(tick, 1000);
  };
  window.renderPairingAvailable = available => {
    signedIn = available;
    if (!available) { image.removeAttribute('src'); if (dialog.open) fail("[[i18n:ui.sign_in_on_this_mac_first]]"); }
  };

  open.addEventListener('click', () => {
    if (!dialog.open && typeof dialog.showModal === 'function') {
      dialog.showModal();
      // Focus the dialog, not the close button, so opening it with the mouse draws no focus ring.
      dialog.focus();
    }
    // Reopening keeps an unexpired code instead of starting another authorization.
    if (live()) { showCode(true); stopTimer(); tick(); timer = setInterval(tick, 1000); }
    else request();
  });
  retry.addEventListener('click', request);
  reopen.addEventListener('click', () => send('open-pairing-approval'));
  dialog.querySelector('#ios-app-close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { stopTimer(); message.textContent = ''; });
})();
