import AppKit
import Foundation
import WebKit
import ServiceManagement

final class PopupPanel: NSPanel {
    override var canBecomeKey: Bool { true }
    override func cancelOperation(_ sender: Any?) { orderOut(nil) }
}

final class PermissionScroller: NSScroller {
    override class var isCompatibleWithOverlayScrollers: Bool { true }

    override func drawKnobSlot(in slotRect: NSRect, highlight flag: Bool) {}

    override func drawKnob() {
        let knob = rect(for: .knob)
        guard !knob.isEmpty else { return }
        let thumb = NSRect(x: bounds.maxX - 6, y: knob.minY, width: 4, height: knob.height)
        NSColor.secondaryLabelColor.withAlphaComponent(0.35).setFill()
        NSBezierPath(roundedRect: thumb, xRadius: 2, yRadius: 2).fill()
    }
}

final class Companion: NSObject, NSApplicationDelegate, WKScriptMessageHandler, WKNavigationDelegate {
    private let control = ControlClient()
    private var stateTimer: Timer?
    private var stateLoading = false
	private var controlReady = false
	private var installationPending = true
	private var installedApplicationPath = Bundle.main.bundlePath
	private var controlStarting = false
	private var sessionServiceReady = false
	private var viewRevision = 0
    private var lastState: Data?
    private var accountEpoch = -1
    private var openedLoginURL: String?
    private var item: NSStatusItem!
    private let permissionPopover = NSPopover()
    private var nativeLanguage = "zh-CN"
    private var shownApprovalID: String?
    private var approvals: [[String:String]] = []
    private lazy var popup = PopupPanel(contentRect: NSRect(x: 0, y: 0, width: 440, height: 390), styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: true)
    private var outsideMonitor: Any?
    private var popupPresentationID: UUID?
    private var pageLoaded = false
    private var localMonitor: Any?
    private var web: WKWebView!
    private var authorized = false
    private var isOnline = true
    private var profileLoaded = false
    private var updateState: [String:Any] = [:]
    private var preparationState: [String:String] = [:]
    private var takeoverAgents = Set<String>()
    private var loadingRuntimes = false
    private var loadingBilling = false
    private var discoveringAgents = false
    private var visibleRefreshTab = "agents"
    private var rendererRecoveryTimes: [Date] = []
    private var lastLoginMessage = ""
    private var lastLoginBusy = false
    private var terminalTransport: TerminalPTY?
    private var terminalGeneration: String?
    private var terminalOutputWindow: TerminalOutputWindow?
    private var sessionReadRevision = 0
    private var launchRequests: [String:String] = [:]
    private let orange = NSColor(srgbRed: 243/255, green: 141/255, blue: 37/255, alpha: 1)
    private var restingFoxIcon: NSImage?
    private var foxAnimationTimer: Timer?
    private var foxMotionStarted: Double = 0
    // Magpie's decaying wing/bird rhythm; shared with SVG, no whole-body scaling.
    // time, left ear, right ear, eye openness, body rotation in degrees.
    private let foxMotionFrames: [[Double]] = [[0,0,0,1,0],[0.12,1,0,1,-2],[0.18,-0.3,0,1,-2],[0.26,0.25,0,1,-0.8],[0.32,0,0,1,0],[0.4,0,1,1,2],[0.46,0,-0.3,1,2],[0.56,0,0.25,1,0.6],[0.62,0,0,1,-0.5],[0.72,0,0,1,-0.2],[0.78,0,0,0.12,-0.1],[0.86,0,0,1,0],[1,0,0,1,0]]

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        installEditingCommands()
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        let icon = foxIcon()
        restingFoxIcon = icon
        item.button?.image = icon
        item.button?.toolTip = "Fennec"
        item.button?.target = self
        item.button?.action = #selector(statusItemClicked)
        item.button?.sendAction(on: [.leftMouseUp, .rightMouseUp])
        buildPopup()
	    prepareApplicationInstallation { [weak self] application in
	        guard let self,let application else{return}
	        self.control.useInstalledApplication(application)
	        self.installedApplicationPath=application
	        self.installationPending=false
	        self.connectControl()
	        self.stateTimer = Timer.scheduledTimer(withTimeInterval:0.5,repeats:true){[weak self] _ in guard let self else{return};if self.controlReady{self.pollState()}else{self.connectControl()}}
	        self.toggle()
	    }
	}

	private func connectControl() {
	    guard !controlReady,!controlStarting else{return}
	    controlStarting=true
        control.start { [weak self] success in
            guard let self else { return }
	        self.controlStarting=false
	        self.controlReady=success
	        guard success else{return}
            self.configureDefaultLoginItem()
	        self.importPreferences()
            self.refreshAgents()
            self.pollState()
        }
    }

    private func installEditingCommands() {
        // Accessory apps still need the standard responder-chain commands:
        // without an Edit menu, Command-V never reaches WKWebView's paste:.
        let menu = NSMenu()
        let appItem = NSMenuItem()
        appItem.submenu = NSMenu(title: "Fennec")
        menu.addItem(appItem)
        let editItem = NSMenuItem(title: "Edit", action: nil, keyEquivalent: "")
        let edit = NSMenu(title: "Edit")
        for (title, action, key) in [
            ("Undo", "undo:", "z"),
            ("Cut", "cut:", "x"),
            ("Copy", "copy:", "c"),
            ("Paste", "paste:", "v"),
            ("Select All", "selectAll:", "a")
        ] {
            let command = NSMenuItem(title: title, action: NSSelectorFromString(action), keyEquivalent: key)
            command.keyEquivalentModifierMask = .command
            edit.addItem(command)
        }
        editItem.submenu = edit
        menu.addItem(editItem)
        NSApp.mainMenu = menu
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
	    if installationPending{return false}
        if !popup.isVisible && popupPresentationID == nil { toggle() }
        return false
    }

    @objc private func toggle() {
        if popup.isVisible || popupPresentationID != nil { popupPresentationID = nil; popup.orderOut(nil); return }
        refreshAgents()
        if authorized, isOnline { loadRuntimes() }
        let request = UUID()
        popupPresentationID = request
        presentPopup(request, remaining: 40)
    }

    @objc private func statusItemClicked() {
	    if installationPending{NSApp.activate(ignoringOtherApps:true);return}
        if NSApp.currentEvent?.type == .rightMouseUp || NSApp.currentEvent?.modifierFlags.contains(.control) == true {
            animateFoxIcon()
            popupPresentationID = nil
            popup.orderOut(nil)
            let menu = NSMenu()
            let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0.2.0"
            let label = NSMenuItem(title: "Fennec \(version)", action: nil, keyEquivalent: "")
            label.isEnabled = false
            menu.addItem(label)
            if updateState["state"] as? String == "ready", let latest = updateState["latest"] as? String, !latest.isEmpty {
                let updateTitle = L10n.text("ui.restart_to_update", language: nativeLanguage) + " (\(latest))"
                let updateItem = NSMenuItem(title: updateTitle, action: #selector(installReadyUpdateFromMenu), keyEquivalent: "")
                updateItem.target = self
                menu.addItem(updateItem)
            }
            menu.addItem(.separator())
            for (title, action) in [("Restart", #selector(restartApp)), ("Exit", #selector(quitApp))] {
                let entry = NSMenuItem(title: title, action: action, keyEquivalent: "")
                entry.target = self
                menu.addItem(entry)
            }
            if let button = item.button {
                // Give the menu its own appearance context without recoloring the template icon.
                let anchor = NSView(frame: button.bounds)
                anchor.appearance = NSApp.effectiveAppearance
                button.addSubview(anchor)
                defer { anchor.removeFromSuperview() }
                menu.popUp(positioning: nil, at: NSPoint(x: 0, y: anchor.bounds.height + 4), in: anchor)
            }
        } else {
            if popup.isVisible { animateFoxIcon() }
            toggle()
        }
    }

    private func foxIcon(left: Double = 0, right: Double = 0, eye: Double = 1, body: Double = 0) -> NSImage {
        let icon = NSImage(size: NSSize(width: 18, height: 18), flipped: true) { _ in
            let transform = NSAffineTransform()
            transform.translateX(by: 9, yBy: 13.5)
            transform.rotate(byDegrees: body)
            transform.translateX(by: -9, yBy: -13.5)
            transform.concat()
            NSColor.black.setFill()
            let path = NSBezierPath()
            path.move(to: NSPoint(x: 1.5, y: 10.8))
            path.line(to: NSPoint(x: 1.5 + 1.2 * left, y: 2.2 + 1.4 * left))
            path.curve(to: NSPoint(x: 2.5 + 1.2 * left, y: 1.7 + 1.4 * left), controlPoint1: NSPoint(x: 1.5 + 1.2 * left, y: 1.3 + 1.4 * left), controlPoint2: NSPoint(x: 2 + 1.2 * left, y: 1.3 + 1.4 * left))
            path.line(to: NSPoint(x: 9, y: 5.8))
            path.line(to: NSPoint(x: 15.5 - 1.2 * right, y: 1.7 + 1.4 * right))
            path.curve(to: NSPoint(x: 16.5 - 1.2 * right, y: 2.2 + 1.4 * right), controlPoint1: NSPoint(x: 16 - 1.2 * right, y: 1.3 + 1.4 * right), controlPoint2: NSPoint(x: 16.5 - 1.2 * right, y: 1.3 + 1.4 * right))
            path.line(to: NSPoint(x: 16.5, y: 10.8))
            path.line(to: NSPoint(x: 9.6, y: 16.1))
            path.curve(to: NSPoint(x: 8.4, y: 16.1), controlPoint1: NSPoint(x: 9.2, y: 16.5), controlPoint2: NSPoint(x: 8.8, y: 16.5))
            path.close()
            for points in [
                [NSPoint(x: 2.25 + 1.2 * left, y: 3.1 + 1.4 * left), NSPoint(x: 4.65, y: 8), NSPoint(x: 2.25, y: 9.7)],
                [NSPoint(x: 15.75 - 1.2 * right, y: 3.1 + 1.4 * right), NSPoint(x: 13.35, y: 8), NSPoint(x: 15.75, y: 9.7)]
            ] {
                let cutout = NSBezierPath()
                cutout.move(to: points[0])
                cutout.line(to: points[1])
                cutout.line(to: points[2])
                cutout.close()
                path.append(cutout)
            }
            for rect in [NSRect(x: 5.3, y: 10.85 - 0.95 * eye, width: 1.9, height: 1.9 * eye),
                         NSRect(x: 10.8, y: 10.85 - 0.95 * eye, width: 1.9, height: 1.9 * eye),
                         NSRect(x: 8, y: 13.1, width: 2, height: 2)] {
                path.append(NSBezierPath(ovalIn: rect))
            }
            path.windingRule = .evenOdd
            path.fill()
            return true
        }
        icon.isTemplate = true
        return icon
    }

    private func animateFoxIcon() {
        foxAnimationTimer?.invalidate()
        guard let icon = restingFoxIcon else { return }
        item.button?.image = icon
        guard !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion else { return }
        foxMotionStarted = Date().timeIntervalSince1970 * 1000 + 40
        let started = foxMotionStarted
        let timer = Timer(timeInterval: 1.0 / 60.0, repeats: true) { [weak self] timer in
            guard let self else { timer.invalidate(); return }
            let progress = max(0, (Date().timeIntervalSince1970 * 1000 - started) / 900)
            if progress >= 1 || NSWorkspace.shared.accessibilityDisplayShouldReduceMotion {
                timer.invalidate(); self.item.button?.image = icon; self.foxAnimationTimer = nil; return
            }
            let upper = self.foxMotionFrames.firstIndex(where: { $0[0] >= progress }) ?? (self.foxMotionFrames.count - 1)
            let b = self.foxMotionFrames[max(1,upper)], a = self.foxMotionFrames[max(1,upper)-1]
            let t = max(0,min(1,(progress-a[0])/(b[0]-a[0])))
            var low = 0.0, high = 1.0
            for _ in 0..<16 {
                let u = (low+high)/2, v = 1-u
                let x = 0.9*v*v*u + 0.9*v*u*u + u*u*u
                if x < t { low = u } else { high = u }
            }
            let u = (low+high)/2
            let ease = t == 0 ? 0 : t == 1 ? 1 : 3*(1-u)*u*u + u*u*u
            let values = (1...4).map { a[$0]+(b[$0]-a[$0])*ease }
            self.item.button?.image = self.foxIcon(left: values[0], right: values[1], eye: values[2], body: values[3])
        }
        foxAnimationTimer = timer
        RunLoop.main.add(timer, forMode: .common)
    }

    @objc private func installReadyUpdateFromMenu() {
        mutate("update.install")
    }

    @objc private func restartApp() {
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/bin/sh")
        // Positional arguments preserve bundle paths without shell interpolation.
        task.arguments = ["-c", "while kill -0 \"$1\" 2>/dev/null; do sleep 0.1; done; /usr/bin/open -n \"$2\" --args --installed-application \"$2\"", "fennec-restart", String(ProcessInfo.processInfo.processIdentifier), installedApplicationPath]
        do { try task.run(); NSApp.terminate(nil) }
        catch { let alert = NSAlert(error: error); alert.runModal() }
    }

    private func configureDefaultLoginItem() {
        control.call("login-item.initialize") { [weak self] result in
            if case .success(let value) = result { self?.render("renderLoginItem",[value]) }
        }
    }
    private func renderLoginItem(_ error: Bool = false) {
	    guard controlReady else{return}
        control.call("login-item.status") { [weak self] result in
            if case .success(let value) = result { self?.render("renderLoginItem",[value]) }
        }
    }
    private func setLoginItem(_ enabled: Bool) {
        mutate(enabled ? "login-item.on" : "login-item.off") { [weak self] success,value in
            if success, let value { self?.render("renderLoginItem",[value]) }
            else { self?.renderLoginItem(true) }
        }
    }

    private func presentPopup(_ request: UUID, remaining: Int) {
        guard popupPresentationID == request else { return }
        guard pageLoaded else { return }
        if let button = item.button, let anchorWindow = button.window,
           anchorWindow.isVisible, let screen = anchorWindow.screen {
            let anchor = anchorWindow.convertToScreen(button.convert(button.bounds, to: nil))
            if let origin = PopupPlacement.origin(anchor: anchor, screen: screen.frame, visible: screen.visibleFrame, size: popup.frame.size, menuBarHeight: NSStatusBar.system.thickness) {
                popup.setFrame(NSRect(origin: origin, size: popup.frame.size), display: false, animate: false)
                popupPresentationID = nil
                popup.makeKeyAndOrderFront(nil)
                web.evaluateJavaScript("window.requestFoxMotion?.()", completionHandler:nil)
                NSApp.activate(ignoringOtherApps: true)
                return
            }
        }
        guard remaining > 0 else { popupPresentationID = nil; return }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) { [weak self] in
            self?.presentPopup(request, remaining: remaining - 1)
        }
    }

    private func dismissPopupIfOutside(_ event: NSEvent) {
        guard popup.isVisible || popupPresentationID != nil else { return }
        if event.window === popup { return }
        let point = event.window.map { $0.convertPoint(toScreen: event.locationInWindow) } ?? NSEvent.mouseLocation
        // WebKit/accessibility events may arrive without the expected NSWindow.
        // Only a screen-space click outside both surfaces dismisses the panel.
        if popup.frame.contains(point) { return }
        if let button = item.button, let window = button.window {
            let frame = window.convertToScreen(button.convert(button.bounds, to: nil))
            if frame.contains(point) { return }
        }
        popupPresentationID = nil
        popup.orderOut(nil)
    }

    private func buildPopup() {
        popup.isRestorable = false
        popup.animationBehavior = .none
        popup.isOpaque = false
        popup.backgroundColor = .clear
        popup.hasShadow = true
        popup.level = .popUpMenu
        popup.isReleasedWhenClosed = false
        popup.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        outsideMonitor = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] event in
            self?.dismissPopupIfOutside(event)
        }
        localMonitor = NSEvent.addLocalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] event in
            if let self = self, event.window !== self.popup, event.window !== self.item.button?.window {
                self.dismissPopupIfOutside(event)
            }
            return event
        }
        let configuration = WKWebViewConfiguration()
        configuration.userContentController.add(self, name: "fennec")
        web = WKWebView(frame: NSRect(x: 0, y: 0, width: 440, height: 390), configuration: configuration)
        web.navigationDelegate = self
        let controller = NSViewController()
        controller.view = web
        web.wantsLayer = true
        web.layer?.cornerRadius = 14
        web.layer?.masksToBounds = true
        popup.contentViewController = controller
        let folder = Bundle.main.resourceURL!.appendingPathComponent("UI")
        web.loadFileURL(folder.appendingPathComponent("index.html"), allowingReadAccessTo: folder)
    }

    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        pageLoaded = false
        if popup.isVisible { popupPresentationID = UUID(); popup.orderOut(nil) }
        closeTerminal()
        sessionReadRevision += 1
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        closeTerminal()
        sessionReadRevision += 1
        let now = Date()
        rendererRecoveryTimes.removeAll { now.timeIntervalSince($0) > 60 }
        // Avoid a restart loop if a renderer repeatedly crashes. Only the
        // local view is reloaded; Herdr retains the remote terminal lifetime.
        guard rendererRecoveryTimes.count < 2 else {
            NSLog("Fennec renderer recovery limit reached")
            return
        }
        rendererRecoveryTimes.append(now)
        webView.reload()
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        decisionHandler(navigationAction.request.url?.isFileURL == true ? .allow : .cancel)
    }

    private func renderLogin(_ message: String, _ busy: Bool) {
        lastLoginMessage = message
        lastLoginBusy = busy
        let data = try! JSONSerialization.data(withJSONObject: [message, busy])
        let json = String(data: data, encoding: .utf8)!
        web.evaluateJavaScript("window.renderLogin(...\(json))", completionHandler: nil)
    }

    private func renderRefreshActivity() {
        let busy = visibleRefreshTab == "sessions" ? loadingRuntimes : visibleRefreshTab == "account" ? loadingBilling : discoveringAgents
        web.evaluateJavaScript("window.renderRefreshing(\(busy ? "true" : "false"))", completionHandler: nil)
    }

    private func closeTerminal() {
        terminalGeneration = nil
        terminalOutputWindow?.close(); terminalOutputWindow = nil
        terminalTransport?.close()
        terminalTransport = nil
    }

    private func handleTerminal(_ message: [String:Any]) {
        guard let type = message["type"] as? String, let generation = message["generation"] as? String else { return }
        if type == "terminal-error" {
            let allowed = ["Error", "TypeError", "ReferenceError", "RangeError", "SyntaxError"]
            let kind = message["kind"] as? String ?? "Error"
            guard message["stage"] as? String == "renderer-init", allowed.contains(kind) else { return }
            NSLog("Fennec terminal renderer-init failed (%@)", kind)
            return
        }
        if type == "terminal-open" {
            guard UUID(uuidString: generation) != nil else { return }
            guard authorized, let runtimeID = preparationState["runtime_id"],
                  message["address"] is String, let sessionID = message["session_id"] as? String,
                  let cols = message["cols"] as? Int, let rows = message["rows"] as? Int,
                  !runtimeID.isEmpty, (1...500).contains(cols), (1...300).contains(rows) else {
                let payload = try! JSONSerialization.data(withJSONObject: [generation,true] as [Any])
                web.evaluateJavaScript("window.endSessionTerminal(...\(String(data:payload,encoding:.utf8)!))", completionHandler:nil)
                return
            }
            closeTerminal()
            let transport = TerminalPTY()
            let outputWindow = TerminalOutputWindow()
            terminalOutputWindow = outputWindow
            terminalTransport = transport; terminalGeneration = generation
            let executable = URL(fileURLWithPath:installedApplicationPath).appendingPathComponent("Contents/MacOS/fen")
            do {
                try transport.start(executable: executable, arguments: ["fen","attach",sessionID], cols: UInt16(cols), rows: UInt16(rows), output: { [weak self] bytes in
                    for offset in stride(from: 0, to: bytes.count, by: 16384) {
                        let chunk = bytes.subdata(in: offset..<min(offset + 16384, bytes.count))
                        guard outputWindow.reserve(chunk.count) else { return }
                        DispatchQueue.main.async {
                            guard let self = self, self.terminalGeneration == generation else { return }
                            let payload = try! JSONSerialization.data(withJSONObject: [generation,chunk.base64EncodedString()])
                            self.web.evaluateJavaScript("window.writeSessionTerminal(...\(String(data:payload,encoding:.utf8)!))") { _, error in
                                if error != nil { outputWindow.close() }
                            }
                        }
                    }
                }, exited: { [weak self] status in
                    DispatchQueue.main.async {
                        guard let self = self, self.terminalGeneration == generation else { return }
                        let payload = try! JSONSerialization.data(withJSONObject: [generation, status != 0] as [Any])
                        self.web.evaluateJavaScript("window.endSessionTerminal(...\(String(data:payload,encoding:.utf8)!))", completionHandler:nil)
                    }
                })
            } catch {
                let payload = try! JSONSerialization.data(withJSONObject: [generation,true] as [Any])
                web.evaluateJavaScript("window.endSessionTerminal(...\(String(data:payload,encoding:.utf8)!))", completionHandler:nil)
                closeTerminal()
            }
            return
        }
        guard terminalGeneration == generation else { return }
        if type == "terminal-link",let text=message["url"] as? String,text.utf8.count<=8192,
           let url=URL(string:text),url.scheme=="https",url.user==nil,url.password==nil {NSWorkspace.shared.open(url);return}
        if type == "terminal-credit", let count = message["bytes"] as? Int {
            terminalOutputWindow?.acknowledge(count)
        }
        if type == "terminal-close" { closeTerminal() }
        if type == "terminal-input", let text = message["data"] as? String, text.utf8.count <= 65536 {
            try? terminalTransport?.write(Data(text.utf8))
        }
        if type == "terminal-resize", let cols = message["cols"] as? Int, let rows = message["rows"] as? Int,
           (1...500).contains(cols), (1...300).contains(rows) {
            try? terminalTransport?.resize(cols: UInt16(cols), rows: UInt16(rows))
        }
    }

    private func copyCloudAttachCommand(_ address: String, _ sessionID: String) {
        guard authorized, let runtimeID = preparationState["runtime_id"],
              runtimeID.range(of: "^[a-f0-9-]{36}$", options: .regularExpression) != nil,
              address.range(of: "^t[0-9a-z]+$", options: .regularExpression) != nil,
              sessionID.range(of: "^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$", options: .regularExpression) != nil else { return }
        let command = "fen attach \(sessionID)"
        NSPasteboard.general.clearContents()
        let copied = NSPasteboard.general.setString(command, forType: .string)
        if let data = try? JSONSerialization.data(withJSONObject: [sessionID, copied] as [Any]),
           let arguments = String(data: data, encoding: .utf8) {
            web.evaluateJavaScript("window.renderAttachCopyResult(...\(arguments))", completionHandler: nil)
        }
    }

    private func renderLaunchStatus(_ agent: String, _ message: String) {
        let data = try! JSONSerialization.data(withJSONObject: [agent,message])
        web.evaluateJavaScript("window.renderLaunchStatus(...\(String(data: data, encoding: .utf8)!))", completionHandler: nil)
    }

    private func permissionText(_ key: String) -> String {
        L10n.text(key, language: nativeLanguage)
    }

    private func renderApprovals() {
        guard let request = approvals.first, let id = request["id"] else {
            permissionPopover.performClose(nil)
            shownApprovalID = nil
            return
        }
        guard shownApprovalID != id else { return }
        shownApprovalID = id
        permissionPopover.behavior = .applicationDefined
        permissionPopover.animates = true
        let contentWidth: CGFloat = 420
        let inset: CGFloat = 12
        let innerWidth = contentWidth - inset * 2
        let controller = NSViewController()
        controller.view = NSView(frame: NSRect(x: 0, y: 0, width: contentWidth, height: 1))
        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 8
        stack.translatesAutoresizingMaskIntoConstraints = false
        controller.view.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: controller.view.leadingAnchor, constant: inset),
            stack.widthAnchor.constraint(equalToConstant: innerWidth),
            stack.topAnchor.constraint(equalTo: controller.view.topAnchor, constant: inset)
        ])
        let title = NSTextField(labelWithString: permissionText(request["kind"] == "hook" ? "permission.hookTitle" : "permission.title"))
        title.font = .systemFont(ofSize: 14, weight: .semibold)
        stack.addArrangedSubview(title)
        let scroll = NSScrollView()
        scroll.verticalScroller = PermissionScroller()
        scroll.scrollerStyle = .overlay
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.borderType = .bezelBorder
        let command = NSTextView(frame: NSRect(x: 0, y: 0, width: innerWidth, height: 32))
        command.isEditable = false
        command.font = .monospacedSystemFont(ofSize: 12, weight: .regular)
        command.string = request["command"] ?? ""
        command.textContainerInset = NSSize(width: 4, height: 5)
        command.isHorizontallyResizable = false
        command.isVerticallyResizable = true
        command.textContainer?.containerSize = NSSize(width: innerWidth - 8, height: .greatestFiniteMagnitude)
        command.textContainer?.widthTracksTextView = true
        command.layoutManager?.ensureLayout(for: command.textContainer!)
        let textHeight = command.layoutManager?.usedRect(for: command.textContainer!).height ?? 16
        let commandHeight = min(100, max(32, ceil(textHeight) + 12))
        scroll.documentView = command
        stack.addArrangedSubview(scroll)
        scroll.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        scroll.heightAnchor.constraint(equalToConstant: commandHeight).isActive = true
        let note = NSTextField(wrappingLabelWithString: permissionText(request["kind"] == "hook" ? "permission.hookNote" : "permission.note"))
        note.font = .systemFont(ofSize: 11)
        note.textColor = .secondaryLabelColor
        note.preferredMaxLayoutWidth = innerWidth
        stack.addArrangedSubview(note)
        note.widthAnchor.constraint(equalToConstant: innerWidth).isActive = true
        let always = NSButton(title: permissionText("permission.always"), target: self, action: #selector(alwaysAllowCommand))
        let deny = NSButton(title: permissionText("permission.deny"), target: self, action: #selector(denyCommand))
        let allow = NSButton(title: permissionText("permission.allow"), target: self, action: #selector(allowCommand))
        for button in [always,deny,allow] { button.bezelStyle = .rounded }
        let spacer = NSView()
        spacer.setContentHuggingPriority(.defaultLow, for: .horizontal)
        let buttons = NSStackView(views: [always,spacer,deny,allow])
        buttons.spacing = 8
        stack.addArrangedSubview(buttons)
        buttons.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        controller.view.layoutSubtreeIfNeeded()
        let contentSize = NSSize(width: contentWidth, height: ceil(stack.fittingSize.height) + inset * 2)
        controller.view.setFrameSize(contentSize)
        permissionPopover.contentViewController = controller
        permissionPopover.contentSize = contentSize
        popup.orderOut(nil)
        if let button = item.button {
            permissionPopover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            NSApp.activate(ignoringOtherApps: true)
        }
    }

    @objc private func alwaysAllowCommand() { decideCommand(allow: true, remember: true) }

    @objc private func denyCommand() { decideCommand(allow: false, remember: false) }

    @objc private func allowCommand() { decideCommand(allow: true, remember: false) }

    @objc private func quitApp() { NSApp.terminate(nil) }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    private func render(_ function: String, _ arguments: [Any] = []) {
        guard let data = try? JSONSerialization.data(withJSONObject: arguments) else { return }
        web.evaluateJavaScript("window.\(function)(...\(String(decoding: data, as: UTF8.self)))", completionHandler: nil)
    }
    private func pollState() {
        guard controlReady,!stateLoading else { return }
        stateLoading = true
	    let revision = viewRevision
        control.call("state.get") { [weak self] result in
            guard let self else { return }
            self.stateLoading = false
	        guard revision == self.viewRevision else {self.pollState();return}
            if case .failure = result { self.controlReady = false; return }
            guard case .success(let value) = result, let state = value as? [String:Any],
                  let data = try? JSONSerialization.data(withJSONObject: state, options: [.sortedKeys]) else { return }
            guard data != self.lastState else { return }
            self.lastState = data
            self.applyState(state)
        }
    }
    private func applyState(_ state: [String:Any]) {
	    if let preferences=state["settings"] as? [String:Any],preferences["initialized"] as? Bool != true {render("renderMagpiePreference",[preferences["magpie"] as? Bool ?? false])}
	    if let preferences=state["settings"] as? [String:Any],preferences["initialized"] as? Bool == true {
	        render("renderPreferences",[preferences])
	        if let language=preferences["language"] as? String, language != nativeLanguage {nativeLanguage=language;shownApprovalID=nil}
	    }
        let account = state["account"] as? [String:Any] ?? [:]
        let epoch = account["epoch"] as? Int ?? 0
        if epoch != accountEpoch { accountEpoch = epoch; profileLoaded = false; openedLoginURL = nil; launchRequests.removeAll(); closeTerminal(); render("resetAgentLogin");render("resetNativeAccountViews") }
        let online = state["online"] as? Bool ?? true
        let reconnected = online && !isOnline
        isOnline = online
        let isAuthorized = account["status"] as? String == "authorized"
        if isAuthorized != authorized {
            authorized = isAuthorized
            if !authorized { profileLoaded = false }
            render(authorized ? "renderAuthorized" : "renderSignedOut")
            if authorized, online { loadProfile() }
        } else if authorized, reconnected, !profileLoaded {
            loadProfile()
        }
        if !authorized {
            let pending = account["status"] as? String == "authorizing"
            let code = account["user_code"] as? String ?? ""
            let loginErrors = ["authorization_expired":"[[i18n:ui.authorization_expired_sign_in_again]]", "session_expired":"[[i18n:ui.session_expired_sign_in_again]]", "access_denied":"[[i18n:ui.authorization_cancelled]]"]
            let errorCode = account["error"] as? String
            let message = pending ? (code.isEmpty ? "[[i18n:ui.connecting_to_runta]]" : "[[i18n:ui.confirm_this_code_in_your_browser]]" + code) : (errorCode.map { loginErrors[$0] ?? "[[i18n:ui.could_not_sign_in_try_again]]" } ?? "")
            renderLogin(message, pending || account["status"] as? String == "restoring")
            if pending, let link = account["verification_url"] as? String, link != openedLoginURL,
               let url = URL(string: link), url.scheme == "https", url.host == "dashboard.runta.com" {
                openedLoginURL = link; NSWorkspace.shared.open(url)
            }
        }
        let runtime = state["runtime"] as? [String:Any] ?? [:]
        let events = runtime["events"] as? [String:[String:String]] ?? [:]
        preparationState = events["runtime_progress"] ?? [:]
        if let id = runtime["runtime_id"] as? String { preparationState["runtime_id"] = id }
        if runtime["stage"] as? String == "failed" { preparationState["stage"] = "failed"; preparationState["status"] = "runtime_preparation_failed" }
        if runtime["stage"] as? String == "offline" || runtime["offline"] as? Bool == true || (!online && authorized && runtime["stage"] as? String != "disabled") {
            preparationState["stage"] = "offline"
            preparationState["status"] = "network_offline"
        }
	    if runtime["stage"] as? String == "disabled" { preparationState=["stage":"disabled"] }
        for (kind,field,function) in [("model_catalog","models","renderModelCatalog"),("route_results","results","renderRouteResults"),("provider_catalog","candidates","renderProviderCatalog")] {
            if let text = events[kind]?[field], let data = text.data(using: .utf8), let value = try? JSONSerialization.jsonObject(with: data) { render(function,[value]) }
        }
        render("renderPreparation", [preparationState])
        let services = state["services"] as? [String:Any] ?? [:]
        takeoverAgents = Set(services["selected"] as? [String] ?? [])
        let ready = services["broker_ready"] as? Bool == true
	    sessionServiceReady=ready
        if reconnected, authorized, ready { loadRuntimes() }
        if reconnected, authorized, visibleRefreshTab == "account" { loadBilling() }
        render("renderTakeover", [["agents":Array(takeoverAgents).sorted(),"activeAgents":ready ? services["active"] as? [String] ?? [] : [],"changing":false,"selectionKnown":services["selection_known"] as? Bool ?? false]])
        for (agent,coverage) in services["coverage"] as? [String:String] ?? [:] { render("renderEntryCoverage", [["agent":agent,"coverage":coverage]]) }
        if let listing = services["sessions"] as? [String:Any], let terminals = listing["terminals"] as? [[String:Any]] { render("renderHerdrSessions", [terminals]) }
        if let upd = state["update"] as? [String:Any] {
            updateState = upd
            render("renderUpdateState", [upd])
        }
        approvals = (services["approvals"] as? [[String:Any]] ?? []).map { row in row.reduce(into: [String:String]()) { result, pair in if let value = pair.value as? String { result[pair.key] = value } } }
        renderApprovals()
    }
    private func loadProfile() {
        let epoch = accountEpoch
        control.call("account.profile") { [weak self] result in
            guard let self, self.accountEpoch == epoch, case .success(let value) = result, let profile = value as? [String:Any] else { return }
            self.profileLoaded = true
            let name = profile["display_name"] as? String ?? profile["email"] as? String
            self.render("renderProfile", [name ?? "", name == nil])
        }
    }
    @objc private func refreshAgents() {
        guard controlReady,!discoveringAgents else { return }
        discoveringAgents = true; renderRefreshActivity()
        control.call("agents.list") { [weak self] result in
            guard let self else { return }; self.discoveringAgents = false; self.renderRefreshActivity()
            if case .success(let agents) = result { self.render("renderAgents", [agents]) }
        }
    }
    private func loadRuntimes() {
        guard authorized,isOnline,sessionServiceReady,!loadingRuntimes else { return }
        loadingRuntimes = true; renderRefreshActivity()
        let epoch = accountEpoch
        control.call("sessions.refresh") { [weak self] result in
            guard let self else { return }; self.loadingRuntimes = false; self.renderRefreshActivity()
            guard self.accountEpoch == epoch else { return }
            if case .success(let value) = result, let listing = value as? [String:Any], let rows = listing["terminals"] { self.render("renderHerdrSessions", [rows]) }
            else if self.isOnline { self.render("renderSessionRefreshError") }
        }
    }
    private func loadBilling() {
        guard authorized, !loadingBilling else { return }
        guard isOnline else {
            render("renderBilling", [["offline": true]])
            return
        }
        render("renderBillingLoading")
        loadingBilling = true; renderRefreshActivity()
        let epoch = accountEpoch
        control.call("account.billing") { [weak self] result in
            guard let self else { return }; self.loadingBilling = false; self.renderRefreshActivity()
            guard self.accountEpoch == epoch else { return }
            switch result {
            case .success(let value): self.render("renderBilling", [value])
            case .failure:
                if self.isOnline { self.render("renderBilling", [["error":"[[i18n:ui.unable_to_load_account_usage]]"]]) }
                else { self.render("renderBilling", [["offline": true]]) }
            }
        }
    }
    private func openBrowserSession(_ params: [String:Any], done: ((Bool) -> Void)? = nil) {
        let epoch=accountEpoch
        control.call("sessions.browser",params) { [weak self] result in
            guard let self,self.accountEpoch==epoch else{return}
            guard case .success(let value)=result,let reply=value as? [String:Any],let text=reply["url"] as? String,
                  let url=URL(string:text),url.scheme=="http",url.host=="127.0.0.1" else {done?(false);return}
            done?(NSWorkspace.shared.open(url))
        }
    }
    private func mutate(_ method: String, _ params: [String:Any] = [:], done: ((Bool,Any?) -> Void)? = nil) {
	    guard controlReady else{done?(false,nil);return}
	    viewRevision += 1
        let epoch = accountEpoch
        control.call(method, params) { [weak self] result in
            guard let self else { return }
            guard self.accountEpoch == epoch else { return }
            switch result {
            case .success(let value): done?(true,value)
            case .failure(let error):
                done?(false,nil)
                NSLog("Fennec control operation %@ failed: %@",method,error.localizedDescription)
                // Each operation owns its contextual feedback; background reads
                // must not produce a generic failure toast.
            }
            self.lastState = nil; self.pollState()
        }
    }
    private func decideCommand(allow: Bool, remember: Bool) {
        guard let id = shownApprovalID else { return }
        mutate("approvals.decide",["id":id,"allow":allow,"never_ask":remember])
    }
    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard message.frameInfo.isMainFrame, message.frameInfo.request.url?.isFileURL == true, let action = message.body as? String else { return }
        if action.hasPrefix("{") {
            if let data = action.data(using:.utf8),let value = (try? JSONSerialization.jsonObject(with:data)) as? [String:Any] { handleTerminal(value) }
            return
        }
        if action.hasPrefix("native-language:") {nativeLanguage=String(action.dropFirst(16));return}
        if action.hasPrefix("language:") { let language = String(action.dropFirst(9)); if ["zh-CN","zh-TW","en-US","en-GB","ja","ko"].contains(language) { nativeLanguage = language; shownApprovalID = nil; renderApprovals();mutate("settings.set",["language":language]) }; return }
	    if action.hasPrefix("settings:"),let data=String(action.dropFirst(9)).data(using:.utf8),let params=(try? JSONSerialization.jsonObject(with:data)) as? [String:Any] {mutate("settings.set",params);return}
        if action.hasPrefix("update-open-url:"),let data=String(action.dropFirst(16)).data(using:.utf8),let params=(try? JSONSerialization.jsonObject(with:data)) as? [String:Any],let text=params["url"] as? String,let url=URL(string:text),url.scheme=="https",(url.host=="github.com" || url.host=="runta.com" || url.host?.hasSuffix(".runta.com")==true) {NSWorkspace.shared.open(url);return}
        if action.hasPrefix("visible-tab:") { visibleRefreshTab = String(action.dropFirst(12)); renderRefreshActivity(); return }
        if action.hasPrefix("refresh:") { visibleRefreshTab = String(action.dropFirst(8)); if visibleRefreshTab == "sessions" { loadRuntimes() } else if visibleRefreshTab == "account" { loadBilling() } else { refreshAgents() }; return }
        if action.hasPrefix("toggle-takeover:") { let agent = String(action.dropFirst(16)); mutate("takeover.set",["agent":agent,"enabled":!takeoverAgents.contains(agent)]); return }
        if action.hasPrefix("approve:") || action.hasPrefix("deny:") { let id = String(action.split(separator:":",maxSplits:1).last ?? ""); mutate("approvals.decide",["id":id,"allow":action.hasPrefix("approve:")]); return }
        for prefix in ["send-session:","read-session:","stop-session:","copy-attach:","browser-attach:","launch-agent:","select-model:","get-model:","get-agent-account:","get-local-model:","start-agent-login:","finish-agent-login:","cancel-agent-login:"] where action.hasPrefix(prefix) {
            guard let data = String(action.dropFirst(prefix.count)).data(using:.utf8),let params = (try? JSONSerialization.jsonObject(with:data)) as? [String:Any] else { return }
            let id = params["session_id"] as? String ?? "", address = params["address"] as? String ?? ""
            switch prefix {
            case "start-agent-login:":
                let agent = params["agent"] as? String ?? ""
                let key = "login:" + agent
                let start: (String) -> Void = { [weak self] requestID in
                    guard let self else { return };self.launchRequests[key] = requestID
                    self.mutate("agent.login.start",["agent":agent,"session_id":requestID]) { [weak self] success,value in
                        guard let self else{return}
                        guard success,let receipt=value as? [String:Any],let sessionID=receipt["session_id"] as? String,let runtimeID=receipt["runtime_id"] as? String else {self.render("renderAgentLoginStarted",[NSNull(),agent]);return}
                        self.openBrowserSession(["session_id":sessionID,"expected_runtime_id":runtimeID]) { opened in
                            self.render("renderAgentLoginStarted",[opened ? receipt : NSNull(),agent])
                        }

                    }
                }
                let allocate: () -> Void = { [weak self] in
                    guard let self else{return}
                    self.launchRequests[key]=""
                    self.mutate("sessions.allocate") { [weak self] success,result in
                        if success,let allocation=result as? [String:Any],let requestID=allocation["session_id"] as? String {start(requestID)}
                        else {self?.launchRequests.removeValue(forKey:key);self?.render("renderAgentLoginStarted",[NSNull(),agent])}
                    }
                }
                if let requestID=launchRequests[key] {
                    if !requestID.isEmpty {
                        mutate("agent.login.finish",["agent":agent,"session_id":requestID]) { [weak self] success,value in
                            guard let self else{return}
                            if success,let state=value as? [String:Any] {
                                if state["signed_in"] as? Bool == true {
                                    self.launchRequests.removeValue(forKey:key)
                                    self.render("renderAgentLoginStarted",[NSNull(),agent])
                                    self.render("resetNativeAccountViews")
                                    return
                                }
                                if state["running"] as? Bool == false {allocate();return}
                            }
                            // Unknown outcomes retain the same launch identity.
                            start(requestID)
                        }
                    }
                } else {allocate()}
            case "finish-agent-login:":
                mutate("agent.login.finish",params) { [weak self] success,value in
                    if let state=value as? [String:Any],state["signed_in"] as? Bool==true || state["running"] as? Bool==false {self?.launchRequests.removeValue(forKey:"login:"+(params["agent"] as? String ?? ""))}
                    self?.render("renderAgentLoginFinished",[success ? value ?? NSNull() : NSNull()])
                }
            case "cancel-agent-login:":
                mutate("agent.login.cancel",params) { [weak self] success,_ in
                    if success {self?.launchRequests.removeValue(forKey:"login:"+(params["agent"] as? String ?? ""))}
                    self?.render("renderAgentLoginCanceled",[success])
                }
            case "copy-attach:": copyCloudAttachCommand(address,id)
            case "browser-attach:": openBrowserSession(params)
            case "stop-session:": mutate("sessions.stop",params) { [weak self] success,_ in self?.render(success ? "renderSessionStopped" : "renderSessionActionError",success ? [id] : []) }
            case "send-session:": mutate("sessions.send",params) { [weak self] success,_ in self?.render("renderSessionSent",[id,success]) }
            case "read-session:":
                sessionReadRevision += 1; let revision = sessionReadRevision
                mutate("sessions.read",params) { [weak self] success,value in guard let self,self.sessionReadRevision == revision else { return }; self.render("renderSessionDetail",[success ? value ?? ["error":true] : ["error":true]]) }
            case "get-agent-account:":
                let epoch=accountEpoch
                control.call("agent.account.get",params) { [weak self] result in
                    guard let self,self.accountEpoch==epoch else{return}
                    if case .success(let value)=result {self.render("renderNativeAccount",[value])}
                    else {self.render("renderNativeAccount",[["agent":params["agent"] as? String ?? "","location":params["location"] as? String ?? "local","status":"unavailable","accounts":[]] as [String:Any]])}
                }
            case "get-local-model:":
                let epoch=accountEpoch
                control.call("model.local.get",params) { [weak self] result in
                    guard let self,self.accountEpoch==epoch else{return}
                    if case .success(let value)=result {self.render("renderLocalModel",[value])}
                    else {self.render("renderLocalModel",[["agent":params["agent"] as? String ?? "","error":true] as [String:Any]])}
                }
            case "get-model:":
                let epoch=accountEpoch
                control.call("model.get",params) { [weak self] result in
                    guard let self,self.accountEpoch==epoch else{return}
                    if case .success(let value)=result {self.render("renderNativeModels",[value])}
                    else {self.render("renderNativeModelFailure",[params["agent"] as? String ?? ""])}
                }
            case "select-model:":
                render("renderModelSaving",[true]); mutate("model.set",params) { [weak self] success,value in if success,let value { self?.render(params["location"] as? String == "local" ? "renderLocalModel" : "renderNativeModels",[value]) }; self?.render("renderModelSaving",[false]) }
            case "launch-agent:":
                let key = (params["agent"] as? String ?? "") + ":" + (params["directory"] as? String ?? "")
                let launch: (String) -> Void = { [weak self] requestID in
                    guard let self = self else { return }
                    self.launchRequests[key] = requestID
                    var request = params; request["session_id"] = requestID
                    self.mutate("sessions.launch",request) { [weak self] success,_ in if success { self?.launchRequests.removeValue(forKey:key); self?.loadRuntimes() } }
                }
                if let requestID = launchRequests[key] { if !requestID.isEmpty { launch(requestID) } }
                else {
                    launchRequests[key] = ""
                    mutate("sessions.allocate") { [weak self] success,result in
                        guard success, let value = result as? [String:Any], let requestID = value["session_id"] as? String else { self?.launchRequests.removeValue(forKey:key); return }
                        launch(requestID)
                    }
                }
            default: break
            }
            return
        }
        switch action {
        case "fox-motion-ready":
            guard popup.isVisible, !NSWorkspace.shared.accessibilityDisplayShouldReduceMotion else { return }
            animateFoxIcon()
            if let data = try? JSONSerialization.data(withJSONObject: ["started":foxMotionStarted,"duration":900,"frames":foxMotionFrames]) {
                web.evaluateJavaScript("window.animateFox?.(\(String(decoding:data,as:UTF8.self)))", completionHandler:nil)
            }
        case "ready","refresh": lastState = nil; pollState(); refreshAgents(); renderLoginItem()
        case "login": mutate("account.login")
	    case "reauthenticate": mutate("account.login",["reauthenticate":true])
        case "logout": mutate("account.logout")
        case "load-billing": loadBilling()
        case "open-dashboard": if let url = URL(string: "https://dashboard.runta.com") { NSWorkspace.shared.open(url) }
        case "update-check": mutate("update.check") { [weak self] success,value in if success,let value { self?.render("renderUpdateState",[value]) } else { self?.lastState = nil; self?.pollState() } }
        case "update-download": mutate("update.download") { [weak self] success,value in if success,let value { self?.render("renderUpdateState",[value]) } else { self?.lastState = nil; self?.pollState() } }
        case "update-install": mutate("update.install") { [weak self] success,value in if success,let value { self?.render("renderUpdateState",[value]) } else { self?.render("renderUpdateInstallFailed") } }
        case "retry-runtime": mutate("runtime.retry")
        case "clear-trust": mutate("trust.clear") { [weak self] success,_ in self?.render("renderTrustResult",[success]) }
        case "cancel-session-read": sessionReadRevision += 1
        case "quit": quitApp()
        case "login-item:on": setLoginItem(true)
        case "login-item:off": setLoginItem(false)
        case "login-item:status": renderLoginItem()
        case "login-item:settings": if #available(macOS 13.0, *) { SMAppService.openSystemSettingsLoginItems() }
        default: break
        }
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        pageLoaded = true
        if let request = popupPresentationID { presentPopup(request, remaining: 40) }
        if popup.isVisible { web.evaluateJavaScript("window.requestFoxMotion?.()", completionHandler:nil) }
	    authorized = false
        let version = Bundle.main.object(forInfoDictionaryKey:"CFBundleShortVersionString") as? String ?? "0.2.0"
        render("renderAppVersion", ["Fennec " + version])
        importPreferences();lastState = nil; pollState(); refreshAgents(); renderLoginItem()
    }
	private func importPreferences() {
	    guard controlReady else{return}
	    web.evaluateJavaScript("window.exportLocalPreferences?.()") { [weak self] value,_ in
	        guard let self,let preferences=value as? [String:Any] else{return}
	        self.control.call("settings.import",preferences){[weak self] _ in self?.lastState=nil;self?.pollState()}
	    }
	}
    func applicationWillTerminate(_ notification: Notification) {
        stateTimer?.invalidate()
        closeTerminal()
        // The LaunchAgent and remote sessions intentionally outlive the UI.
    }

}

@main
struct FennecMain {
    static func main() {
	    if CommandLine.arguments.count==3,CommandLine.arguments[1]=="--platform-stop-ui" {
	        exit(runStopUIAdapter(CommandLine.arguments[2]) ? 0 : 1)
	    }
        if CommandLine.arguments.count == 3, CommandLine.arguments[1] == "--platform-login-item" {
            exit(runLoginItemAdapter(CommandLine.arguments[2]) ? 0 : 1)
        }
        let app = NSApplication.shared
        let delegate = Companion()
        app.delegate = delegate
        withExtendedLifetime(delegate) { app.run() }
    }
}
