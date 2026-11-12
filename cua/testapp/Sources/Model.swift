import AppKit

// Model holds the fixture's true state. Every mutation happens on the main
// thread; the control server reads it through onMain.
final class Model {
    static let shared = Model()

    // gen changes on every reset so that delayed work and modal callbacks of
    // an earlier scene never write into the current one.
    var gen = 0
    var scene = ""
    var resets = 0
    var ready: (() -> Bool)?

    // Scene state.
    var readers: [String: () -> Any] = [:]
    var volatileReaders: [String: () -> Any] = [:]
    var presses: [String: Int] = [:]
    var edits: [String: Int] = [:]
    var commands: [[String: Any]] = []
    var events: [String: [[String: Any]]] = [:]
    var facts: [String: Any] = [:]

    // Scene resources released on reset.
    var retained: [AnyObject] = []
    var timers: [Timer] = []
    var teardowns: [() -> Void] = []

    // Menu toggles; reset with every scene.
    var statusBar = false
    var ruler = true
    var wrapToPage = false

    // Process facts kept across resets until the next reset clears them.
    var opened: [String] = []
    var freezes: [[String: Any]] = []

    static let eventKinds = ["clicks", "scrolls", "keys", "drags", "links", "cover_clicks"]

    func clear() {
        readers = [:]
        volatileReaders = [:]
        presses = [:]
        edits = [:]
        commands = []
        events = [:]
        for k in Model.eventKinds { events[k] = [] }
        facts = [:]
        retained = []
        timers = []
        teardowns = []
        statusBar = false
        ruler = true
        wrapToPage = false
        opened = []
        freezes = []
        ready = nil
    }

    func reg(_ id: String, _ f: @escaping () -> Any) { readers[id] = f }
    func regVolatile(_ id: String, _ f: @escaping () -> Any) { volatileReaders[id] = f }
    func press(_ id: String) { presses[id, default: 0] += 1 }
    func log(_ kind: String, _ e: [String: Any]) { events[kind, default: []].append(e) }

    // after runs f on the main queue unless a reset happened meanwhile.
    func after(_ seconds: Double, _ f: @escaping () -> Void) {
        let g = gen
        DispatchQueue.main.asyncAfter(deadline: .now() + seconds) {
            if Model.shared.gen == g { f() }
        }
    }

    // every runs f on a timer that also fires during menu tracking and modal loops.
    func every(_ seconds: Double, _ f: @escaping () -> Void) {
        let t = Timer(timeInterval: seconds, repeats: true) { _ in f() }
        RunLoop.main.add(t, forMode: .common)
        timers.append(t)
    }

    func snapshot() -> [String: Any] {
        var controls: [String: Any] = [:]
        for (k, f) in readers { controls[k] = f() }
        var volatile: [String: Any] = [:]
        for (k, f) in volatileReaders { volatile[k] = f() }
        volatile["resets"] = resets
        volatile["pid"] = Int(getpid())
        let instances = NSRunningApplication.runningApplications(withBundleIdentifier: fixtureBundleID)
        volatile["instance_pids"] = instances.map { Int($0.processIdentifier) }
        return [
            "scene": scene,
            "active": NSApp.isActive,
            "windows": windowList(),
            "modal": orNull(NSApp.modalWindow.map { $0.title as Any }),
            "focus": focusState(),
            "menu": ["status_bar": statusBar, "ruler": ruler, "wrap": wrapToPage ? "page" : "window"],
            "commands": commands,
            "presses": presses,
            "edits": edits,
            "controls": controls,
            "events": events,
            "facts": facts,
            "launch": [
                "args": Array(CommandLine.arguments.dropFirst()),
                "opened": opened,
                "instances": instances.count,
            ] as [String: Any],
            "freezes": freezes,
            "volatile": volatile,
        ]
    }
}

let fixtureBundleID = "com.runta.fennec.cua-fixture"

// isFixtureWindow says whether a window is one the state reports: fixture
// windows, sheets, alerts and titled panels, but not menus or tooltips.
func isFixtureWindow(_ w: NSWindow) -> Bool {
    if w is FixtureWindow || w.sheetParent != nil { return true }
    return w is NSPanel && w.styleMask.contains(.titled)
}

func windowID(_ w: NSWindow) -> String {
    if let id = w.identifier?.rawValue, !id.isEmpty { return id }
    if w is NSSavePanel { return "save-panel" }
    if w.sheetParent != nil { return "sheet" }
    return "panel"
}

// desktopFrame converts an AppKit frame (bottom-left origin) into desktop
// coordinates with a top-left origin on the primary display.
func desktopFrame(_ r: NSRect) -> [Int] {
    let h = NSScreen.screens.first?.frame.height ?? 0
    return [Int(r.minX.rounded()), Int((h - r.maxY).rounded()), Int(r.width.rounded()), Int(r.height.rounded())]
}

func windowList() -> [[String: Any]] {
    var out: [[String: Any]] = []
    for w in NSApp.windows where isFixtureWindow(w) && (w.isVisible || w.isMiniaturized) {
        var e: [String: Any] = [
            "id": windowID(w),
            "title": w.title,
            "kind": w.sheetParent != nil ? "sheet" : (w is NSPanel ? "panel" : "window"),
            "key": w.isKeyWindow,
            "main": w.isMainWindow,
            "visible": w.isVisible,
            "minimized": w.isMiniaturized,
            "transparent": w.alphaValue < 0.01,
            "fullscreen": w.styleMask.contains(.fullScreen),
            "on_active_space": w.isOnActiveSpace,
            "frame": desktopFrame(w.frame),
            "screen": w.screen.flatMap { s in NSScreen.screens.firstIndex(of: s) } ?? -1,
        ]
        if let p = w.sheetParent { e["owner"] = windowID(p) }
        if let s = w.attachedSheet { e["sheet"] = windowID(s) }
        out.append(e)
    }
    out.sort { ($0["id"] as! String, $0["title"] as! String) < ($1["id"] as! String, $1["title"] as! String) }
    return out
}

// elementID finds the fixture id of a responder: the control a field editor
// edits, or the nearest view with an identifier.
func elementID(_ r: NSResponder?) -> Any {
    var v: NSView?
    if let tv = r as? NSTextView, tv.isFieldEditor {
        v = tv.delegate as? NSView
    } else {
        v = r as? NSView
    }
    while let cur = v {
        if let id = cur.identifier?.rawValue, !id.isEmpty, !id.hasPrefix("_") { return id }
        v = cur.superview
    }
    return NSNull()
}

func focusState() -> [String: Any] {
    guard let w = NSApp.keyWindow else { return ["window": NSNull(), "element": NSNull()] }
    return ["window": windowID(w), "element": elementID(w.firstResponder)]
}

func checkState(_ s: NSControl.StateValue) -> String {
    switch s {
    case .on: return "on"
    case .mixed: return "mixed"
    default: return "off"
    }
}

func modifierNames(_ f: NSEvent.ModifierFlags) -> [String] {
    var a: [String] = []
    if f.contains(.control) { a.append("ctrl") }
    if f.contains(.option) { a.append("alt") }
    if f.contains(.shift) { a.append("shift") }
    if f.contains(.command) { a.append("cmd") }
    return a
}

func orNull(_ v: Any?) -> Any { v ?? NSNull() }
