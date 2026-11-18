import SwiftUI
import FennecCore

@main
struct FennecApp: App {
    @State private var app = AppModel()
    @Environment(\.scenePhase) private var scenePhase
    @AppStorage("appearance") private var appearance = "system"

    var body: some Scene {
        WindowGroup {
            Group {
                if app.auth.status == .restoring && !app.preview {
                    Theme.bg.ignoresSafeArea()
                } else if app.auth.status == .signedIn || app.preview {
                    HomeView()
                } else {
                    ScanSignInView()
                }
            }
            .id(app.environment)
            .environment(app)
            .environment(app.auth)
            .environment(app.store)
            .tint(Theme.fg)
            .environment(\.locale, Locale(identifier: L10n.language))
            .onChange(of: scenePhase) { _, phase in
                if phase == .active {
                    L10n.preferences.systemLanguages = Locale.preferredLanguages
                    // Back in the foreground: check the connection now, not at the next list.
                    if let core = app.store.core { Task.detached { core.warm() } }
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
                    if app.auth.status != .signedIn && !app.auth.isAuthorizing { app.signIn(pairing: url.absoluteString) }
                default: break
                }
            }
        }
    }
}
