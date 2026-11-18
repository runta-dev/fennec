import FennecCore
import Foundation
import Observation
import Security

/// Runta sign-in through the Fennec client's own account package
/// (internal/account via gomobile): the same device flow, token handling, and
/// expiry rules as the Mac app. Google, GitHub, and email-code sign-in run in
/// the system authentication window and create the account on first sign-in.
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

    let environment: RuntaEnvironment

    init(environment: RuntaEnvironment) {
        self.environment = environment
        var error: NSError?
        // Each environment keeps its own token, like the Mac's per-environment credentials.
        core = FennecmobileNewAuth(environment.rawValue, KeychainTokenStore(account: environment.keychainAccount), &error)!
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

    func signIn() {
        pairing = false
        let core = self.core
        Task.detached {
            let snapshot = core.login()
            await MainActor.run { self.apply(snapshot) }
        }
    }

    func signIn(method: String) {
        guard !isAuthorizing else { return }
        pairing = false
        status = .authorizing(nil)
        let core = self.core
        Task.detached {
            do {
                let snapshot = try goCall { core.loginMethod(method, error: $0) }
                await MainActor.run { self.apply(snapshot) }
            } catch {
                await MainActor.run { self.status = .failed("Couldn't start sign-in. Try again.") }
            }
        }
    }

    func cancelSignIn() {
        let core = self.core
        Task.detached { try? core.cancelLogin() }
    }

    func signOut() {
        let core = self.core
        profile = nil
        Task.detached { try? core.logout() }
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
        status = Self.status(for: snapshot)
        if !isAuthorizing { pairing = false }
        if status == .signedIn, previous != .signedIn { Task { await loadProfile() } }
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
