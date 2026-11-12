import AppKit

// CUAFixture is the scenario app for fennec-cua's comparison tests. With
// CUA_FIXTURE_SOCKET set it serves its true state on that unix socket; with
// CUA_FIXTURE_COVER set it runs as the "other app" cover window of the cover
// scene instead.

final class AppDelegate: NSObject, NSApplicationDelegate {
    var server: ControlServer?

    func applicationDidFinishLaunching(_ n: Notification) {
        buildMainMenu()
        Model.shared.clear()
        // Without CUA_FIXTURE_SOCKET (as when a backend under test launches the
        // app) serve on the default path, unless another instance already does.
        var path = ProcessInfo.processInfo.environment["CUA_FIXTURE_SOCKET"] ?? ""
        if path.isEmpty {
            let def = (NSTemporaryDirectory() as NSString).appendingPathComponent("cua-fixture.sock")
            if !socketAnswers(def) { path = def }
        }
        if !path.isEmpty {
            let s = ControlServer(path: path)
            do {
                try s.start()
                server = s
            } catch {
                FileHandle.standardError.write(Data("cua-fixture: control socket: \(error.localizedDescription)\n".utf8))
                exit(2)
            }
        }
        // Keep URLs and documents a cold launch handed over.
        let opened = Model.shared.opened
        _ = resetScene("basic")
        Model.shared.opened = opened
    }

    func application(_ app: NSApplication, open urls: [URL]) {
        for u in urls { Model.shared.opened.append(u.isFileURL ? u.path : u.absoluteString) }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ app: NSApplication) -> Bool { false }

    func applicationShouldTerminate(_ app: NSApplication) -> NSApplication.TerminateReply {
        cleanupBeforeExit()
        return .terminateNow
    }
}

func cleanupBeforeExit() {
    teardownScene()
    (NSApp.delegate as? AppDelegate)?.server?.stop()
}

let env = ProcessInfo.processInfo.environment
let app = NSApplication.shared
if let spec = env["CUA_FIXTURE_COVER"] {
    runCover(spec)
} else {
    let delegate = AppDelegate()
    app.delegate = delegate
    app.setActivationPolicy(.regular)
    signal(SIGPIPE, SIG_IGN)
    app.run()
}
