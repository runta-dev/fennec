import AuthenticationServices
import UIKit

/// Opens the Runta device-authorization page in an ASWebAuthenticationSession,
/// which shares Safari's cookies: if the dashboard is already signed in there,
/// the user only confirms the device. The device flow has no redirect back to
/// the app, so the page is closed by `finish()` once the token arrives.
@MainActor
final class WebSignIn: NSObject, ASWebAuthenticationPresentationContextProviding {
    private var session: ASWebAuthenticationSession?

    /// `onCancel` runs when the user closes the page before signing in.
    func start(_ url: URL, onCancel: @escaping @MainActor () -> Void) {
        finish()
        let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "fennec") { [weak self] _, error in
            Task { @MainActor in
                guard self?.session != nil else { return }
                self?.session = nil
                if let error = error as? ASWebAuthenticationSessionError, error.code == .canceledLogin {
                    onCancel()
                }
            }
        }
        session.presentationContextProvider = self
        session.prefersEphemeralWebBrowserSession = false
        self.session = session
        session.start()
    }

    func finish() {
        let session = self.session
        self.session = nil
        session?.cancel()
    }

    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated {
            UIApplication.shared.connectedScenes
                .compactMap { $0 as? UIWindowScene }
                .flatMap(\.windows)
                .first { $0.isKeyWindow } ?? ASPresentationAnchor()
        }
    }
}
