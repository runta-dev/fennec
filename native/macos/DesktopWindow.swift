import AppKit

// The desktop window (docs/desktop-app.md) is its own native program,
// Contents/MacOS/fennec-desktop (desktop/: GPUI with herdr-app's terminal core).
// It reads the board and sessions from the daemon itself; this class only starts
// it, brings it to the front, and stops it when Fennec quits.
final class DesktopWindow: NSObject {
    private let control: ControlClient
    private let fen: () -> URL
    var openPanel: () -> Void = {}
    var refreshSessions: () -> Void = {}
    private var process: Process?
    private var language = ""

    init(control: ControlClient, fen: @escaping () -> URL) {
        self.control = control
        self.fen = fen
        super.init()
        NotificationCenter.default.addObserver(self, selector: #selector(quit), name: NSApplication.willTerminateNotification, object: nil)
    }

    var isVisible: Bool { process?.isRunning == true }

    func show() {
        if let process, process.isRunning {
            NSRunningApplication(processIdentifier: process.processIdentifier)?.activate()
            return
        }
        let contents = fen().deletingLastPathComponent().deletingLastPathComponent()
        let process = Process()
        process.executableURL = contents.appendingPathComponent("MacOS/fennec-desktop")
        var environment = ProcessInfo.processInfo.environment
        // herdr-core for the window's terminals: the cloud computer's, through `fen hc`.
        environment["FENNEC_HC"] = contents.appendingPathComponent("Resources/fennec-hc").path
        // This Mac's own herdr-core (bundled, an isolated instance) for local shell tabs.
        environment["FENNEC_LOCAL_HC"] = contents.appendingPathComponent("Resources/fennec-hc-local").path
        environment["FENNEC_MENU_BAR_APP"] = contents.deletingLastPathComponent().path
        if !language.isEmpty { environment["FENNEC_LANGUAGE"] = language }
        process.environment = environment
        process.standardInput = FileHandle.nullDevice
        process.terminationHandler = { [weak self] ended in
            DispatchQueue.main.async { if self?.process === ended { self?.process = nil } }
        }
        do {
            try process.run()
            self.process = process
            refreshSessions()
        } catch {
            NSLog("Fennec: cannot open the desktop window: \(error.localizedDescription)")
        }
    }

    // The menu bar app hands over what it learns; the window asks the daemon
    // itself, so only the language is kept for the next start.
    func update(_ change: [String: Any]) {
        if let language = change["language"] as? String { self.language = language }
    }

    @objc private func quit() {
        process?.terminate()
    }
}
