import SwiftUI
import FennecCore

@main
struct FennecApp: App {
    @State private var app: AppModel
    /// Keeps the board stream open for a moment after leaving the app (FEN-234).
    @State private var grace = BackgroundGrace()
    @Environment(\.scenePhase) private var scenePhase

    init() {
        let app = AppModel()
        _app = State(initialValue: app)
        BackgroundRefresh.app = app
    }
    @AppStorage("appearance") private var appearance = "system"

    var body: some Scene {
        WindowGroup {
            Group {
                if app.auth.status == .restoring && !app.preview {
                    Theme.bg.ignoresSafeArea()
                } else if app.auth.status == .signedIn || app.preview {
                    MainView()
                } else {
                    ScanSignInView()
                }
            }
            .dismissesKeyboardOnOutsideTap()
            .id(app.environment)
            .environment(app)
            .environment(app.auth)
            .environment(app.store)
            .tint(Theme.fg)
            .environment(\.locale, Locale(identifier: L10n.language))
            .onChange(of: scenePhase) { _, phase in
                if phase == .active {
                    L10n.preferences.systemLanguages = Locale.preferredLanguages
                    // Back within the grace period: the stream is still open.
                    grace.cancel()
                    // Back in the foreground: check the connection now, not at the next list.
                    if let core = app.store.core { Task.detached { core.warm() } }
                }
                if phase == .background {
                    // FEN-234: keep the board stream for a short while, so a
                    // quick return finds the list live, and have iOS refresh
                    // the cached list now and then while the app is away.
                    BackgroundRefresh.schedule()
                    grace.begin {
                        app.queues.stopWatching()
                        // FEN-68: the connection may die while the app is
                        // suspended. Say so now, so the first read back in the
                        // foreground checks it (and dials a new one alongside)
                        // instead of hanging on it.
                        app.store.core?.suspend()
                    }
                }
            }
            .onChange(of: app.auth.status == .signedIn) { _, signedIn in
                // Signed in (or restored at launch): connect while the home
                // screen appears, and time the first list from here.
                guard signedIn, let core = app.store.core else { return }
                app.store.startedAt = .now
                app.store.firstListSeconds = nil
                Task.detached { core.warm() }
            }
            .preferredColorScheme(appearance == "dark" ? .dark : appearance == "light" ? .light : nil)
            // QR codes scanned with the system Camera open fennec://pair links.
            .onOpenURL { url in
                switch FennecmobileDeepLinkRoute(url.absoluteString) {
                case "auth/callback": app.auth.handleCallback(url, epoch: app.auth.epoch)
                case "pair":
                    // Not in the demo: it stays on the phone until Sign out.
                    if app.auth.status != .signedIn && !app.auth.isAuthorizing && !app.preview { app.signIn(pairing: url.absoluteString) }
                default: break
                }
            }
        }
        // FEN-234: iOS wakes the app now and then to read the home list.
        .backgroundTask(.appRefresh(BackgroundRefresh.identifier)) { await BackgroundRefresh.run() }
    }
}
