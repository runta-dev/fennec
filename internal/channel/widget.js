// Fennec feedback widget (FEN-224). Embed it with
//   <script src="https://<intake host>/widget.js" data-channel="<channel id>" async></script>
// It adds a Feedback button; the form posts to the same host, gated by
// Cloudflare Turnstile. The email is optional and only used to answer.
(function () {
  "use strict";
  var script = document.currentScript;
  if (!script || window.__fennecFeedback) return;
  window.__fennecFeedback = true;
  var base = new URL(script.src).origin;
  var channel = script.getAttribute("data-channel") || "";
  // English until the server's copy in the visitor's language arrives
  // (widget.* in the shared translation catalog).
  var t = { open: "Feedback", title: "Send feedback", text: "What went wrong, or what would you like?", name: "Name (optional)", email: "Email (optional, to hear back)", send: "Send", sending: "Sending…", done: "Got it, thank you!", done_reply: "Got it, thank you! We'll answer by email.", fail: "Couldn't send. Please try again later.", close: "Close", check: "Please complete the check first." };

  var host = document.createElement("div");
  host.style.cssText = "position:fixed;right:20px;bottom:20px;z-index:2147483000";
  var root = host.attachShadow({ mode: "closed" });
  root.innerHTML =
    "<style>" +
    ":host{all:initial;font:14px/1.4 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;color:#1d1d1f}" +
    "button{font:inherit;cursor:pointer;border:0;border-radius:8px}" +
    ".fab{background:#1d1d1f;color:#fff;padding:10px 16px;box-shadow:0 4px 16px rgba(0,0,0,.2)}" +
    ".panel{display:none;width:320px;max-width:calc(100vw - 40px);background:#fff;border-radius:12px;box-shadow:0 8px 32px rgba(0,0,0,.25);padding:16px;box-sizing:border-box}" +
    ".open .panel{display:block}.open .fab{display:none}" +
    ".head{display:flex;justify-content:space-between;align-items:center;margin-bottom:10px;font-weight:600}" +
    ".x{background:none;font-size:18px;line-height:1;color:#888;padding:2px 6px}" +
    "textarea,input{display:block;width:100%;box-sizing:border-box;font:inherit;border:1px solid #d2d2d7;border-radius:8px;padding:8px;margin-bottom:8px;color:inherit;background:#fff}" +
    "textarea{height:110px;resize:vertical}" +
    ".send{width:100%;background:#0071e3;color:#fff;padding:9px}.send:disabled{opacity:.5;cursor:default}" +
    ".msg{margin-top:8px;font-size:13px;color:#555}.cf{min-height:65px;margin-bottom:8px}" +
    "</style>" +
    "<div class=w><button class=fab type=button></button>" +
    "<form class=panel><div class=head><span class=title></span><button class=x type=button>×</button></div>" +
    "<textarea name=text maxlength=5000 required></textarea>" +
    "<input name=name maxlength=40 autocomplete=name>" +
    "<input name=email type=email maxlength=254 autocomplete=email>" +
    "<div class=cf></div><button class=send type=submit></button><div class=msg role=status></div></form></div>";
  var $ = function (s) { return root.querySelector(s); };
  var wrap = $(".w"), form = $("form"), msg = $(".msg"), send = $(".send");
  function label() {
    $(".fab").textContent = script.getAttribute("data-label") || t.open;
    $(".title").textContent = t.title;
    $(".x").setAttribute("aria-label", t.close);
    form.text.placeholder = t.text;
    form.name.placeholder = t.name;
    form.email.placeholder = t.email;
    send.textContent = t.send;
  }
  label();

  var siteKey = null, widgetId = null, token = "";
  function loadTurnstile(cb) {
    if (window.turnstile) return cb();
    var s = document.createElement("script");
    s.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
    s.async = true;
    s.onload = cb;
    document.head.appendChild(s);
  }
  function renderChallenge() {
    if (widgetId !== null || !siteKey) return;
    // Turnstile can't render inside a closed shadow root, so its box is a
    // light-DOM child slotted in.
    var box = document.createElement("div");
    box.slot = "cf";
    host.appendChild(box);
    $(".cf").innerHTML = "<slot name=cf></slot>";
    widgetId = window.turnstile.render(box, {
      sitekey: siteKey,
      callback: function (v) { token = v; },
      "expired-callback": function () { token = ""; },
    });
  }
  var config = fetch(base + "/v1/widget/" + encodeURIComponent(channel) + "?lang=" + encodeURIComponent(navigator.language || ""))
    .then(function (r) { if (!r.ok) throw r; return r.json(); })
    .then(function (c) {
      siteKey = c.site_key;
      for (var k in c.strings || {}) t[k] = c.strings[k];
      label();
    });
  function open() {
    wrap.classList.add("open");
    form.text.focus();
    config.then(function () { loadTurnstile(renderChallenge); }, function () { msg.textContent = t.fail; });
  }
  $(".fab").onclick = open;
  $(".x").onclick = function () { wrap.classList.remove("open"); };
  form.onsubmit = function (e) {
    e.preventDefault();
    if (!token) { msg.textContent = t.check; return; }
    send.disabled = true;
    send.textContent = t.sending;
    msg.textContent = "";
    fetch(base + "/v1/widget/" + encodeURIComponent(channel) + "/feedback", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: form.text.value, name: form.name.value, email: form.email.value, token: token, page: location.href }),
    })
      .then(function (r) { if (!r.ok) throw r; return r.json(); })
      .then(function (res) {
        form.reset();
        msg.textContent = res.can_reply ? t.done_reply : t.done;
      })
      .catch(function () { msg.textContent = t.fail; })
      .then(function () {
        send.disabled = false;
        send.textContent = t.send;
        token = "";
        if (widgetId !== null) window.turnstile.reset(widgetId);
      });
  };
  (document.body || document.documentElement).appendChild(host);
})();
