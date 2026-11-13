import AppKit
import WebKit

// Shows the third-party notices bundled in Contents/Resources/licenses. Each
// document opens in its own window with a WKWebView, whose web content
// process renders the multi-megabyte Chromium credits page without blocking
// the menu-bar UI. Only the documents named here can be opened.
final class LicenseViewer: NSObject, WKNavigationDelegate, NSWindowDelegate {
    private struct Document {
        let file: String
        let titleKey: String
    }
    private static let documents: [String: Document] = [
        "fennec": Document(file: "THIRD_PARTY_NOTICES.md", titleKey: "ui.third_party_notices"),
        "chromium-credits": Document(file: "Chromium-credits.html", titleKey: "ui.chromium_credits"),
        "chromium-license": Document(file: "Chromium-LICENSE.txt", titleKey: "ui.chromium_license"),
    ]
    private var windows: [String: NSWindow] = [:]

    static func directory() -> URL? {
        Bundle.main.resourceURL?.appendingPathComponent("licenses", isDirectory: true)
    }

    /// Opens (or brings forward) one bundled notice. Answers false when the
    /// name is unknown or the file is not in this bundle.
    @discardableResult
    func open(_ name: String, language: String) -> Bool {
        guard let document = Self.documents[name], let directory = Self.directory() else { return false }
        let url = directory.appendingPathComponent(document.file)
        guard FileManager.default.isReadableFile(atPath: url.path) else { return false }
        let title = L10n.text(document.titleKey, language: language)
        if let window = windows[name] {
            window.title = title
            show(window)
            return true
        }
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        let web = WKWebView(frame: NSRect(x: 0, y: 0, width: 760, height: 640), configuration: configuration)
        web.navigationDelegate = self
        if url.pathExtension == "html" {
            web.loadFileURL(url, allowingReadAccessTo: directory)
        } else {
            // Plain text and Markdown are shown verbatim.
            guard let data = try? Data(contentsOf: url) else { return false }
            web.load(data, mimeType: "text/plain", characterEncodingName: "utf-8", baseURL: url)
        }
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 760, height: 640),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable],
                              backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.title = title
        window.contentView = web
        window.minSize = NSSize(width: 420, height: 320)
        window.delegate = self
        window.center()
        windows[name] = window
        show(window)
        return true
    }

    private func show(_ window: NSWindow) {
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    func windowWillClose(_ notification: Notification) {
        guard let window = notification.object as? NSWindow else { return }
        windows = windows.filter { $0.value !== window }
    }

    // The notices stay local: in-page anchors and the bundled files load in
    // place, project homepages open in the default browser, and nothing else
    // navigates.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = action.request.url else { decisionHandler(.cancel); return }
        if url.isFileURL, let directory = Self.directory(), url.standardizedFileURL.path.hasPrefix(directory.standardizedFileURL.path + "/") {
            decisionHandler(.allow)
            return
        }
        if url.scheme == "about" {
            decisionHandler(.allow)
            return
        }
        if action.navigationType == .linkActivated, url.scheme == "https" || url.scheme == "http" {
            NSWorkspace.shared.open(url)
        }
        decisionHandler(.cancel)
    }
}
