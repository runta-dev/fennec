// Account > Sign in on iPhone: shows a QR code approved by this Mac's account.
// The iPhone redeems the short-lived device code for a token of its own.
(() => {
  const card = document.querySelector('#pairing-card');
  if (!card) return;
  const start = card.querySelector('#pair-iphone');
  const panel = card.querySelector('#pairing-panel');
  const image = card.querySelector('#pairing-qr');
  const expiry = card.querySelector('#pairing-expiry');
  const message = card.querySelector('#pairing-message');
  const cancel = card.querySelector('#pairing-cancel');
  // A forgotten open panel must not mint codes forever.
  const MAX_AUTOMATIC_REFRESHES = 4;
  let timer = null;
  let expiresAt = 0;
  let refreshes = 0;
  let pending = false;

  const stopTimer = () => { if (timer) { clearInterval(timer); timer = null; } };
  const reset = () => {
    stopTimer(); pending = false; refreshes = 0;
    panel.hidden = true; start.hidden = false; start.disabled = false;
    image.removeAttribute('src'); message.textContent = '';
  };
  const request = () => {
    if (pending) return;
    pending = true; start.disabled = true;
    message.textContent = '';
    send('pair-device');
  };
  const tick = () => {
    const remaining = Math.max(0, Math.round((expiresAt - Date.now()) / 1000));
    expiry.textContent = "[[i18n:ui.expires_in]] " + Math.floor(remaining / 60) + ':' + String(remaining % 60).padStart(2, '0');
    if (remaining > 0) return;
    stopTimer();
    if (refreshes < MAX_AUTOMATIC_REFRESHES) { refreshes += 1; request(); }
    else reset();
  };

  window.renderPairing = result => {
    pending = false; start.disabled = false;
    if (!result || result.error) {
      stopTimer();
      panel.hidden = true; start.hidden = false;
      message.textContent = result?.error || "[[i18n:ui.pairing_failed]]";
      return;
    }
    expiresAt = Date.parse(result.expires_at) || Date.now() + 120000;
    image.src = result.qr;
    start.hidden = true; panel.hidden = false; message.textContent = '';
    stopTimer(); tick(); timer = setInterval(tick, 1000);
  };
  window.renderPairingAvailable = available => { card.hidden = !available; if (!available) reset(); };

  start.addEventListener('click', () => { refreshes = 0; request(); });
  cancel.addEventListener('click', reset);
})();
