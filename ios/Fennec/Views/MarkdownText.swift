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
            Text(MarkdownBlock.inline(text)).lineLimit(lineLimit)
        } else {
            VStack(alignment: .leading, spacing: 12) {
                ForEach(Array(MarkdownBlock.parse(text).enumerated()), id: \.offset) { _, block in
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

    static func inline(_ text: String) -> AttributedString {
        (try? AttributedString(markdown: text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(text)
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
        let lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
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
            Text(MarkdownBlock.inline(text)).lineSpacing(3)
        case .heading(let level, let text):
            Text(MarkdownBlock.inline(text))
                .font(.system(size: level == 1 ? 20 : level == 2 ? 18 : 16, weight: .semibold))
                .padding(.top, 4)
        case .list(let items):
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(item.marker).foregroundStyle(Theme.fg2).monospacedDigit()
                            .frame(minWidth: 14, alignment: .trailing)
                        Text(MarkdownBlock.inline(item.text)).lineSpacing(3)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(.leading, CGFloat(item.depth) * 18)
                }
            }
        case .quote(let text):
            Text(MarkdownBlock.inline(text)).foregroundStyle(Theme.fg2).lineSpacing(3)
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
                        Text(MarkdownBlock.inline(header[column])).font(.system(size: 14, weight: .semibold))
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
                            Text(MarkdownBlock.inline(cell(rows[index], column))).font(.system(size: 14))
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
