import AuthenticationServices
import FennecCore
import UIKit

/// Opens the Runta device-authorization page in an ASWebAuthenticationSession,
/// for native OAuth or hosted device authorization. Native OAuth returns its
/// callback to the Go login state machine. Hosted login closes through `finish()`
/// once the independent device credential arrives.
@MainActor
final class WebSignIn: NSObject, ASWebAuthenticationPresentationContextProviding {
    private var session: ASWebAuthenticationSession?
    private var generation = UUID()

    /// `onCancel` runs when the user closes the page before signing in.
    /// `onInvalid` runs when it returns something that isn't a sign-in callback.
    func start(_ url: URL, onCallback: @escaping @MainActor (URL) -> Void = { _ in },
               onInvalid: (@MainActor (URL) -> Void)? = nil, onFailure: (@MainActor (Error) -> Void)? = nil,
               onCancel: @escaping @MainActor () -> Void) {
        finish()
        let generation = UUID()
        self.generation = generation
        let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "fennec") { [weak self] callback, error in
            Task { @MainActor in
                guard self?.session != nil, self?.generation == generation else { return }
                self?.session = nil
                // Closing the window is a cancel; any other error is a failure
                // to show (onFailure), never a silent return to sign-in.
                if let error {
                    let closed = (error as NSError).domain == ASWebAuthenticationSessionErrorDomain
                        && (error as NSError).code == ASWebAuthenticationSessionError.canceledLogin.rawValue
                    if closed || onFailure == nil { onCancel() } else { onFailure?(error) }
                }
                else if let callback, FennecmobileDeepLinkRoute(callback.absoluteString) == "auth/callback" { onCallback(callback) }
                else if let callback, let onInvalid { onInvalid(callback) }
                else { onCancel() }
            }
        }
        session.presentationContextProvider = self
        session.prefersEphemeralWebBrowserSession = false
        self.session = session
        if !session.start() {
            self.session = nil
            onCancel()
        }
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
