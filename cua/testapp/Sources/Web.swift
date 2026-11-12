import AppKit
import WebKit

// WebScene shows a local page in a WKWebView. The page reports its state
// (address path, clicks per DOM id, input values) through a script message
// handler after every change.
final class WebScene: NSObject, WKScriptMessageHandler, WKNavigationDelegate {
    static let baseURL = URL(string: "https://fixture.example/app/form")!
    var loaded = false
    var state: [String: Any] = ["path": "", "clicks": [String: Any](), "inputs": [String: Any]()]

    static func build() {
        let m = Model.shared
        let s = WebScene()
        m.retained.append(s)
        let cfg = WKWebViewConfiguration()
        cfg.userContentController.add(s, name: "fixture")
        let wv = WKWebView(frame: NSRect(x: 0, y: 0, width: 640, height: 480), configuration: cfg)
        wv.navigationDelegate = s
        markID(wv, "web-view")
        _ = fixed(wv, 640, 480)
        wv.loadHTMLString(page(), baseURL: baseURL)
        m.reg("web") { s.state }
        m.reg("web-loaded") { s.loaded }
        m.teardowns.append { cfg.userContentController.removeScriptMessageHandler(forName: "fixture") }
        makeWindow("web", "Web Form", wv, x: 80, y: 40)
        m.ready = { s.loaded }
    }

    func userContentController(_ c: WKUserContentController, didReceive msg: WKScriptMessage) {
        if let d = msg.body as? [String: Any] { state = d }
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { loaded = true }

    static func page() -> String {
        var rows = ""
        for i in 1...120 {
            rows += "<tr><td>Row \(i)</td><td><button id=\"delete-\(i)\" class=\"rec\">Delete</button></td></tr>\n"
        }
        return """
        <!doctype html>
        <html><head><meta charset="utf-8"><title>Fixture Web Form</title>
        <style>body{font:14px -apple-system;margin:16px} td{padding:2px 8px}</style></head>
        <body>
        <h1>Fixture Web Form</h1>
        <section id="form-section">
          <label for="email">Email</label> <input id="email" type="text">
          <label for="web-password">Password</label> <input id="web-password" type="password">
          <button id="save-top" class="rec">Save</button>
          <a id="docs" class="rec" href="https://example.com/docs/web">Docs</a>
          <button id="go-login" class="rec">Go to Login</button>
          <p><textarea id="notes" aria-label="Notes" rows="3" cols="40"></textarea></p>
        </section>
        <section id="login-section" hidden><h2>Login</h2><label for="login-user">User</label> <input id="login-user"></section>
        <table>
        \(rows)</table>
        <button id="save-bottom" class="rec">Save</button>
        <script>
        const clicks = {};
        function report() {
          const inputs = {};
          for (const el of document.querySelectorAll('input,textarea')) inputs[el.id] = el.value;
          window.webkit.messageHandlers.fixture.postMessage({path: location.pathname, clicks, inputs});
        }
        document.addEventListener('click', e => {
          const el = e.target.closest('.rec');
          if (!el) return;
          if (el.tagName === 'A') e.preventDefault();
          clicks[el.id] = (clicks[el.id] || 0) + 1;
          if (el.id === 'go-login') {
            history.pushState({}, '', '/app/login');
            document.getElementById('form-section').hidden = true;
            document.getElementById('login-section').hidden = false;
          }
          report();
        });
        document.addEventListener('input', report);
        report();
        </script>
        </body></html>
        """
    }
}
