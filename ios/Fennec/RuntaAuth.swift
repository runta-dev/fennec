import AuthenticationServices
import CryptoKit
import FennecCore
import Foundation
import Observation
import Security

/// Runta sign-in through the Fennec client's own account package
/// (internal/account via gomobile): the same device flow, token handling, and
/// expiry rules as the Mac app. OAuth uses the system authentication window;
/// email OTP stays in native fields. The Go client creates the Runta API key.
@MainActor
@Observable
final class RuntaAuth {
    enum Status: Equatable {
        case restoring
        case signedOut
        case authorizing(URL?)
        case signedIn
        case failed(String)
    }

    private(set) var status: Status = .restoring
    private(set) var profile: Profile?

    let core: FennecmobileAuth
    private var watchTask: Task<Void, Never>?
    private var appliedRevision: UInt64?
    private(set) var epoch: Int64 = 0
    private(set) var emailCode = false
    private(set) var emailVerifying = false
    private(set) var emailError = false
    /// What the sign-in provider said about the last failure, shown on tap.
    private(set) var failureDetail: String?

    let environment: RuntaEnvironment

    init(environment: RuntaEnvironment) {
        self.environment = environment
        var error: NSError?
        // Each environment keeps its own token, like the Mac's per-environment credentials.
        core = FennecmobileNewAuth(environment.rawValue, KeychainTokenStore(account: environment.keychainAccount), &error)!
        // Sign-in replaces this device's own API key ("Fennec iPhone (ssxd)").
        core.setDeviceKeyName(DeviceIdentity.keyName)
        let core = self.core
        Task.detached {
            try? core.restore()
            let snapshot = core.snapshot()
            await MainActor.run { self.apply(snapshot) }
        }
        watch()
    }

    /// Stops watching the account; used when switching environments.
    func close() {
        watchTask?.cancel()
        core.close()
    }

    var isAuthorizing: Bool {
        if case .authorizing = status { return true }
        return false
    }

    var verificationURL: URL? {
        if case .authorizing(let url) = status { return url }
        return nil
    }

    /// True while redeeming a QR code from Fennec on the Mac (no browser sheet).
    private(set) var pairing = false

    /// Signs in with a QR code from Fennec on the Mac; the user approves it in
    /// the Mac's browser, and account.Manager.Redeem polls until then.
    func redeem(_ pairingURL: String) {
        pairing = true
        let core = self.core
        Task.detached {
            do {
                let snapshot = try goCall { core.redeem(pairingURL, error: $0) }
                await MainActor.run { self.apply(snapshot) }
            } catch {
                await MainActor.run {
                    self.pairing = false
                    self.status = .failed("That isn't a Fennec sign-in code.")
                }
            }
        }
    }

    func signIn(method: String) {
        guard !isAuthorizing else { return }
        pairing = false
        status = .authorizing(nil)
        // These Go entry points create the attempt synchronously and run all
        // network work in a goroutine, so Cancel cannot overtake its creation.
        do {
            let snapshot = try goCall {
                return core.nativeLogin(method, error: $0)
            }
            apply(snapshot)
        } catch {
            status = .failed("Couldn't start sign-in. Try again.")
        }
    }

    /// The raw nonce of the Sign in with Apple request, with its attempt's epoch.
    private var appleNonce: (value: String, epoch: Int64)?

    /// Starts Sign in with Apple and returns the SHA-256 (hex) of a new nonce
    /// for the Apple request, or nil if no attempt started. Supabase checks
    /// that the identity token carries it, so a token can't be replayed.
    func beginApple() -> String? {
        guard !isAuthorizing else { return nil }
        pairing = false
        status = .authorizing(nil)
        do { apply(try goCall { core.appleLogin($0) }) }
        catch {
            status = .failed("Couldn't start sign-in. Try again.")
            return nil
        }
        var bytes = [UInt8](repeating: 0, count: 32)
        guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else {
            failSignIn("no random nonce")
            return nil
        }
        let nonce = Data(bytes).base64EncodedString()
        appleNonce = (nonce, epoch)
        return SHA256.hash(data: Data(nonce.utf8)).map { String(format: "%02x", $0) }.joined()
    }

    /// Apple's sheet returned: give the identity token to the Go client.
    func completeApple(_ result: Result<ASAuthorization, Error>) {
        let nonce = appleNonce
        appleNonce = nil
        switch result {
        case .failure(let error):
            let ns = error as NSError
            if ns.domain == ASAuthorizationError.errorDomain, ns.code == ASAuthorizationError.canceled.rawValue {
                cancelSignIn()
            } else {
                failSignIn("Sign in with Apple: \(ns.domain) \(ns.code)")
            }
        case .success(let authorization):
            guard let nonce, nonce.epoch == epoch, isAuthorizing else { return }
            guard let credential = authorization.credential as? ASAuthorizationAppleIDCredential,
                  let data = credential.identityToken, let token = String(data: data, encoding: .utf8) else {
                failSignIn("Sign in with Apple returned no identity token")
                return
            }
            // Apple shares the name on the first authorization only.
            let name = credential.fullName.map { PersonNameComponentsFormatter.localizedString(from: $0, style: .default) } ?? ""
            if !core.completeAppleLogin(token, nonce: nonce.value, fullName: name, epoch: nonce.epoch) {
                failSignIn("Apple identity token not accepted")
            }
        }
    }

    /// The browser came back with something that isn't a sign-in callback:
    /// end the attempt and say so, rather than looking cancelled.
    func rejectCallback(_ url: URL) {
        // Parameter names only, never their values (a code or a token).
        func names(_ part: String?) -> String {
            (part ?? "").split(separator: "&").map { String($0.split(separator: "=", maxSplits: 1).first ?? "") }.joined(separator: ",")
        }
        let shape = "?\(names(url.query))#\(names(url.fragment))"
        failSignIn(url.scheme.map { "unexpected return: \($0)://\(url.host() ?? "")\(url.path())\(shape)" } ?? "unexpected return")
    }

    func cancelSignIn() {
        let core = self.core
        Task.detached { try? core.cancelLogin() }
    }

    func handleCallback(_ url: URL, epoch: Int64) {
        guard isAuthorizing, self.epoch == epoch else {
            failSignIn("callback for an older attempt (\(epoch) ≠ \(self.epoch), \(status))")
            return
        }
        if !core.completeNativeLogin(url.absoluteString, epoch: epoch) {
            failSignIn("callback not accepted (\(url.query?.split(separator: "&").map { $0.split(separator: "=").first ?? "" }.joined(separator: ",") ?? "no query"))")
        }
    }

    /// The sign-in window ended without a callback for a reason other than
    /// the person closing it; say so instead of looking cancelled.
    func windowFailed(_ error: Error) {
        let ns = error as NSError
        failSignIn("sign-in window: \(ns.domain) \(ns.code)")
    }

    /// Ends the attempt with "Couldn't finish sign-in" and a detail for the Details view.
    /// Recorded in the Go account manager, not only here: its next snapshot
    /// would otherwise replace this with a plain sign-out (that was the
    /// silence in 0.1.4 and 0.1.5).
    private func failSignIn(_ detail: String) {
        core.failLogin(detail)
        status = .failed(Self.message(for: "authorization_failed"))
        failureDetail = detail
    }

    private let turnstile = TurnstileTokenProvider()

    /// Runta sends the code through its own /v1/auth/login, which requires the
    /// Dashboard's Turnstile token; the provider obtains it without a page.
    func signInEmail(_ email: String) {
        guard !isAuthorizing else { return }
        pairing = false
        status = .authorizing(nil)
        Task {
            let token: String
            do {
                token = try await turnstile.token(siteKey: environment.turnstileSiteKey, origin: environment.dashboard, action: "login_magic_link")
            } catch {
                status = .failed(L10n.text("ios.verification_failed"))
                return
            }
            do { apply(try goCall { core.emailLogin(email, turnstileToken: token, error: $0) }) }
            catch { status = .failed(L10n.text("ios.invalid_email")) }
        }
    }

    func verifyEmail(_ code: String) {
        _ = core.verifyEmail(code, epoch: epoch)
    }

    func restartEmail(_ email: String) {
        guard emailCode, !emailVerifying else { return }
        try? core.cancelLogin()
        apply(core.snapshot())
        signInEmail(email)
    }

    func signOut() {
        let core = self.core
        profile = nil
        Task.detached { try? core.logout() }
    }

    /// The App Review demo's account, shown without signing in to Runta.
    func showDemoProfile(_ shown: Bool) {
        profile = shown ? ReviewDemo.profile : nil
    }

    func loadProfile() async {
        let core = self.core
        let json: String? = await Task.detached { try? goCall { core.profile($0) } }.value
        guard let json, let data = json.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return }
        profile = Profile(displayName: object["display_name"] as? String ?? "", email: object["email"] as? String ?? "")
    }

    /// Follows account.Manager's change notifications for the app's lifetime.
    private func watch() {
        let core = self.core
        watchTask = Task.detached { [weak self] in
            while !Task.isCancelled {
                let snapshot = core.wait(forChange: 30_000)
                guard let self else { return }
                await MainActor.run { self.apply(snapshot) }
            }
        }
    }

    private func apply(_ json: String) {
        guard let data = json.data(using: .utf8),
              let snapshot = try? JSONDecoder().decode(AccountSnapshot.self, from: data) else { return }
        if let revision = snapshot.revision {
            if let appliedRevision, revision < appliedRevision { return }
            appliedRevision = revision
        }
        let previous = status
        epoch = snapshot.epoch ?? 0
        emailCode = snapshot.email_code ?? false
        emailVerifying = snapshot.email_verifying ?? false
        emailError = snapshot.error == "invalid_email_code"
        failureDetail = snapshot.detail?.isEmpty == false ? snapshot.detail : nil
        status = Self.status(for: snapshot)
        if !isAuthorizing { pairing = false }
        if status == .signedIn, previous != .signedIn { Task { await loadProfile() } }
        // Signed out (or the key was rejected): the cached list was this account's.
        if previous == .signedIn, status != .signedIn, status != .restoring { SessionStore.clearListCache(environment) }
    }

    nonisolated static func status(for snapshot: AccountSnapshot) -> Status {
        switch snapshot.status {
        case "authorized": return .signedIn
        case "restoring": return .restoring
        case "authorizing": return .authorizing(snapshot.verification_url.flatMap(URL.init(string:)))
        default:
            if let error = snapshot.error { return .failed(message(for: error)) }
            return .signedOut
        }
    }

    /// Same error codes as the Mac app's account manager.
    nonisolated static func message(for code: String) -> String {
        switch code {
        case "access_denied": "Sign-in was declined."
        case "authorization_expired": "The sign-in link expired. Try again."
        case "session_expired": "Your session expired. Sign in again."
        case "credential_save_failed", "credentials_unavailable": "Couldn't save your sign-in on this iPhone. Try again."
        default: "Couldn't finish sign-in. Try again."
        }
    }
}

/// Calls a gomobile method that reports failure through an NSError pointer.
func goCall(_ body: (NSErrorPointer) -> String) throws -> String {
    var error: NSError?
    let result = body(&error)
    if let error { throw error }
    return result
}

/// account.Snapshot as JSON. Tokens never appear in it.
struct AccountSnapshot: Decodable {
    var status: String
    var revision: UInt64?
    var verification_url: String?
    var error: String?
    var epoch: Int64?
    var email_code: Bool?
    var email_verifying: Bool?
    var detail: String?
}

/// The Runta session token lives only in the iPhone Keychain.
final class KeychainTokenStore: NSObject, FennecmobileTokenStoreProtocol, @unchecked Sendable {
    private let query: [String: Any]

    init(account: String) {
        query = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: "com.runta.fennec.ios",
            kSecAttrAccount as String: account,
        ]
    }

    func loadToken(_ error: NSErrorPointer) -> String {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return "" }
        guard status == errSecSuccess, let data = out as? Data, let token = String(data: data, encoding: .utf8) else {
            error?.pointee = NSError(domain: NSOSStatusErrorDomain, code: Int(status))
            return ""
        }
        return token
    }

    func saveToken(_ token: String?) throws {
        SecItemDelete(query as CFDictionary)
        var q = query
        q[kSecValueData as String] = Data((token ?? "").utf8)
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else { throw NSError(domain: NSOSStatusErrorDomain, code: Int(status)) }
    }

    func clearToken() throws {
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }
}
