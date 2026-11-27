import SwiftUI

/// Agent replies are Markdown. SwiftUI's Text renders inline Markdown only
/// (bold, code, links), so blocks are laid out here: headings, lists, quotes,
/// fenced code and tables. Tables fit the screen and wrap their cells; code
/// scrolls sideways so lines keep their shape.
struct MarkdownText: View {
    var text: String
    var lineLimit: Int? = nil

    var body: some View {
        if let lineLimit {
            // A folded note: plain inline Markdown, a few lines.
            MarkdownBlock.inlineText(text).lineLimit(lineLimit)
        } else {
            VStack(alignment: .leading, spacing: 12) {
                ForEach(Array(MarkdownBlock.blocks(text).enumerated()), id: \.offset) { _, block in
                    BlockView(block: block)
                }
            }
        }
    }
}

enum MarkdownBlock: Equatable {
    case paragraph(String)
    case heading(Int, String)
    case list([ListItem])
    case quote(String)
    case code(String)
    case table(header: [String], alignments: [HorizontalAlignment], rows: [[String]])
    case rule

    struct ListItem: Equatable {
        var marker: String // "•" or "1."
        var depth: Int
        var text: String
    }

    static func == (a: MarkdownBlock, b: MarkdownBlock) -> Bool { String(describing: a) == String(describing: b) }

    /// Parsing runs on every redraw (each poll redraws the conversation), so
    /// results are kept per text; replies don't change once written.
    @MainActor private static var inlineCache: [String: AttributedString] = [:]
    @MainActor private static var blockCache: [String: [MarkdownBlock]] = [:]

    @MainActor static func inline(_ text: String) -> AttributedString {
        if let cached = inlineCache[text] { return cached }
        let value = parseInline(text)
        if inlineCache.count > 4000 { inlineCache.removeAll() }
        inlineCache[text] = value
        return value
    }

    /// Inline Markdown as Text, with `**bold**` drawn at semibold like the rest of
    /// Fennec (THI-187); SwiftUI would draw the strong intent at full bold.
    @MainActor static func inlineText(_ text: String) -> Text {
        let value = inline(text)
        var out = Text(verbatim: "")
        for run in value.runs {
            var piece = AttributedString(value[run.range])
            guard var intent = run.inlinePresentationIntent, intent.contains(.stronglyEmphasized) else {
                out = Text("\(out)\(Text(piece))")
                continue
            }
            intent.remove(.stronglyEmphasized)
            piece.inlinePresentationIntent = intent.isEmpty ? nil : intent
            out = Text("\(out)\(Text(piece).fontWeight(.semibold))")
        }
        return out
    }

    /// Inline Markdown without the cache, for callers off the main actor.
    static func parseInline(_ text: String) -> AttributedString {
        let clean = stripAgentMarkers(text)
        let (masked, restore) = maskPunctuationAtDelimiters(clean)
        guard let value = try? AttributedString(markdown: masked, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)) else {
            return AttributedString(clean)
        }
        return unmask(value, restore)
    }

    /// Codex marks web citations and named entities with private-use
    /// characters: U+E200 opens one, U+E202 separates its fields and U+E201
    /// closes it, e.g. "\u{E200}cite\u{E202}turn5search2\u{E201}". Its own
    /// apps hide them; iOS fonts have no glyph, so replies showed boxes around
    /// "cite turn…search2" (FEN-85). Citations are dropped, with the spaces
    /// before them; an entity keeps its display name.
    static func stripAgentMarkers(_ text: String) -> String {
        let marker: ClosedRange<UInt32> = 0xE200...0xE2FF
        guard text.unicodeScalars.contains(where: { marker.contains($0.value) }) else { return text }
        let scalars = Array(text.unicodeScalars)
        var out = String.UnicodeScalarView()
        var i = 0
        while i < scalars.count {
            let scalar = scalars[i]
            if scalar.value == 0xE200, let end = scalars[(i + 1)...].firstIndex(where: { $0.value == 0xE201 }) {
                let fields = String(String.UnicodeScalarView(scalars[(i + 1)..<end])).components(separatedBy: "\u{E202}")
                if fields.first == "entity", fields.count > 1, let name = entityName(fields[1]) {
                    out.append(contentsOf: name.unicodeScalars)
                } else {
                    while let last = out.last, last == " " || last == "\t" { out.removeLast() }
                }
                i = end + 1
                continue
            }
            if !marker.contains(scalar.value) { out.append(scalar) }
            i += 1
        }
        return String(out)
    }

    /// `["company","OpenAI",…]` → "OpenAI".
    private static func entityName(_ field: String) -> String? {
        guard let array = try? JSONSerialization.jsonObject(with: Data(field.utf8)) as? [Any], array.count > 1 else { return nil }
        return array[1] as? String
    }

    /// CommonMark won't close `**` that follows punctuation when a letter comes
    /// next, so `**转发。**它们` and `**现在怎么办：**追问` showed raw asterisks
    /// (FEN-85; the desktop has the same fix, FEN-43). Punctuation like `。：（`
    /// touching a `*`/`~` run, with a letter on the run's other side, is swapped
    /// for a private-use character (U+E000+n) before parsing and put back after.
    /// Skipped when the text already holds such characters.
    static func maskPunctuationAtDelimiters(_ text: String) -> (String, [Character: Character]) {
        let chars = Array(text)
        guard chars.contains(where: isDelimiter),
              !text.unicodeScalars.contains(where: { (0xE000...0xE0FF).contains($0.value) }) else { return (text, [:]) }
        var out = chars
        var masks: [Character: Character] = [:] // original -> mask
        func mask(_ index: Int) {
            let original = chars[index]
            if masks[original] == nil {
                guard masks.count < 256, let scalar = Unicode.Scalar(0xE000 + UInt32(masks.count)) else { return }
                masks[original] = Character(scalar)
            }
            out[index] = masks[original]!
        }
        var i = 0
        while i < chars.count {
            guard isDelimiter(chars[i]) else { i += 1; continue }
            var j = i
            while j < chars.count, chars[j] == chars[i] { j += 1 }
            let before: Character? = i > 0 ? chars[i - 1] : nil
            let after: Character? = j < chars.count ? chars[j] : nil
            if let before, isWidePunctuation(before), let after, isLetterLike(after) { mask(i - 1) }
            if let after, isWidePunctuation(after), let before, isLetterLike(before) { mask(j) }
            i = j
        }
        guard !masks.isEmpty else { return (text, [:]) }
        return (String(out), Dictionary(uniqueKeysWithValues: masks.map { ($0.value, $0.key) }))
    }

    private static func isDelimiter(_ c: Character) -> Bool { c == "*" || c == "~" }
    private static func isLetterLike(_ c: Character) -> Bool { !c.isWhitespace && !c.isPunctuation && !isDelimiter(c) }
    /// Non-ASCII punctuation: CJK and full-width marks, curly quotes, dashes, ….
    private static func isWidePunctuation(_ c: Character) -> Bool {
        guard c.unicodeScalars.count == 1, let scalar = c.unicodeScalars.first, scalar.value > 0x7F else { return false }
        return c.isPunctuation
    }

    private static func unmask(_ value: AttributedString, _ restore: [Character: Character]) -> AttributedString {
        guard !restore.isEmpty else { return value }
        var out = AttributedString()
        for run in value.runs {
            let text = String(value[run.range].characters.map { restore[$0] ?? $0 })
            out.append(AttributedString(text, attributes: run.attributes))
        }
        return out
    }

    @MainActor static func blocks(_ text: String) -> [MarkdownBlock] {
        if let cached = blockCache[text] { return cached }
        let value = parse(text)
        if blockCache.count > 1000 { blockCache.removeAll() }
        blockCache[text] = value
        return value
    }

    private static let listItem = /^(\s*)([-*+]|\d{1,3}[.)])\s+(.*)$/
    private static let heading = /^(#{1,6})\s+(.*?)\s*#*$/

    static func cells(_ line: String) -> [String] {
        var row = line.trimmingCharacters(in: .whitespaces)
        if row.hasPrefix("|") { row.removeFirst() }
        if row.hasSuffix("|") && !row.hasSuffix("\\|") { row.removeLast() }
        return row.split(separator: "|", omittingEmptySubsequences: false).map { $0.trimmingCharacters(in: .whitespaces) }
    }

    private static func isDivider(_ line: String) -> Bool {
        let cells = cells(line)
        return line.contains("-") && !cells.isEmpty && cells.allSatisfy { $0.range(of: #"^:?-{1,}:?$"#, options: .regularExpression) != nil }
    }

    static func parse(_ text: String) -> [MarkdownBlock] {
        let lines = stripAgentMarkers(text).replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        var blocks: [MarkdownBlock] = []
        var paragraph: [String] = []
        var items: [ListItem] = []
        var i = 0

        func flush() {
            if !paragraph.isEmpty { blocks.append(.paragraph(paragraph.joined(separator: "\n"))); paragraph = [] }
            if !items.isEmpty { blocks.append(.list(items)); items = [] }
        }

        while i < lines.count {
            let line = lines[i]
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("```") || trimmed.hasPrefix("~~~") {
                flush()
                let fence = String(trimmed.prefix(3))
                var code: [String] = []
                i += 1
                while i < lines.count, !lines[i].trimmingCharacters(in: .whitespaces).hasPrefix(fence) {
                    code.append(lines[i]); i += 1
                }
                blocks.append(.code(code.joined(separator: "\n")))
                i += 1
                continue
            }
            if trimmed.hasPrefix("|") || (trimmed.contains("|") && i + 1 < lines.count && isDivider(lines[i + 1])),
               i + 1 < lines.count, isDivider(lines[i + 1]) {
                flush()
                let header = cells(line)
                let alignments: [HorizontalAlignment] = cells(lines[i + 1]).map {
                    $0.hasPrefix(":") && $0.hasSuffix(":") ? .center : $0.hasSuffix(":") ? .trailing : .leading
                }
                var rows: [[String]] = []
                i += 2
                while i < lines.count, lines[i].contains("|"), !lines[i].trimmingCharacters(in: .whitespaces).isEmpty {
                    rows.append(cells(lines[i])); i += 1
                }
                blocks.append(.table(header: header, alignments: alignments, rows: rows))
                continue
            }
            if trimmed.isEmpty { flush(); i += 1; continue }
            if trimmed.range(of: #"^([-*_])\s*(\1\s*){2,}$"#, options: .regularExpression) != nil {
                flush(); blocks.append(.rule); i += 1; continue
            }
            if let match = trimmed.wholeMatch(of: heading) {
                flush(); blocks.append(.heading(match.1.count, String(match.2))); i += 1; continue
            }
            if trimmed.hasPrefix(">") {
                flush()
                var quote: [String] = []
                while i < lines.count, lines[i].trimmingCharacters(in: .whitespaces).hasPrefix(">") {
                    quote.append(String(lines[i].trimmingCharacters(in: .whitespaces).dropFirst()).trimmingCharacters(in: .whitespaces))
                    i += 1
                }
                blocks.append(.quote(quote.joined(separator: "\n")))
                continue
            }
            if let match = line.wholeMatch(of: listItem) {
                if !paragraph.isEmpty { blocks.append(.paragraph(paragraph.joined(separator: "\n"))); paragraph = [] }
                let marker = String(match.2)
                let ordered = marker.first?.isNumber == true
                items.append(ListItem(marker: ordered ? marker.replacingOccurrences(of: ")", with: ".") : "•",
                                      depth: min(match.1.count / 2, 3), text: String(match.3)))
                i += 1
                continue
            }
            if !items.isEmpty, line.hasPrefix("  ") {
                // A wrapped list item continues on an indented line.
                items[items.count - 1].text += " " + trimmed
                i += 1
                continue
            }
            if !items.isEmpty { blocks.append(.list(items)); items = [] }
            paragraph.append(line)
            i += 1
        }
        flush()
        return blocks
    }
}

private struct BlockView: View {
    var block: MarkdownBlock

    var body: some View {
        switch block {
        case .paragraph(let text):
            MarkdownBlock.inlineText(text).lineSpacing(3)
        case .heading(let level, let text):
            MarkdownBlock.inlineText(text)
                .font(.system(size: level == 1 ? 20 : level == 2 ? 18 : 16, weight: .semibold))
                .padding(.top, 4)
        case .list(let items):
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(item.marker).foregroundStyle(Theme.fg2).monospacedDigit()
                            .frame(minWidth: 14, alignment: .trailing)
                        MarkdownBlock.inlineText(item.text).lineSpacing(3)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(.leading, CGFloat(item.depth) * 18)
                }
            }
        case .quote(let text):
            MarkdownBlock.inlineText(text).foregroundStyle(Theme.fg2).lineSpacing(3)
                .padding(.leading, 12)
                .overlay(alignment: .leading) { Rectangle().fill(Theme.line).frame(width: 2) }
        case .code(let code):
            ScrollView(.horizontal, showsIndicators: false) {
                Text(code).font(.system(size: 13, design: .monospaced)).fixedSize().padding(12)
            }
            .background(Theme.surface2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        case .table(let header, let alignments, let rows):
            // A table that fits keeps the screen's width; a wider one scrolls
            // inside its own block instead of widening the conversation.
            ViewThatFits(in: .horizontal) {
                TableBlock(header: header, alignments: alignments, rows: rows)
                ScrollView(.horizontal, showsIndicators: false) {
                    TableBlock(header: header, alignments: alignments, rows: rows).fixedSize(horizontal: true, vertical: false)
                }
            }
        case .rule:
            Rectangle().fill(Theme.line).frame(height: 1)
        }
    }
}

private struct TableBlock: View {
    var header: [String]
    var alignments: [HorizontalAlignment]
    var rows: [[String]]

    private func cell(_ row: [String], _ column: Int) -> String { column < row.count ? row[column] : "" }
    private func alignment(_ column: Int) -> Alignment {
        switch column < alignments.count ? alignments[column] : .leading {
        case .trailing: .trailing
        case .center: .center
        default: .leading
        }
    }

    var body: some View {
        Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
                GridRow {
                    ForEach(header.indices, id: \.self) { column in
                        MarkdownBlock.inlineText(header[column]).font(.system(size: 14, weight: .semibold))
                            .padding(.horizontal, 10).padding(.vertical, 8)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: column == header.count - 1 ? .infinity : nil, alignment: alignment(column))
                            .gridColumnAlignment(column < alignments.count ? alignments[column] : .leading)
                    }
                }
                ForEach(rows.indices, id: \.self) { index in
                    Divider().gridCellUnsizedAxes(.horizontal)
                    GridRow {
                        ForEach(header.indices, id: \.self) { column in
                            MarkdownBlock.inlineText(cell(rows[index], column)).font(.system(size: 14))
                                .padding(.horizontal, 10).padding(.vertical, 8)
                                .fixedSize(horizontal: false, vertical: true)
                                // Columns take their content's width; the last one takes the rest.
                                .frame(maxWidth: column == header.count - 1 ? .infinity : nil, alignment: alignment(column))
                        }
                    }
                }
            }
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(Theme.line))
    }
}
