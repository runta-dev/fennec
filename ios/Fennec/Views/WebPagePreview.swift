import SwiftUI
import UIKit
import WebKit

/// A web page from the cloud computer, full screen. It runs in a
/// non-persistent web view and can't navigate away from itself; links open
/// in Safari instead.
struct WebPagePreview: View {
    var url: URL
    var title: String
    var onClose: () -> Void

    var body: some View {
        NavigationStack {
            WebPageView(url: url)
                .ignoresSafeArea(edges: .bottom)
                .navigationTitle(title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button(action: onClose) { Image(systemName: "xmark").regularIconWeight() }
                            .accessibilityLabel(L10n.text("ui.close"))
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        ShareLink(item: url) { Image(systemName: "square.and.arrow.up").regularIconWeight() }
                    }
                }
        }
    }
}

private struct WebPageView: UIViewRepresentable {
    var url: URL

    func makeCoordinator() -> WebPageNavigation { WebPageNavigation(page: url) }

    func makeUIView(context: Context) -> WKWebView {
        let view = WKWebView(frame: .zero, configuration: WebPageNavigation.configuration())
        view.navigationDelegate = context.coordinator
        view.allowsBackForwardNavigationGestures = false
        view.loadFileURL(url, allowingReadAccessTo: url.deletingLastPathComponent())
        return view
    }

    func updateUIView(_ view: WKWebView, context: Context) {}
}

/// Keeps a preview on its own page: the page and its subresources load,
/// any navigation elsewhere opens in Safari.
final class WebPageNavigation: NSObject, WKNavigationDelegate {
    let page: URL
    var finished: (() -> Void)?

    init(page: URL) { self.page = page }

    static func configuration() -> WKWebViewConfiguration {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        return configuration
    }

    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping @MainActor (WKNavigationActionPolicy) -> Void) {
        guard action.targetFrame?.isMainFrame != false else { return decisionHandler(.allow) }
        let target = action.request.url
        if target?.isFileURL == true && target?.path() == page.path() || target?.scheme == "about" {
            return decisionHandler(.allow)
        }
        if let target, ["http", "https"].contains(target.scheme), action.navigationType == .linkActivated {
            UIApplication.shared.open(target)
        }
        decisionHandler(.cancel)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { finished?() }
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) { finished?() }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) { finished?() }
}

/// What the top of a web page looks like, for its card in the conversation.
@MainActor
enum WebPageSnapshot {
    static func render(_ url: URL, size: CGSize = CGSize(width: 390, height: 560)) async -> UIImage? {
        guard let window = UIApplication.shared.connectedScenes.compactMap({ $0 as? UIWindowScene })
            .flatMap(\.windows).first(where: \.isKeyWindow) else { return nil }
        let navigation = WebPageNavigation(page: url)
        let view = WKWebView(frame: CGRect(origin: CGPoint(x: -size.width * 2, y: 0), size: size), configuration: WebPageNavigation.configuration())
        view.navigationDelegate = navigation
        view.isUserInteractionEnabled = false
        // Laid out in the window (off screen) so WebKit actually renders it.
        window.addSubview(view)
        defer { view.removeFromSuperview() }
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            var resumed = false
            let finish = {
                guard !resumed else { return }
                resumed = true
                done.resume()
            }
            navigation.finished = finish
            view.loadFileURL(url, allowingReadAccessTo: url.deletingLastPathComponent())
            Task { try? await Task.sleep(for: .seconds(8)); finish() }
        }
        // Scripts that draw charts run after the load event.
        try? await Task.sleep(for: .milliseconds(400))
        let configuration = WKSnapshotConfiguration()
        configuration.rect = CGRect(origin: .zero, size: size)
        return try? await view.takeSnapshot(configuration: configuration)
    }
}
