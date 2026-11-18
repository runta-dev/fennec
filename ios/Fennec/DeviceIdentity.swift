import Security
import UIKit

/// This device's name on Runta, e.g. "Fennec iPhone (ssxd)": its API key is
/// named after it, so each sign-in replaces this device's own key and never
/// another device's. iOS exposes no hardware ID (the MAC address reads as
/// 02:00:00:00:00:00, and identifierForVendor resets on reinstall), so the
/// suffix is a random ID kept in the Keychain: it survives reinstalls and,
/// being this-device-only, is not copied to a new phone by backup or migration.
enum DeviceIdentity {
    static var keyName: String { "Fennec \(UIDevice.current.model) (\(suffix))" }

    static let suffix: String = {
        if let saved = load(), saved.count == 4 { return saved }
        // No look-alike characters (0/o, 1/l/i), so the name reads unambiguously.
        let alphabet = Array("abcdefghjkmnpqrstuvwxyz23456789")
        let made = String((0..<4).map { _ in alphabet.randomElement()! })
        save(made)
        return made
    }()

    private static let query: [String: Any] = [
        kSecClass as String: kSecClassGenericPassword,
        kSecAttrService as String: "com.runta.fennec.ios",
        kSecAttrAccount as String: "device-id",
    ]

    private static func load() -> String? {
        var q = query
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private static func save(_ value: String) {
        SecItemDelete(query as CFDictionary)
        var q = query
        q[kSecValueData as String] = Data(value.utf8)
        q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(q as CFDictionary, nil)
    }
}
