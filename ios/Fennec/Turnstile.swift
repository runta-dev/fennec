import UIKit
import WebKit

/// Obtains a Cloudflare Turnstile token for Runta's email sign-in
/// (`/v1/auth/login`) without a visible web page. The Dashboard's own widget
/// runs in an off-screen web view whose document origin is the Dashboard, so
/// Runta verifies the token exactly as it does for the web login (same secret,
/// action `login_magic_link`, Dashboard hostname). The widget is shown only
/// when Cloudflare asks for an interaction.
@MainActor
final class TurnstileTokenProvider: NSObject, WKScriptMessageHandler, WKNavigationDelegate {
    enum Failure: Error { case unavailable, timedOut, refused }

    private var webView: WKWebView?
    private var continuation: CheckedContinuation<String, Error>?
    private var timeout: Task<Void, Never>?

    func token(siteKey: String, origin: URL, action: String) async throws -> String {
        guard continuation == nil, let window = Self.keyWindow() else { throw Failure.unavailable }
        return try await withCheckedThrowingContinuation { continuation in
            self.continuation = continuation
            let controller = WKUserContentController()
            controller.add(self, name: "turnstile")
            let configuration = WKWebViewConfiguration()
            configuration.userContentController = controller
            configuration.websiteDataStore = .nonPersistent()
            let view = WKWebView(frame: CGRect(x: 0, y: 0, width: 300, height: 70), configuration: configuration)
            view.navigationDelegate = self
            view.isOpaque = false
            view.backgroundColor = .clear
            view.scrollView.isScrollEnabled = false
            view.alpha = 0.01
            view.isUserInteractionEnabled = false
            view.center = CGPoint(x: window.bounds.midX, y: window.bounds.midY)
            window.addSubview(view)
            webView = view
            view.loadHTMLString(Self.page(siteKey: siteKey, action: action), baseURL: origin)
            timeout = Task { [weak self] in
                try? await Task.sleep(for: .seconds(45))
                self?.finish(.failure(Failure.timedOut))
            }
        }
    }

    nonisolated func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        let body = message.body as? [String: String] ?? [:]
        MainActor.assumeIsolated {
            switch body["event"] {
            case "token":
                if let token = body["token"], !token.isEmpty { finish(.success(token)) } else { finish(.failure(Failure.refused)) }
            case "interactive":
                // Cloudflare wants a click: show the widget where the user can reach it.
                webView?.alpha = 1
                webView?.isUserInteractionEnabled = true
            default:
                finish(.failure(Failure.refused))
            }
        }
    }

    nonisolated func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        MainActor.assumeIsolated { finish(.failure(Failure.unavailable)) }
    }

    nonisolated func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        MainActor.assumeIsolated { finish(.failure(Failure.unavailable)) }
    }

    private func finish(_ result: Result<String, Error>) {
        timeout?.cancel()
        timeout = nil
        webView?.configuration.userContentController.removeScriptMessageHandler(forName: "turnstile")
        webView?.removeFromSuperview()
        webView = nil
        continuation?.resume(with: result)
        continuation = nil
    }

    private static func keyWindow() -> UIWindow? {
        UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows).first { $0.isKeyWindow }
    }

    private static func page(siteKey: String, action: String) -> String {
        func js(_ value: String) -> String {
            let data = try? JSONSerialization.data(withJSONObject: [value])
            return String(data: data ?? Data("[\"\"]".utf8), encoding: .utf8).map { String($0.dropFirst().dropLast()) } ?? "\"\""
        }
        return """
        <!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1">
        <style>html,body{margin:0;background:transparent}</style>
        <script src="https://challenges.cloudflare.com/turnstile/v0/api.js?onload=fennecTurnstile&render=explicit" async></script>
        <script>
        function post(m){window.webkit.messageHandlers.turnstile.postMessage(m)}
        function fennecTurnstile(){
          turnstile.render('#w',{sitekey:\(js(siteKey)),action:\(js(action)),appearance:'interaction-only',
            callback:function(t){post({event:'token',token:t})},
            'before-interactive-callback':function(){post({event:'interactive'})},
            'error-callback':function(){post({event:'error'})},
            'expired-callback':function(){post({event:'error'})},
            'unsupported-callback':function(){post({event:'error'})}});
        }
        </script></head><body><div id="w"></div></body></html>
        """
    }
}
