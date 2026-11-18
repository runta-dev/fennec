import LinkPresentation
import SwiftUI
import UIKit

/// Rich cards for web links in a message, from the page's Open Graph data
/// through LinkPresentation (what Messages uses). The link stays in the text;
/// the card is added below it and loads after the text, so it never delays
/// or reflows what's already shown.
enum LinkCards {
    static let limit = 2

    /// Public https links worth a card, in order, without duplicates. Code is
    /// skipped (a URL in a command is not something to preview), as are local
    /// and private addresses and cloud computers' own published ports.
    static func urls(in text: String) -> [URL] {
        let prose = text
            .replacingOccurrences(of: #"(?s)```.*?(```|$)"#, with: " ", options: .regularExpression)
            .replacingOccurrences(of: #"`[^`\n]*`"#, with: " ", options: .regularExpression)
        guard let detector = try? NSDataDetector(types: NSTextCheckingResult.CheckingType.link.rawValue) else { return [] }
        var found: [URL] = []
        for match in detector.matches(in: prose, range: NSRange(prose.startIndex..., in: prose)) {
            guard let url = match.url, previewable(url), !found.contains(where: { same($0, url) }) else { continue }
            found.append(url)
            if found.count == limit { break }
        }
        return found
    }

    static func previewable(_ url: URL) -> Bool {
        guard url.scheme?.lowercased() == "https", let host = url.host()?.lowercased(), host.contains(".") else { return false }
        if host.hasSuffix(".local") || host.hasSuffix(".internal") || host.hasSuffix(".localhost") || host.contains(":") { return false }
        // A cloud computer's published port: {port}-{runtime}.runta.dev/.xyz/.sh.
        if host.range(of: #"^[0-9]+-[0-9a-z-]+\.runta\.(dev|xyz|sh)$"#, options: .regularExpression) != nil { return false }
        let octets = host.split(separator: ".").compactMap { Int($0) }
        if octets.count == 4, host.allSatisfy({ $0.isNumber || $0 == "." }) {
            switch (octets[0], octets[1]) {
            case (10, _), (127, _), (0, _), (192, 168), (169, 254): return false
            case (172, 16...31), (100, 64...127): return false
            default: return true
            }
        }
        return true
    }

    private static func same(_ a: URL, _ b: URL) -> Bool {
        a.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/")) == b.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    }
}

/// What a card shows, fetched once per URL for the app's lifetime.
struct LinkInfo {
    var title: String?
    var host: String
    var image: UIImage?
}

@MainActor
final class LinkMetadataCache {
    static let shared = LinkMetadataCache()
    private var done: [URL: LinkInfo?] = [:]
    private var running: [URL: Task<LinkInfo?, Never>] = [:]

    func info(for url: URL) async -> LinkInfo? {
        if let cached = done[url] { return cached }
        if let task = running[url] { return await task.value }
        let task = Task<LinkInfo?, Never> {
            let provider = LPMetadataProvider()
            provider.timeout = 10
            guard let metadata = try? await provider.startFetchingMetadata(for: url) else { return nil }
            var image = await Self.image(metadata.imageProvider)
            if image == nil { image = Self.iconLarge(await Self.image(metadata.iconProvider)) }
            let title = metadata.title?.trimmingCharacters(in: .whitespacesAndNewlines)
            guard title?.isEmpty == false || image != nil else { return nil }
            return LinkInfo(title: title, host: (metadata.url ?? url).host() ?? url.absoluteString, image: image)
        }
        running[url] = task
        let info = await task.value
        running[url] = nil
        done[url] = info
        return info
    }

    private nonisolated static func image(_ provider: NSItemProvider?) async -> UIImage? {
        guard let provider, provider.canLoadObject(ofClass: UIImage.self) else { return nil }
        return await withCheckedContinuation { continuation in
            provider.loadObject(ofClass: UIImage.self) { object, _ in continuation.resume(returning: object as? UIImage) }
        }
    }

    /// Site icons are tiny; only a reasonably sized one is worth a thumbnail.
    private static func iconLarge(_ icon: UIImage?) -> UIImage? {
        guard let icon, icon.size.width >= 64 else { return nil }
        return icon
    }
}

/// Cards for a message's links; nothing until a page answers.
struct LinkCardList: View {
    var text: String
    var body: some View {
        let urls = LinkCards.urls(in: text)
        if !urls.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(urls, id: \.self) { LinkCard(url: $0) }
            }
        }
    }
}

struct LinkCard: View {
    var url: URL
    @Environment(\.openURL) private var openURL
    @State private var info: LinkInfo?

    var body: some View {
        // Not a Group: an empty Group drops its modifiers, and the fetch
        // below would never start. The clear line keeps the view present.
        ZStack(alignment: .leading) {
            Color.clear.frame(height: 0)
            if let info {
                Button { openURL(url) } label: {
                    HStack(spacing: 12) {
                        if let image = info.image {
                            Image(uiImage: image).resizable().scaledToFill()
                                .frame(width: 56, height: 56)
                                .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                        }
                        VStack(alignment: .leading, spacing: 3) {
                            if let title = info.title {
                                Text(title).font(.system(size: 14, weight: .medium)).foregroundStyle(Theme.fg).lineLimit(2)
                            }
                            Text(info.host).font(.system(size: 12)).foregroundStyle(Theme.fg2).lineLimit(1)
                        }
                        Spacer(minLength: 0)
                    }
                    .padding(10)
                    .frame(maxWidth: 360, alignment: .leading)
                    .background(Theme.surface, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("link-card")
                .transition(.opacity)
            }
        }
        .task(id: url) {
            let loaded = await LinkMetadataCache.shared.info(for: url)
            withAnimation(.easeOut(duration: 0.2)) { info = loaded }
        }
    }
}
