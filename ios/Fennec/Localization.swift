import Foundation
import Observation

/// UI preferences are observed without rebuilding the navigation stack.
@Observable
final class LanguagePreferences {
    private let defaults: UserDefaults
    var selection: String {
        didSet { defaults.set(selection, forKey: "appLanguage") }
    }
    var systemLanguages: [String] = Locale.preferredLanguages

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        selection = defaults.string(forKey: "appLanguage") ?? "system"
    }
}

/// iOS and macOS share stable message IDs and the same translation catalog.
enum L10n {
    static let preferences = LanguagePreferences()
    static let supportedLanguages = ["en-US", "zh-CN"]

    private struct Catalog: Decodable {
        var version: Int
        var messages: [String: [String: String]]
    }
    static let messages: [String: [String: String]] = {
        guard let url = Bundle.main.url(forResource: "translations", withExtension: "json"),
              let data = try? Data(contentsOf: url),
              let catalog = try? JSONDecoder().decode(Catalog.self, from: data), catalog.version == 1 else { return [:] }
        return catalog.messages
    }()

    static func resolveLanguage(_ selection: String, preferred: [String]) -> String {
        if supportedLanguages.contains(selection) { return selection }
        for language in preferred {
            if language.hasPrefix("zh") { return "zh-CN" }
            if language.hasPrefix("en") { return "en-US" }
        }
        return "en-US"
    }

    static var language: String {
        resolveLanguage(preferences.selection, preferred: preferences.systemLanguages)
    }

    static func text(_ key: String, language: String? = nil, values: [String: String] = [:]) -> String {
        let row = messages[key]
        var result = row?[language ?? self.language] ?? row?["en-US"] ?? key
        for (name, value) in values { result = result.replacingOccurrences(of: "{\(name)}", with: value) }
        return result
    }
}
