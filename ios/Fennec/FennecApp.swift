import SwiftUI

@main
struct FennecApp: App {
    @State private var app = AppModel()
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
