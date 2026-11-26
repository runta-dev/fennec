import SwiftTerm
import SwiftUI
import UIKit

// The rest of the App Review demo (ReviewDemo.swift): the terminal, file and
// web page previews. Everything here is made on the phone; nothing in the
// demo reaches the network.

// MARK: Terminal

/// What a demo session's terminal shows: the session drawn the way the
/// agent's own terminal draws it, replayed line by line when the page opens.
enum DemoTerminal {
    /// Output and the milliseconds after opening at which it appears.
    static func recording(for session: Session) -> [(at: Int, text: String)] {
        let dim = "\u{1B}[2m", bold = "\u{1B}[1m", reset = "\u{1B}[0m"
        let orange = "\u{1B}[38;5;208m", green = "\u{1B}[32m", red = "\u{1B}[31m", yellow = "\u{1B}[33m"
        func lines(_ text: String) -> String { text.replacingOccurrences(of: "\n", with: "\r\n") }
        func plain(_ text: String) -> String { lines(text.replacingOccurrences(of: "**", with: "").replacingOccurrences(of: "`", with: "")) }

        var chunks: [String] = []
        let place = session.project.isEmpty ? "~" : "~/repos/\(session.project)"
        let model = session.model.isEmpty ? "" : " · \(session.model)"
        chunks.append("\u{1B}[2J\u{1B}[H\(orange)✻\(reset) \(bold)\(session.agent.displayName)\(reset)\(model)\r\n\(dim)  \(place)\(reset)\r\n\r\n")
        // A long history replays its end, as a terminal scrolled to the bottom shows.
        for message in session.messages.suffix(40) {
            switch message.kind {
            case .user(let text):
                chunks.append("\(dim)>\(reset) \(bold)\(lines(text))\(reset)\r\n\r\n")
            case .agent(let text):
                chunks.append("\(orange)●\(reset) \(plain(text))\r\n\r\n")
            case .tools(let uses):
                for use in uses {
                    chunks.append("\(green)●\(reset) \(bold)\(use.verb)\(reset)(\(lines(use.summary)))\r\n\(dim)  ⎿  done\(reset)\r\n")
                }
                chunks.append("\r\n")
            case .step(let text):
                chunks.append("\(dim)  \(text)\(reset)\r\n\r\n")
            case .changes(let files):
                for file in files {
                    chunks.append("  \(green)+\(file.added)\(reset) \(red)-\(file.removed)\(reset) \(file.path)\r\n")
                }
                chunks.append("\r\n")
            default:
                break
            }
        }
        if let approval = session.approval {
            chunks.append("\(yellow)?\(reset) \(bold)Run this command?\(reset)\r\n  \(approval.command)\r\n\(dim)  1. Yes   2. Yes, for this session   3. No\(reset)\r\n")
        } else if let question = session.question {
            chunks.append("\(yellow)?\(reset) \(bold)\(question.prompt)\(reset)\r\n")
            for option in question.options { chunks.append("\(dim)  \(option.id). \(reset)\(option.title)\r\n") }
        } else if session.state == .working {
            chunks.append("\(orange)✻\(reset) \(session.activity ?? "Working…") \(dim)(esc to interrupt)\(reset)\r\n")
        } else {
            chunks.append("\(dim)\(String(repeating: "─", count: 36))\(reset)\r\n> ")
        }
        return chunks.enumerated().map { (at: 300 + $0.offset * 90, text: $0.element) }
    }
}

/// 10 terminal in the demo: the session's recorded terminal, read only.
struct DemoTerminalPage: View {
    var session: Session

    var body: some View {
        ZStack(alignment: .bottom) {
            Color.black.ignoresSafeArea()
            DemoTerminalView(recording: DemoTerminal.recording(for: session))
                .padding(.horizontal, 4)
            Label { Text(L10n.text("ios.demo_terminal")) } icon: { Image(systemName: "eye").regularIconWeight() }
                .font(.system(size: 13, weight: .medium)).foregroundStyle(.white.opacity(0.85))
                .padding(.horizontal, 14).frame(minHeight: 34)
                .glassEffect(.regular, in: Capsule())
                .environment(\.colorScheme, .dark)
                .padding(.bottom, 12)
                .accessibilityIdentifier("demo-terminal")
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .principal) { TitlePill(session: session) } }
        .toolbarBackground(.hidden, for: .navigationBar)
    }
}

/// A terminal that never takes the keyboard: the demo's is read only.
private final class ReadOnlyTerminalView: TerminalView {
    override var canBecomeFirstResponder: Bool { false }
}

private struct DemoTerminalView: UIViewRepresentable {
    var recording: [(at: Int, text: String)]

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> TerminalView {
        let view = ReadOnlyTerminalView(frame: .zero)
        view.font = UIFont.monospacedSystemFont(ofSize: 11, weight: .regular)
        view.nativeBackgroundColor = .black
        view.nativeForegroundColor = UIColor(white: 0.92, alpha: 1)
        let recording = recording
        context.coordinator.replay = Task { @MainActor [weak view] in
            let start = ContinuousClock.now
            for entry in recording {
                do { try await Task.sleep(until: start + .milliseconds(entry.at), clock: .continuous) } catch { return }
                view?.feed(byteArray: ArraySlice(Array(entry.text.utf8)))
            }
        }
        return view
    }

    func updateUIView(_ view: TerminalView, context: Context) {}

    static func dismantleUIView(_ view: TerminalView, coordinator: Coordinator) { coordinator.replay?.cancel() }

    final class Coordinator { var replay: Task<Void, Never>? }
}

// MARK: File and web page previews

/// The demo's workspace files: a web page report and a chart image, the
/// kinds of file an agent shares as a preview card.
enum DemoPreviews {
    static let pageRef = "pv_" + String(repeating: "a", count: 31) + "1"
    static let chartRef = "pv_" + String(repeating: "a", count: 31) + "2"

    static func metadata(_ ref: String) throws -> WorkspaceFile {
        switch ref {
        case pageRef: WorkspaceFile(ref: ref, name: "ssh-latency.html", title: "SSH latency by office", kind: "web", mime: "text/html")
        case chartRef: WorkspaceFile(ref: ref, name: "p95-latency.png", title: "p95 latency by office", kind: "image", mime: "image/png")
        default: throw AttachmentError.unavailable
        }
    }

    /// Writes the file into the app's caches and returns where it is.
    static func file(_ ref: String) throws -> URL {
        let file = try metadata(ref)
        let directory = URL.cachesDirectory.appending(path: "demo-previews", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let url = directory.appending(path: file.name)
        if ref == pageRef {
            try Data(page.utf8).write(to: url)
        } else {
            guard let png = chart().pngData() else { throw AttachmentError.unavailable }
            try png.write(to: url)
        }
        return url
    }

    /// Median and p95 round trips in ms (the same numbers as the sample
    /// "Compare SSH latency by region" session).
    private static let offices: [(name: String, median: Double, p95: Double)] = [
        ("Singapore", 182, 241), ("San Francisco", 21, 34), ("Berlin", 158, 203),
    ]

    private static var page: String {
        let bars = offices.enumerated().map { index, office in
            let y = 20 + index * 46
            return """
            <text x="0" y="\(y + 15)">\(office.name)</text>
            <rect x="120" y="\(y)" width="\(Int(office.p95))" height="20" rx="4" fill="#f38d25" opacity="0.35"/>
            <rect x="120" y="\(y)" width="\(Int(office.median))" height="20" rx="4" fill="#f38d25"/>
            <text x="\(128 + Int(office.p95))" y="\(y + 15)" class="v">\(Int(office.median)) / \(Int(office.p95)) ms</text>
            """
        }.joined(separator: "\n")
        let rows = offices.map { "<tr><td>\($0.name)</td><td>\(Int($0.median)) ms</td><td>\(Int($0.p95)) ms</td></tr>" }.joined()
        return """
        <!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
        <title>SSH latency by office</title>
        <style>
        body{font:15px -apple-system,system-ui,sans-serif;margin:24px;color:#1d1d1f;background:#fff}
        h1{font-size:22px;margin:0 0 4px}p{color:#6e6e73;margin:0 0 20px}
        svg text{font-size:13px;fill:#1d1d1f}svg text.v{fill:#6e6e73}
        table{border-collapse:collapse;width:100%;margin-top:20px}td,th{text-align:left;padding:8px 4px;border-bottom:1px solid #e5e5ea}
        @media (prefers-color-scheme:dark){body{background:#1c1c1e;color:#f5f5f7}svg text{fill:#f5f5f7}td,th{border-color:#3a3a3c}}
        </style></head><body>
        <h1>SSH latency by office</h1>
        <p>200 round trips from each office to the cloud computer, this week.</p>
        <svg viewBox="0 0 420 160" width="100%" role="img" aria-label="Median and p95 latency by office">
        \(bars)
        </svg>
        <table><tr><th>Office</th><th>Median</th><th>p95</th></tr>\(rows)</table>
        </body></html>
        """
    }

    private static func chart() -> UIImage {
        let size = CGSize(width: 720, height: 420)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: size, format: format).image { context in
            UIColor.white.setFill()
            context.fill(CGRect(origin: .zero, size: size))
            let title: [NSAttributedString.Key: Any] = [.font: UIFont.systemFont(ofSize: 28, weight: .semibold), .foregroundColor: UIColor.black]
            let label: [NSAttributedString.Key: Any] = [.font: UIFont.systemFont(ofSize: 22), .foregroundColor: UIColor.darkGray]
            ("p95 SSH latency (ms)" as NSString).draw(at: CGPoint(x: 40, y: 28), withAttributes: title)
            let orange = UIColor(red: 0.95, green: 0.55, blue: 0.15, alpha: 1)
            let maximum = offices.map(\.p95).max() ?? 1
            for (index, office) in offices.enumerated() {
                let width: CGFloat = 140, gap: CGFloat = 70, bottom: CGFloat = 360
                let x = 60 + CGFloat(index) * (width + gap)
                let height = CGFloat(office.p95 / maximum) * 250
                orange.setFill()
                UIBezierPath(roundedRect: CGRect(x: x, y: bottom - height, width: width, height: height), cornerRadius: 8).fill()
                ("\(Int(office.p95))" as NSString).draw(at: CGPoint(x: x + 46, y: bottom - height - 32), withAttributes: label)
                (office.name as NSString).draw(at: CGPoint(x: x, y: bottom + 12), withAttributes: label)
            }
        }
    }

    /// A finished session whose reply shares both files as cards.
    static func session(updatedAt: Date) -> Session {
        Session(id: "s10", title: "SSH latency report", agent: .claudeCode, model: "Opus 5.5", project: "ops",
                location: .cloud, state: .idle, updatedAt: updatedAt,
                messages: [
                    .init(kind: .user("Turn this week's SSH latency numbers into a one-page report with a chart.")),
                    .init(kind: .tools([ToolUse(name: "read", summary: "benchmarks/ssh-latency.json"),
                                        ToolUse(name: "ran", summary: "python3 report.py --out ssh-latency.html")])),
                    .init(kind: .step("Worked for 1m 12s")),
                    .init(kind: .agent("""
                    The report is ready. Tap a card to open it.

                    [SSH latency by office](http://localhost:47823/p/review-demo/\(pageRef))

                    ![p95 latency by office](fennec-preview:\(chartRef))

                    San Francisco is fastest at 21 ms median. Singapore and Berlin are both over 150 ms.
                    """)),
                ])
    }
}
