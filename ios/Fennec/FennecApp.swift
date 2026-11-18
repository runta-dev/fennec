import SwiftUI

@main
struct FennecApp: App {
    @State private var app = AppModel()
    @Environment(\.scenePhase) private var scenePhase
    @AppStorage("appearance") private var appearance = "system"
    @AppStorage("didFinishConnecting") private var didFinishConnecting = false

    var body: some Scene {
        WindowGroup {
            Group {
                if app.auth.status == .restoring && !app.preview {
                    Theme.bg.ignoresSafeArea()
                } else if app.auth.status == .signedIn || app.preview {
                    if didFinishConnecting || app.preview {
                        HomeView()
                    } else {
                        ConnectingView { didFinishConnecting = true }
                    }
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
                if url.scheme == "fennec", app.auth.status != .signedIn { app.signIn(pairing: url.absoluteString) }
            }
            .onChange(of: app.auth.status) { _, status in
                if status == .signedOut { didFinishConnecting = false }
            }
        }
    }
}
