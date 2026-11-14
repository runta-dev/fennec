// fennec-cua-setup is Fennec CUA's setup window: it walks the user through
// the macOS permissions the app needs and shows how to connect an agent.
//
// It runs from FennecCUA.app/Contents/MacOS, started by the app itself
// (a Finder launch, `fennec-cua setup`, or the daemon when a grant is
// missing), so macOS attributes it to the app: AXIsProcessTrusted and
// CGPreflightScreenCaptureAccess here answer for "Fennec CUA", and, unlike
// in the long-running daemon, a fresh Screen Recording grant shows at once.
// Everything else (the daemon, the test capture, the direct-capture alert)
// goes through the bundled fennec-cua command.
//
// The window follows Fennec's menu-bar panel: borderless, rounded, one flat
// layer of cards on the window background, drawn by a WKWebView from
// Resources/setup/index.html.
import AppKit
import ApplicationServices
import WebKit

let showRequest = Notification.Name("com.runta.fennec.cua.setup.show")

// One window per user: a second start asks the first to come forward.
func claimSingleInstance() -> Bool {
    let support = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("fennec/cua", isDirectory: true)
    try? FileManager.default.createDirectory(at: support, withIntermediateDirectories: true)
    let fd = open(support.appendingPathComponent("setup.lock").path, O_CREAT | O_RDWR, 0o600)
    if fd < 0 { return true }
    if flock(fd, LOCK_EX | LOCK_NB) != 0 {
        DistributedNotificationCenter.default().postNotificationName(showRequest, object: nil, userInfo: nil, deliverImmediately: true)
        return false
    }
    return true // the descriptor stays open, holding the lock, until exit
}

final class SetupWindow: NSWindow {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { true }
}

// DragStrip lies over the page's header, outside its buttons, and moves
// the window: a borderless window has no title bar to drag.
final class DragStrip: NSView {
    var buttonsWidth: CGFloat = 52
    override func hitTest(_ point: NSPoint) -> NSView? {
        let p = convert(point, from: superview)
        guard bounds.contains(p), p.x < bounds.width - buttonsWidth else { return nil }
        return self
    }
    override func mouseDown(with event: NSEvent) { window?.performDrag(with: event) }
}

// The result of one `fennec-cua doctor --json`.
struct Doctor {
    var daemonScreenRecording = ""
    var capture = "" // "", "ok", "consent", "error"
    var captureDetail = ""
    var daemonError = ""
}

final class Controller: NSObject, NSApplicationDelegate, WKScriptMessageHandler, WKNavigationDelegate {
    let window = SetupWindow(contentRect: NSRect(x: 0, y: 0, width: 440, height: 588), styleMask: [.borderless, .miniaturizable], backing: .buffered, defer: false)
    var web: WKWebView!
    var timer: Timer?
    var loaded = false

    let app = Bundle.main.bundleURL
    var exe: String { app.appendingPathComponent("Contents/MacOS/fennec-cua").path }
    var link: String {
        let l = FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/bin/fennec-cua").path
        if let dest = try? FileManager.default.destinationOfSymbolicLink(atPath: l), dest == exe { return l }
        return exe
    }
    let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""
    let asksDirectCapture = ProcessInfo.processInfo.operatingSystemVersion.majorVersion >= 15

    // State of the checks that go through fennec-cua.
    var doctor = Doctor()
    var doctorRunning = false
    var lastDoctor = Date.distantPast
    var restartedForScreen = false
    var requesting = false
    var notice = ""

    func applicationDidFinishLaunching(_ note: Notification) {
        buildMenu()
        let content = NSView(frame: window.contentRect(forFrameRect: window.frame))
        content.wantsLayer = true
        content.layer?.cornerRadius = 12
        content.layer?.masksToBounds = true
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        config.userContentController.add(self, name: "setup")
        web = WKWebView(frame: content.bounds, configuration: config)
        web.autoresizingMask = [.width, .height]
        web.navigationDelegate = self
        web.setValue(false, forKey: "drawsBackground")
        content.addSubview(web)
        let strip = DragStrip(frame: NSRect(x: 0, y: content.bounds.height - 46, width: content.bounds.width, height: 46))
        strip.autoresizingMask = [.width, .minYMargin]
        content.addSubview(strip)

        window.contentView = content
        window.isOpaque = false
        window.backgroundColor = .clear
        window.hasShadow = true
        window.isReleasedWhenClosed = false
        window.title = "Fennec CUA Setup"
        window.center()

        if let page = Bundle.main.url(forResource: "index", withExtension: "html", subdirectory: "setup") {
            web.loadFileURL(page, allowingReadAccessTo: page.deletingLastPathComponent())
        }
        DistributedNotificationCenter.default().addObserver(forName: showRequest, object: nil, queue: .main) { [weak self] _ in self?.bringForward() }
        NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: window, queue: .main) { _ in NSApp.terminate(nil) }
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in self?.tick() }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows: Bool) -> Bool {
        bringForward()
        return true
    }

    func bringForward() {
        window.makeKeyAndOrderFront(nil)
        window.orderFrontRegardless()
        NSApp.activate(ignoringOtherApps: true)
    }

    func buildMenu() {
        let main = NSMenu()
        let appItem = NSMenuItem()
        main.addItem(appItem)
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "Quit Fennec CUA Setup", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu
        let editItem = NSMenuItem()
        main.addItem(editItem)
        let edit = NSMenu(title: "Edit")
        edit.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editItem.submenu = edit
        let windowItem = NSMenuItem()
        main.addItem(windowItem)
        let win = NSMenu(title: "Window")
        win.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        win.addItem(withTitle: "Minimize", action: #selector(NSWindow.miniaturize(_:)), keyEquivalent: "m")
        windowItem.submenu = win
        NSApp.mainMenu = main
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        loaded = true
        push()
        bringForward()
        tick()
    }

    // Only the bundled page loads; links open in the default browser.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if let url = action.request.url, url.isFileURL {
            decisionHandler(.allow)
            return
        }
        if let url = action.request.url, action.navigationType == .linkActivated, url.scheme == "https" {
            NSWorkspace.shared.open(url)
        }
        decisionHandler(.cancel)
    }

    var accessibility: Bool { AXIsProcessTrusted() }
    var screen: Bool { CGPreflightScreenCaptureAccess() }

    func tick() {
        push()
        // Once both grants are in, the daemon's own view decides the
        // last step: its test capture (and, on macOS 15 and later, whether
        // macOS has asked about direct capture).
        guard accessibility, screen, !doctorRunning, !requesting, doctor.capture != "ok" else { return }
        if Date().timeIntervalSince(lastDoctor) < 3 { return }
        runDoctor()
    }

    func runDoctor() {
        doctorRunning = true
        push()
        run(["doctor", "--json"]) { [weak self] out, _ in
            guard let self else { return }
            self.doctorRunning = false
            self.lastDoctor = Date()
            self.doctor = Self.parse(out)
            // The daemon checks Screen Recording once, at start; one that
            // started before the grant needs a restart to use it.
            if self.screen, self.doctor.daemonScreenRecording != "", self.doctor.daemonScreenRecording != "granted", !self.restartedForScreen {
                self.restartedForScreen = true
                self.run(["quit"]) { _, _ in self.lastDoctor = .distantPast; self.tick() }
            }
            self.push()
        }
    }

    static func parse(_ out: String) -> Doctor {
        var d = Doctor()
        guard let data = out.data(using: .utf8),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let checks = obj["checks"] as? [[String: Any]] else {
            d.daemonError = "fennec-cua doctor gave no answer"
            return d
        }
        for c in checks {
            let name = c["name"] as? String ?? ""
            let level = c["level"] as? String ?? ""
            let detail = c["detail"] as? String ?? ""
            switch name {
            case "Screen Recording":
                d.daemonScreenRecording = level == "ok" ? "granted" : "denied"
            case "screen capture":
                if level == "ok" {
                    d.capture = "ok"
                } else if detail.contains("has not yet asked") {
                    d.capture = "consent"
                } else {
                    d.capture = "error"
                    d.captureDetail = detail
                }
            case "daemon":
                if level == "fail" { d.daemonError = detail }
            default:
                break
            }
        }
        return d
    }

    func run(_ args: [String], done: @escaping (String, Int32) -> Void) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: exe)
        p.arguments = args
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        DispatchQueue.global().async {
            var out = ""
            do {
                try p.run()
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                p.waitUntilExit()
                out = String(data: data, encoding: .utf8) ?? ""
            } catch {
                out = ""
            }
            let status = p.isRunning ? -1 : p.terminationStatus
            DispatchQueue.main.async { done(out, status) }
        }
    }

    func state() -> [String: Any] {
        var capture = doctor.capture
        if !(accessibility && screen) {
            capture = "waiting"
        } else if requesting || (doctorRunning && capture == "") {
            capture = "checking"
        }
        return [
            "version": version,
            "accessibility": accessibility,
            "screen": screen,
            "asksDirectCapture": asksDirectCapture,
            "capture": capture,
            "captureDetail": doctor.captureDetail,
            "daemonError": doctor.daemonError,
            "notice": notice,
            "commands": [
                ["id": "claude", "name": "Claude Code", "command": "claude mcp add --scope user fennec-cua -- \(link) mcp"],
                ["id": "codex", "name": "Codex", "command": "codex mcp add fennec-cua -- \(link) mcp"],
                ["id": "skill", "name": "Agent skill", "command": "\(link) skill install --claude --codex"],
            ],
        ]
    }

    func push() {
        guard loaded, let data = try? JSONSerialization.data(withJSONObject: state()), let json = String(data: data, encoding: .utf8) else { return }
        web.evaluateJavaScript("window.setupRender && window.setupRender(\(json))")
    }

    func openPane(_ anchor: String) {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?\(anchor)") {
            NSWorkspace.shared.open(url)
        }
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard let body = message.body as? [String: Any], let action = body["action"] as? String else { return }
        notice = ""
        switch action {
        case "accessibility":
            // The prompt lists the app in the pane; the pane is where the
            // switch is.
            let opts = [kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary
            if !AXIsProcessTrustedWithOptions(opts) { openPane("Privacy_Accessibility") }
        case "screen":
            if !CGRequestScreenCaptureAccess() { openPane("Privacy_ScreenCapture") }
        case "capture":
            // The daemon raises macOS's direct-capture alert (see
            // `fennec-cua permissions --request`); the alert comes from the
            // daemon's app, which is this one.
            requesting = true
            push()
            run(["permissions", "--request"]) { [weak self] _, status in
                guard let self else { return }
                self.requesting = false
                if status != 0 { self.notice = "Fennec CUA could not ask macOS. Run `fennec-cua permissions --request` in a terminal to see why." }
                self.lastDoctor = .distantPast
                self.doctor.capture = ""
                self.runDoctor()
            }
        case "retry":
            doctor = Doctor()
            restartedForScreen = false
            run(["quit"]) { [weak self] _, _ in self?.lastDoctor = .distantPast; self?.runDoctor() }
        case "copy":
            if let text = body["text"] as? String {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(text, forType: .string)
            }
        case "close":
            window.performClose(nil)
            window.close()
        case "minimize":
            window.miniaturize(nil)
        default:
            break
        }
        push()
    }
}

guard claimSingleInstance() else { exit(0) }
let application = NSApplication.shared
let controller = Controller()
application.delegate = controller
application.setActivationPolicy(.regular)
application.run()
