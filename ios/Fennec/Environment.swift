import FennecCore
import Foundation
import Observation
import SwiftUI

/// Runta deployment zones, named as in internal/runta (ResolveEnvironment).
enum RuntaEnvironment: String, CaseIterable, Identifiable {
    case production, integration

    var id: String { rawValue }
    var label: String { self == .production ? "Production" : "Integration" }
    /// Short badge, as in the Mac panel header. Production shows none.
    var badge: String? { self == .production ? nil : "INT" }
    var keychainAccount: String { self == .production ? "runta" : "runta-\(rawValue)" }

    /// The environment of a Runta API origin carried in a pairing QR code.
    init?(api: String) {
        switch api.trimmingCharacters(in: CharacterSet(charactersIn: "/")) {
        case "https://api.runta.com": self = .production
        case "https://api.runta.xyz": self = .integration
        default: return nil
        }
    }

    /// Saved choice; `-RuntaEnvironment integration` in the scheme overrides it.
    static var saved: RuntaEnvironment {
        get { UserDefaults.standard.string(forKey: "RuntaEnvironment").flatMap { RuntaEnvironment(rawValue: $0) } ?? .production }
        set { UserDefaults.standard.set(newValue.rawValue, forKey: "RuntaEnvironment") }
    }
}

/// Owns the account and session store for the selected environment and
/// rebuilds both when the developer switch changes it.
@MainActor
@Observable
final class AppModel {
    private(set) var environment: RuntaEnvironment
    private(set) var auth: RuntaAuth
    private(set) var store: SessionStore
    let preview: Bool

    init() {
        #if DEBUG
        preview = UserDefaults.standard.bool(forKey: "PreviewSignedIn")
        #else
        preview = false
        #endif
        let environment = RuntaEnvironment.saved
        let auth = RuntaAuth(environment: environment)
        self.environment = environment
        self.auth = auth
        self.store = preview ? SessionStore.demo() : SessionStore.live(auth: auth)
    }

    /// Signs in with a QR code from Fennec on the Mac, switching to the Mac's
    /// Runta environment first. Returns a message when the code isn't usable.
    @discardableResult
    func signIn(pairing url: String) -> String? {
        var error: NSError?
        let api = FennecmobilePairingAPI(url, &error)
        guard error == nil, let environment = RuntaEnvironment(api: api) else {
            return "That isn't a Fennec sign-in code."
        }
        switchEnvironment(to: environment)
        auth.redeem(url)
        return nil
    }

    func switchEnvironment(to environment: RuntaEnvironment) {
        guard environment != self.environment else { return }
        RuntaEnvironment.saved = environment
        store.core?.close()
        auth.close()
        let auth = RuntaAuth(environment: environment)
        self.environment = environment
        self.auth = auth
        self.store = preview ? SessionStore.demo() : SessionStore.live(auth: auth)
        UserDefaults.standard.set(false, forKey: "didFinishConnecting")
    }
}

/// Developer switch, same gesture as the Mac panel: five taps on the Fennec
/// mark within 1.8 seconds opens the environment picker.
struct EnvironmentSwitchGesture: ViewModifier {
    @Environment(AppModel.self) private var app
    @State private var taps: [Date] = []
    @State private var showPicker = false

    func body(content: Content) -> some View {
        content
            .contentShape(Rectangle())
            .onTapGesture {
                let now = Date()
                taps = (taps + [now]).filter { now.timeIntervalSince($0) < 1.8 }
                if taps.count >= 5 {
                    taps = []
                    showPicker = true
                }
            }
            .confirmationDialog("Runta environment", isPresented: $showPicker, titleVisibility: .visible) {
                ForEach(RuntaEnvironment.allCases) { environment in
                    Button(environment == app.environment ? "\(environment.label) ✓" : environment.label) {
                        app.switchEnvironment(to: environment)
                    }
                }
            } message: {
                Text("Switching signs you in to a different Runta account store. Each environment keeps its own sign-in.")
            }
    }
}

extension View {
    func environmentSwitchGesture() -> some View { modifier(EnvironmentSwitchGesture()) }
}

struct EnvironmentBadge: View {
    var environment: RuntaEnvironment
    var body: some View {
        if let badge = environment.badge {
            Text(badge)
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(Theme.accent)
                .padding(.horizontal, 7)
                .padding(.vertical, 2)
                .overlay(Capsule().strokeBorder(Theme.accent.opacity(0.6), lineWidth: 1))
                .accessibilityLabel("\(environment.label) environment")
        }
    }
}
