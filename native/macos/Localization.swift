import Foundation

// Both AppKit and the WebView use the same message IDs and translation catalog.
enum L10n {
    private struct Catalog: Decodable {
        let version: Int
        let messages: [String: [String: String]]
    }
    private static let messages: [String: [String: String]] = {
        guard let url = Bundle.main.url(forResource: "translations", withExtension: "json", subdirectory: "UI"),
              let data = try? Data(contentsOf: url),
              let catalog = try? JSONDecoder().decode(Catalog.self, from: data), catalog.version == 1 else { return [:] }
        return catalog.messages
    }()
    static func text(_ key: String, language: String) -> String {
        let locale: String
        if language.hasPrefix("zh-Hant") || language == "zh-TW" || language == "zh-HK" { locale = "zh-TW" }
        else if language.hasPrefix("zh") { locale = "zh-CN" }
        else if language.hasPrefix("ja") { locale = "ja" }
        else if language.hasPrefix("ko") { locale = "ko" }
        else { locale = "en-US" }
        let row = messages[key]
        let value = row?[locale] ?? (locale == "zh-TW" ? row?["zh-CN"] : nil) ?? FennecEnglish.messages[key] ?? key
        return displayText(value, locale: locale)
    }
    static func displayText(_ text: String, locale: String) -> String {
        let separator = locale == "ja" ? "\u{3001}" : locale.hasPrefix("zh") ? "\u{ff0c}" : " · "
        return text.replacingOccurrences(of: "...", with: "…")
            .replacingOccurrences(of: "[\\u3002\\uFF0E](?=\\S)", with: separator, options: .regularExpression)
            .replacingOccurrences(of: "[\\u3002\\uFF0E]$", with: "", options: .regularExpression)
            .replacingOccurrences(of: "\\.\\s+(?=\\S)", with: separator, options: .regularExpression)
            .replacingOccurrences(of: "\\.$", with: "", options: .regularExpression)
    }
}
