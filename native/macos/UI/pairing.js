// Footer > Get Fennec iOS App: a modal with the App Store step and a sign-in
// QR code approved by this Mac's account. The iPhone redeems the short-lived
// device code for a token of its own.
(() => {
  const dialog = document.querySelector('#ios-app-dialog');
  const open = document.querySelector('#get-ios-app');
  if (!dialog || !open) return;
  const image = dialog.querySelector('#pairing-qr');
  const loading = dialog.querySelector('#pairing-loading');
  const expiry = dialog.querySelector('#pairing-expiry');
  const message = dialog.querySelector('#pairing-message');
  const retry = dialog.querySelector('#pairing-retry');
  // An open dialog refreshes the code a few times, then stops minting codes.
  const MAX_AUTOMATIC_REFRESHES = 4;
  let signedIn = false;
  let timer = null;
  let expiresAt = 0;
  let refreshes = 0;
  let pending = false;

  const stopTimer = () => { if (timer) { clearInterval(timer); timer = null; } };
  const showCode = visible => { image.hidden = !visible; loading.hidden = visible || !pending; };
  const fail = text => {
    stopTimer(); pending = false;
    image.removeAttribute('src'); showCode(false);
    expiry.textContent = ''; message.textContent = text; retry.hidden = !signedIn;
  };
  const request = () => {
    if (pending) return;
    if (!signedIn) { fail("[[i18n:ui.sign_in_on_this_mac_first]]"); return; }
    pending = true; retry.hidden = true; message.textContent = ''; showCode(false);
    send('pair-device');
  };
  const tick = () => {
    const remaining = Math.max(0, Math.round((expiresAt - Date.now()) / 1000));
    expiry.textContent = "[[i18n:ui.expires_in]] " + Math.floor(remaining / 60) + ':' + String(remaining % 60).padStart(2, '0');
    if (remaining > 0) return;
    stopTimer();
    if (refreshes < MAX_AUTOMATIC_REFRESHES) { refreshes += 1; request(); }
    else fail('');
  };

  window.renderPairing = result => {
    if (!dialog.open) { pending = false; return; }
    pending = false;
    if (!result || result.error) { fail(result?.error || "[[i18n:ui.pairing_failed]]"); return; }
    expiresAt = Date.parse(result.expires_at) || Date.now() + 120000;
    image.src = result.qr; showCode(true); message.textContent = '';
    stopTimer(); tick(); timer = setInterval(tick, 1000);
  };
  window.renderPairingAvailable = available => {
    signedIn = available;
    if (!available && dialog.open) fail("[[i18n:ui.sign_in_on_this_mac_first]]");
  };

  open.addEventListener('click', () => {
    refreshes = 0;
    if (!dialog.open && typeof dialog.showModal === 'function') {
      dialog.showModal();
      // Focus the dialog, not the close button, so opening it with the mouse draws no focus ring.
      dialog.focus();
    }
    request();
  });
  retry.addEventListener('click', () => { refreshes = 0; request(); });
  dialog.querySelector('#ios-app-close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { stopTimer(); image.removeAttribute('src'); showCode(false); expiry.textContent = ''; message.textContent = ''; });
})();
