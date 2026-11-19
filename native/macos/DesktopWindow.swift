import AppKit
import WebKit

// The desktop window: projects with request boards and the cloud computer's
// sessions with terminal tabs (docs/desktop-app.md). It is a page (UI/desktop.html)
// in a title-bar-less window. State and secrets stay in the Go daemon; this
// class only relays allowed control calls, runs terminal tabs in PTYs, and
// tells the page about the account and sessions the menu bar app already polls.
final class DesktopWebView: WKWebView {
    // Strips of the page that move the window, minus the controls in them,
    // in page coordinates (origin at the top left).
    var dragRegions: [NSRect] = []
    var noDragRegions: [NSRect] = []

    override func mouseDown(with event: NSEvent) {
        let local = convert(event.locationInWindow, from: nil)
        let point = NSPoint(x: local.x, y: isFlipped ? local.y : bounds.height - local.y)
        if dragRegions.contains(where: { $0.contains(point) }) && !noDragRegions.contains(where: { $0.contains(point) }) {
            if event.clickCount == 2 { window?.performZoom(nil) } else { window?.performDrag(with: event) }
            return
        }
        super.mouseDown(with: event)
    }

    override func willOpenMenu(_ menu: NSMenu, with event: NSEvent) {
        // Text actions only; no Reload or Inspect.
        menu.items.removeAll { ["WKMenuItemIdentifierReload", "WKMenuItemIdentifierInspectElement", "WKMenuItemIdentifierGoBack", "WKMenuItemIdentifierGoForward"].contains($0.identifier?.rawValue ?? "") }
    }
}

final class DesktopWindow: NSObject, NSWindowDelegate, WKScriptMessageHandler, WKNavigationDelegate {
    private let control: ControlClient
    private let fen: () -> URL
    var openPanel: () -> Void = {}
    var refreshSessions: () -> Void = {}
    private var window: NSWindow?
    private var web: DesktopWebView?
    private var ready = false
    private var state: [String: Any] = [:]
    private var terminals: [String: (pty: TerminalPTY, output: TerminalOutputWindow)] = [:]
    // Control methods the page may call. Everything else stays with the panel.
    // controlMethod marks them for cmd/check-control-contract.
    private static let methods: Set<String> = [controlMethod("board.call"), controlMethod("sessions.start"), controlMethod("account.organizations"), controlMethod("sessions.refresh"),
        controlMethod("settings.set"), controlMethod("login-item.status"), controlMethod("login-item.on"), controlMethod("login-item.off"), controlMethod("account.logout")]
    private static func controlMethod(_ name: String) -> String { name }
    private static let session = try! NSRegularExpression(pattern: "^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$")
    private static let key = try! NSRegularExpression(pattern: "^[a-z0-9-]{3,40}:[a-z0-9]{1,12}$")

    init(control: ControlClient, fen: @escaping () -> URL) {
        self.control = control
        self.fen = fen
    }

    var isVisible: Bool { window?.isVisible == true }

    func show() {
        if window == nil { build() }
        NSApp.setActivationPolicy(.regular)
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func build() {
        let frame = NSRect(x: 0, y: 0, width: 1280, height: 800)
        let window = NSWindow(contentRect: frame, styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .hidden
        window.title = "Fennec"
        window.minSize = NSSize(width: 900, height: 560)
        window.isReleasedWhenClosed = false
        window.delegate = self
        window.setFrameAutosaveName("FennecDesktop")
        if !window.setFrameUsingName("FennecDesktop") { window.center() }
        let configuration = WKWebViewConfiguration()
        configuration.userContentController.add(WeakHandler(self), name: "desktop")
        let web = DesktopWebView(frame: frame, configuration: configuration)
        web.navigationDelegate = self
        web.setValue(false, forKey: "drawsBackground")
        window.contentView = web
        let folder = Bundle.main.resourceURL!.appendingPathComponent("UI")
        web.loadFileURL(folder.appendingPathComponent("desktop.html"), allowingReadAccessTo: folder)
        self.window = window
        self.web = web
        applyTheme(theme)
        placeLights()
    }

    // Traffic lights centred on the sidebar's 46 px title row.
    private func placeLights() {
        guard let window, !window.styleMask.contains(.fullScreen), let close = window.standardWindowButton(.closeButton),
              let container = close.superview?.superview else { return }
        let height: CGFloat = 46
        container.frame = NSRect(x: 0, y: window.frame.height - height, width: window.frame.width, height: height)
        for (index, kind) in [NSWindow.ButtonType.closeButton, .miniaturizeButton, .zoomButton].enumerated() {
            guard let button = window.standardWindowButton(kind) else { continue }
            button.setFrameOrigin(NSPoint(x: 16 + CGFloat(index) * 20, y: (height - button.frame.height) / 2))
        }
    }

    func windowDidResize(_ notification: Notification) { placeLights() }
    func windowDidExitFullScreen(_ notification: Notification) { placeLights() }

    func windowWillClose(_ notification: Notification) {
        for (_, terminal) in terminals { terminal.output.close(); terminal.pty.close() }
        terminals.removeAll()
        ready = false
        web?.configuration.userContentController.removeScriptMessageHandler(forName: "desktop")
        web = nil
        window = nil
        // Back to a menu bar app when the window closes.
        NSApp.setActivationPolicy(.accessory)
    }

    // The menu bar app hands over what it learns: account, language, sessions.
    func update(_ change: [String: Any]) {
        for (key, value) in change { state[key] = value }
        if let theme = (change["settings"] as? [String: Any])?["theme"] as? String { applyTheme(theme) }
        guard ready else { return }
        call("desktopState", [change])
    }

    // Appearance follows Settings (system, light, dark); the page follows the window.
    private var theme = "system"
    private func applyTheme(_ value: String) {
        theme = value
        window?.appearance = value == "dark" ? NSAppearance(named: .darkAqua) : value == "light" ? NSAppearance(named: .aqua) : nil
    }

    private func call(_ function: String, _ arguments: [Any]) {
        guard let web, let data = try? JSONSerialization.data(withJSONObject: arguments), let json = String(data: data, encoding: .utf8) else { return }
        web.evaluateJavaScript("window.\(function)(...\(json))", completionHandler: nil)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        decisionHandler(navigationAction.request.url?.isFileURL == true ? .allow : .cancel)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        for (_, terminal) in terminals { terminal.output.close(); terminal.pty.close() }
        terminals.removeAll()
        ready = false
        webView.reload()
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard let text = message.body as? String, text.utf8.count <= 1 << 20,
              let data = text.data(using: .utf8),
              let body = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let action = body["action"] as? String else { return }
        switch action {
        case "ready":
            ready = true
            call("desktopState", [state])
        case "rpc":
            guard let id = body["id"] as? Int, let method = body["method"] as? String else { return }
            guard Self.methods.contains(method) else { return call("desktopReply", [id, false, ["message": "Not allowed"]]) }
            control.call(method, body["params"] as? [String: Any] ?? [:]) { [weak self] result in
                switch result {
                case .success(let value): self?.call("desktopReply", [id, true, value])
                case .failure(let error):
                    let error = error as NSError
                    self?.call("desktopReply", [id, false, ["message": error.localizedDescription, "code": error.userInfo["FennecCode"] as? String ?? ""]])
                }
            }
        case "drag-regions":
            let rects = { (value: Any?) -> [NSRect] in
                (value as? [[Double]] ?? []).compactMap { $0.count == 4 ? NSRect(x: $0[0], y: $0[1], width: $0[2], height: $0[3]) : nil }
            }
            web?.dragRegions = rects(body["drag"])
            web?.noDragRegions = rects(body["exclude"])
        case "open-url":
            if let text = body["url"] as? String, text.utf8.count <= 8192, let url = URL(string: text),
               ["https", "http"].contains(url.scheme ?? ""), url.user == nil, url.password == nil { NSWorkspace.shared.open(url) }
        case "open-panel":
            openPanel()
        case "refresh-sessions":
            refreshSessions()
        case "theme":
            if let value = body["theme"] as? String, ["system", "light", "dark"].contains(value) { applyTheme(value) }
        default:
            terminal(action, body)
        }
    }

    // ---------- terminal tabs ----------

    private func terminal(_ action: String, _ body: [String: Any]) {
        guard let key = body["key"] as? String, matches(Self.key, key) else { return }
        switch action {
        case "term-open":
            guard terminals[key] == nil, let kind = body["kind"] as? String,
                  let cols = body["cols"] as? Int, let rows = body["rows"] as? Int,
                  (1...500).contains(cols), (1...300).contains(rows) else { return }
            let executable: URL, arguments: [String], directory: URL?
            if kind == "agent" {
                guard let session = body["session"] as? String, matches(Self.session, session) else { return ended(key, failed: true) }
                executable = fen()
                arguments = ["fen", "attach", session]
                directory = nil
            } else if kind == "shell" {
                // The user's login shell on this Mac, in the home folder.
                let shell = ProcessInfo.processInfo.environment["SHELL"].flatMap { FileManager.default.isExecutableFile(atPath: $0) ? $0 : nil } ?? "/bin/zsh"
                executable = URL(fileURLWithPath: shell)
                arguments = ["-l"]
                directory = FileManager.default.homeDirectoryForCurrentUser
            } else { return }
            let pty = TerminalPTY(), output = TerminalOutputWindow()
            terminals[key] = (pty, output)
            let dark = window?.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            let probe = kind == "agent" ? DisplayProbe(dark: dark) : nil
            do {
                try pty.start(executable: executable, arguments: arguments, directory: directory, cols: UInt16(cols), rows: UInt16(rows), output: { [weak self] data in
                    var bytes = data
                    if let probe, let reply = probe.answer(&bytes) { try? pty.write(reply) }
                    for offset in stride(from: 0, to: bytes.count, by: 16384) {
                        let chunk = bytes.subdata(in: offset..<min(offset + 16384, bytes.count))
                        guard output.reserve(chunk.count) else { return }
                        DispatchQueue.main.async {
                            guard let self, self.terminals[key]?.pty === pty else { return }
                            self.queueOutput(key, chunk)
                        }
                    }
                }, exited: { [weak self] status in
                    DispatchQueue.main.async {
                        guard let self, self.terminals[key]?.pty === pty else { return }
                        self.terminals[key] = nil
                        self.ended(key, failed: status != 0 && kind == "agent")
                    }
                })
            } catch {
                terminals[key] = nil
                ended(key, failed: true)
            }
        case "term-input":
            if let text = body["data"] as? String, text.utf8.count <= 65536 { try? terminals[key]?.pty.write(Data(text.utf8)) }
        case "term-resize":
            if let cols = body["cols"] as? Int, let rows = body["rows"] as? Int, (1...500).contains(cols), (1...300).contains(rows) {
                try? terminals[key]?.pty.resize(cols: UInt16(cols), rows: UInt16(rows))
            }
        case "term-credit":
            if let count = body["bytes"] as? Int { terminals[key]?.output.acknowledge(count) }
        case "term-close":
            terminals[key]?.output.close()
            terminals[key]?.pty.close()
            terminals[key] = nil
        default:
            return
        }
    }

    private func ended(_ key: String, failed: Bool) { flushOutput(); call("termEnded", [key, failed]) }

    // Output waiting for the page. Chunks that arrive together reach xterm.js
    // in one call instead of one JavaScript evaluation each.
    private var pendingOutput: [String: Data] = [:]
    private var flushScheduled = false
    private func queueOutput(_ key: String, _ chunk: Data) {
        pendingOutput[key, default: Data()].append(chunk)
        guard !flushScheduled else { return }
        flushScheduled = true
        DispatchQueue.main.async { [weak self] in self?.flushOutput() }
    }
    private func flushOutput() {
        flushScheduled = false
        let pending = pendingOutput
        pendingOutput.removeAll()
        for (key, data) in pending where terminals[key] != nil { call("termOutput", [key, data.base64EncodedString()]) }
    }

    private func matches(_ pattern: NSRegularExpression, _ text: String) -> Bool {
        pattern.firstMatch(in: text, range: NSRange(text.startIndex..., in: text)) != nil
    }
}

// `fen attach` asks its terminal for colors, cell size and attributes when it
// starts and waits 100 ms (internal/launcher/display.go). Through the page
// the answer can come later, and then reaches the agent as typed text. The
// window answers that first query itself, with the colors the page uses,
// and the page never sees it.
final class DisplayProbe {
    private static let query = Data("\u{1b}]10;?\u{1b}\\\u{1b}]11;?\u{1b}\\\u{1b}]12;?\u{1b}\\\u{1b}[16t\u{1b}[c".utf8)
    private let reply: Data
    private let lock = NSLock()
    private var armed = true
    private var seen = 0

    init(dark: Bool) {
        let (foreground, background) = dark ? ("e6e6/e6e6/e9e9", "1b1b/1c1c/2020") : ("1c1c/1c1c/2121", "ffff/ffff/ffff")
        reply = Data("\u{1b}]10;rgb:\(foreground)\u{1b}\\\u{1b}]11;rgb:\(background)\u{1b}\\\u{1b}]12;rgb:\(foreground)\u{1b}\\\u{1b}[6;15;8t\u{1b}[?62;22c".utf8)
    }

    // Removes the query from output and returns the answer, once. Only the
    // first 64 KiB are looked at.
    func answer(_ data: inout Data) -> Data? {
        lock.lock()
        defer { lock.unlock() }
        guard armed else { return nil }
        seen += data.count
        if let range = data.range(of: Self.query) {
            armed = false
            data.removeSubrange(range)
            return reply
        }
        if seen > 65536 { armed = false }
        return nil
    }
}

// WKUserContentController retains its handlers; this keeps the window free.
private final class WeakHandler: NSObject, WKScriptMessageHandler {
    weak var target: WKScriptMessageHandler?
    init(_ target: WKScriptMessageHandler) { self.target = target }
    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(controller, didReceive: message)
    }
}
