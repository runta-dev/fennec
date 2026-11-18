import FennecCore
import SwiftTerm
import SwiftUI

/// 10 terminal, opened from the session menu. The phone attaches to the
/// agent's Herdr terminal as a viewer of its own, at the phone's size: Herdr
/// sizes the PTY for the viewer active last (herdr-core viewers.rs), so the
/// agent redraws for the phone while this page is in front and goes back to
/// the Mac's size when the Mac's terminal is used again. Leaving detaches;
/// the agent keeps running.
struct TerminalPage: View {
    @Environment(SessionStore.self) private var store
    @Environment(\.scenePhase) private var scenePhase
    var session: Session
    var address: String
    @State private var link = TerminalLink()

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            AgentTerminalView(link: link) { cols, rows in
                guard let core = store.core else { return }
                link.connect(core: core, address: address, sessionID: session.id, cols: cols, rows: rows)
            }
            .padding(.horizontal, 4)
            if link.reconnecting {
                ReconnectingPill().environment(\.colorScheme, .dark)
            } else if let issue = link.ended {
                // Why the terminal ended (the session stopped, it was signed
                // out); a dropped link never gets here, it keeps reconnecting.
                Text(issue.kind == .other && issue.detail.isEmpty ? L10n.text("ios.terminal_closed") : issue.title)
                    .font(.system(size: 14)).foregroundStyle(.white.opacity(0.8)).multilineTextAlignment(.center)
                .padding(24)
                .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 24, style: .continuous))
                .environment(\.colorScheme, .dark)
            }
        }
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .principal) { TitlePill(session: session) } }
        .toolbarBackground(.hidden, for: .navigationBar)
        .onDisappear { link.close() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { link.resume(); link.focus() }
        }
    }
}

/// Owns this page's Herdr viewer and feeds its stream into SwiftTerm.
@MainActor
@Observable
final class TerminalLink {
    /// Why the viewer stopped, once it gave up reconnecting on its own.
    private(set) var ended: ConnectionIssue?
    /// A dropped link is retried a few times before `ended` is shown.
    private(set) var reconnecting = false
    @ObservationIgnored private var attempts = 0
    @ObservationIgnored weak var view: TerminalView?
    @ObservationIgnored private var terminal: FennecmobileTerminal?
    @ObservationIgnored private var size = (cols: 0, rows: 0)
    @ObservationIgnored private var request: (core: FennecmobileSessions, address: String, sessionID: String)?
    @ObservationIgnored private var connecting = false

    func connect(core: FennecmobileSessions, address: String, sessionID: String, cols: Int, rows: Int) {
        size = (cols, rows)
        request = (core, address, sessionID)
        if let terminal {
            try? terminal.resize(cols, rows: rows)
            return
        }
        guard !connecting else { return }
        connecting = true
        ended = nil
        let output = TerminalSink(link: self)
        Task.detached {
            var failure: String?
            let terminal: FennecmobileTerminal?
            do { terminal = try core.attach(address, sessionID: sessionID, cols: cols, rows: rows, out_: output) }
            catch { terminal = nil; failure = (error as NSError).localizedDescription }
            await MainActor.run {
                self.connecting = false
                if let terminal {
                    self.terminal = terminal
                    self.reconnecting = false
                    self.attempts = 0
                    if self.size != (cols, rows) { try? terminal.resize(self.size.cols, rows: self.size.rows) }
                } else {
                    self.stopped(failure ?? "")
                }
            }
        }
    }

    /// Back in the foreground: a link that is waiting to reconnect tries now.
    func resume() {
        guard reconnecting, terminal == nil, !connecting, let request else { return }
        connect(core: request.core, address: request.address, sessionID: request.sessionID, cols: size.cols, rows: size.rows)
    }

    func write(_ bytes: ArraySlice<UInt8>) { try? terminal?.write(Data(bytes)) }
    func focus() { try? terminal?.focus() }

    /// Leaving the page: the viewer ends and nothing reconnects it.
    func close() {
        request = nil
        reconnecting = false
        terminal?.close()
        terminal = nil
    }

    fileprivate func output(_ data: Data) { view?.feed(byteArray: ArraySlice(data)) }

    fileprivate func closed(_ message: String) {
        terminal = nil
        stopped(message)
    }

    /// A dropped link reconnects by itself for as long as the page is open,
    /// 1, 2, 4, 8, then every 10 seconds; anything else shows why it ended.
    private func stopped(_ message: String) {
        guard let request else { return }
        let issue = ConnectionIssue(message: message)
        guard issue.kind == .network else {
            reconnecting = false
            ended = issue
            return
        }
        attempts += 1
        reconnecting = true
        let delay = min(pow(2, Double(attempts - 1)), 10)
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard let self, self.terminal == nil, !self.connecting, self.reconnecting else { return }
            self.connect(core: request.core, address: request.address, sessionID: request.sessionID,
                         cols: self.size.cols, rows: self.size.rows)
        }
    }
}

/// Receives the Go stream on Go's goroutines and hops to the main actor.
private final class TerminalSink: NSObject, FennecmobileTerminalOutputProtocol, @unchecked Sendable {
    weak var link: TerminalLink?
    init(link: TerminalLink) { self.link = link }

    func output(_ data: Data?) {
        guard let data else { return }
        DispatchQueue.main.async { [weak self] in self?.link?.output(data) }
    }

    func closed(_ message: String?) {
        DispatchQueue.main.async { [weak self] in self?.link?.closed(message ?? "") }
    }
}

private struct AgentTerminalView: UIViewRepresentable {
    var link: TerminalLink
    var sized: (Int, Int) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(link: link, sized: sized) }

    func makeUIView(context: Context) -> TerminalView {
        let view = TerminalView(frame: .zero)
        view.font = UIFont.monospacedSystemFont(ofSize: 11, weight: .regular)
        view.nativeBackgroundColor = .black
        view.nativeForegroundColor = UIColor(white: 0.92, alpha: 1)
        view.terminalDelegate = context.coordinator
        link.view = view
        DispatchQueue.main.async { _ = view.becomeFirstResponder() }
        return view
    }

    func updateUIView(_ view: TerminalView, context: Context) {}

    final class Coordinator: NSObject, TerminalViewDelegate {
        let link: TerminalLink
        let sized: (Int, Int) -> Void
        init(link: TerminalLink, sized: @escaping (Int, Int) -> Void) { self.link = link; self.sized = sized }

        func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
            guard newCols >= 20, newRows >= 5 else { return }
            sized(newCols, newRows)
        }
        func send(source: TerminalView, data: ArraySlice<UInt8>) { link.write(data) }
        func setTerminalTitle(source: TerminalView, title: String) {}
        func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
        func scrolled(source: TerminalView, position: Double) {}
        func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
            if let url = URL(string: link), ["http", "https"].contains(url.scheme) { UIApplication.shared.open(url) }
        }
        func bell(source: TerminalView) {}
        func clipboardCopy(source: TerminalView, content: Data) {
            if let text = String(data: content, encoding: .utf8) { UIPasteboard.general.string = text }
        }
        func iTermContent(source: TerminalView, content: ArraySlice<UInt8>) {}
        func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
    }
}
