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
                if phase == .active { L10n.preferences.systemLanguages = Locale.preferredLanguages }
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
