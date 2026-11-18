import FennecCore
import Foundation
import Observation

/// Sessions shown by the app. Live sessions come from the Fennec client
/// (mobile/fennecmobile via gomobile): it connects to the shared cloud
/// computer over SSH like the Mac daemon and runs fennec-terminal list, read,
/// send, and stop. `SessionStore.demo()` keeps example data for previews.
@MainActor
@Observable
final class SessionStore {
    struct PendingSend {
        enum Delivery { case sending, sent, unconfirmed }
        var message: ChatMessage
        var previousMatches: Int
        var delivery = Delivery.sending
    }
    var pendingSends: [String: PendingSend] = [:]
    var attachmentDrafts: [String: [DraftAttachment]] = [:]
    var attachmentErrors: [String: String] = [:]
    @ObservationIgnored var previewDownloads: [String: Task<URL, Error>] = [:]
    /// Demo sessions' recorded replies in progress (ReviewDemo.swift).
    @ObservationIgnored var demoReplies: [String: Task<Void, Never>] = [:]

    func matchingPrompts(_ text: String, in items: [TranscriptItem]) -> Int {
        items.filter { $0.kind == "user" && $0.text == text }.count
    }

    func reconcilePendingSend(_ id: String, items: [TranscriptItem]) {
        guard let pending = pendingSends[id], case .user(let text) = pending.message.kind,
              matchingPrompts(text, in: items) > pending.previousMatches else { return }
        // Initial/reset reads may include old identical prompts. Only a new
        // timestamped echo can confirm an optimistic message in that case.
        let sentAt = pending.message.time ?? .now
        guard items.contains(where: {
            $0.kind == "user" && $0.text == text &&
            ($0.time.map { Date(timeIntervalSince1970: Double($0) / 1000) >= sentAt.addingTimeInterval(-5) } ?? false)
        }) else { return }
        pendingSends[id] = nil
    }

    var sessions: [Session]
    var macOnline: Bool
    var cloudRunning: Bool
    var enabledAgents: Set<AgentKind>
    /// Nil in demo mode.
    let core: FennecmobileSessions?
    var loadError: String?
    var loaded = false
    /// Sessions whose log is being read now.
    @ObservationIgnored var transcriptReads: Set<String> = []
    @ObservationIgnored private var sessionListRevision = 0

    init(sessions: [Session], macOnline: Bool = true, cloudRunning: Bool = true,
         enabledAgents: Set<AgentKind> = [.claudeCode, .codex, .grokBuild, .kimiCode, .pi],
         core: FennecmobileSessions? = nil) {
        self.core = core
        self.loaded = core == nil
        self.sessions = sessions
        self.macOnline = macOnline
        self.cloudRunning = cloudRunning
        self.enabledAgents = enabledAgents
    }

    /// Groups in display order; empty groups are omitted.
    var groups: [(SessionState, [Session])] {
        SessionState.allCases.compactMap { state in
            let items = sessions
                .filter { $0.state == state }
                .sorted { ($0.pinned ? 1 : 0, $0.updatedAt) > ($1.pinned ? 1 : 0, $1.updatedAt) }
            return items.isEmpty ? nil : (state, items)
        }
    }

    func session(_ id: String) -> Session? { sessions.first { $0.id == id } }

    func search(_ text: String) -> (sessions: [Session], messages: [(Session, String)]) {
        let query = text.trimmingCharacters(in: .whitespaces)
        guard !query.isEmpty else { return ([], []) }
        let titles = sessions.filter { $0.title.localizedCaseInsensitiveContains(query) }
        var hits: [(Session, String)] = []
        for session in sessions {
            for message in session.messages {
                if case .agent(let body) = message.kind, body.localizedCaseInsensitiveContains(query) {
                    hits.append((session, body))
                }
            }
        }
        return (titles, hits)
    }

    func update(_ id: String, _ change: (inout Session) -> Void) {
        guard let index = sessions.firstIndex(where: { $0.id == id }) else { return }
        change(&sessions[index])
        sessions[index].updatedAt = .now
    }

    func send(_ text: String, to id: String) {
        if let core, let session = session(id), let address = session.address {
            guard pendingSends[id] == nil else { return }
            pendingSends[id] = PendingSend(message: .init(kind: .user(text), time: .now),
                                           previousMatches: matchingPrompts(text, in: session.transcriptItems))
            let messageID = pendingSends[id]?.message.id
            Task {
                do {
                    _ = try await Task.detached { try goCall { core.send(address, sessionID: id, text: text, error: $0) } }.value
                    if pendingSends[id]?.message.id == messageID {
                        pendingSends[id]?.delivery = .sent
                    }
                } catch {
                    // A transport failure does not prove the server rejected the message.
                    if pendingSends[id]?.message.id == messageID {
                        pendingSends[id]?.delivery = .unconfirmed
                    }
                }
                await loadTranscript(id)
            }
            return
        }
        update(id) {
            $0.messages.append(.init(kind: .user(text), time: .now))
            if $0.title == "Untitled session" { $0.title = Self.demoTitle(text) }
        }
        playDemoReply(id)
    }

    /// A demo session's title from its first message, as agents name theirs.
    static func demoTitle(_ text: String) -> String {
        let line = text.split(whereSeparator: \.isNewline).first.map(String.init) ?? text
        return line.count > 48 ? String(line.prefix(47)) + "…" : line
    }

    func approve(_ id: String, forSession: Bool) {
        update(id) {
            guard let approval = $0.approval else { return }
            $0.messages.append(.init(kind: .step(forSession ? "Approved for this session" : "Approved")))
            $0.messages.append(.init(kind: .step("Ran \(approval.command)")))
            $0.approval = nil
        }
        playDemoReply(id)
    }

    func deny(_ id: String, reason: String?) {
        update(id) {
            $0.approval = nil
            if let reason, !reason.isEmpty { $0.messages.append(.init(kind: .user(reason))) }
        }
        playDemoReply(id)
    }

    func answer(_ id: String, with option: String) {
        update(id) {
            $0.question = nil
            $0.messages.append(.init(kind: .user(option)))
        }
        playDemoReply(id)
    }

    func rename(_ id: String, to title: String) { update(id) { $0.title = title } }
    func togglePin(_ id: String) { update(id) { $0.pinned.toggle() } }
    func stop(_ id: String) {
        if let core, let address = session(id)?.address {
            Task { if await live({ try goCall { core.stop(address, sessionID: id, error: $0) } }) != nil { sessions.removeAll { $0.id == id } } }
            return
        }
        demoReplies[id]?.cancel()
        demoReplies[id] = nil
        sessions.removeAll { $0.id == id }
    }

    /// Creates an empty session. The first message is sent from the chat page.
    func launch(agent: AgentKind) async throws -> String {
        guard let core else {
            return create(agent: agent, model: "", project: "", location: .cloud, prompt: "").id
        }
        let json = try await Task.detached { try goCall { core.launch(agent.launcherID, error: $0) } }.value
        struct Launched: Decodable { var address: String; var labels: [String: String]?; var id: String? }
        guard let data = json.data(using: .utf8), let launched = try? JSONDecoder().decode(Launched.self, from: data),
              let id = launched.labels?["fennec.session"] ?? launched.id else {
            throw NSError(domain: "Fennec", code: 1, userInfo: [NSLocalizedDescriptionKey: "Couldn't start the session."])
        }
        var session = Session(id: id, title: "Untitled session", agent: agent, model: "", project: "",
                              location: .cloud, state: .idle, updatedAt: .now, messages: [])
        session.address = launched.address
        // A newly launched session has no history to wait for. Live reads still
        // run on the chat page and fill in messages as they arrive.
        session.transcriptLoaded = true
        sessionListRevision += 1
        sessions.removeAll { $0.id == id }
        sessions.append(session)
        return id
    }

    @discardableResult
    func create(agent: AgentKind, model: String, project: String, location: Location, prompt: String) -> Session {
        let session = Session(
            id: UUID().uuidString, title: "Untitled session", agent: agent, model: model,
            project: project, location: location, state: prompt.isEmpty ? .idle : .working, updatedAt: .now,
            messages: prompt.isEmpty ? [] : [.init(kind: .user(prompt))])
        sessions.append(session)
        return session
    }
}

extension SessionStore {
    static func demo() -> SessionStore {
        let now = Date.now
        func ago(_ minutes: Double) -> Date { now.addingTimeInterval(-minutes * 60) }
        return SessionStore(sessions: [
            Session(id: "s1", title: "Fix flaky reconnect test", agent: .codex, model: "GPT-5", project: "fennec",
                    location: .cloud, state: .needsInput, updatedAt: ago(2),
                    messages: [
                        .init(kind: .user("Find the flaky reconnect test and fix it with the smallest change.")),
                        .init(kind: .step("Worked for 1m 42s")),
                        .init(kind: .agent("The retry fires before the resident channel is fenced. I wrote a focused test that reproduces the race and need your approval to run it 20 times.")),
                    ],
                    approval: ApprovalRequest(command: "go test ./internal/terminalstream -run Reconnect -count=20",
                                              directory: "~/repos/runta-dev/fennec", location: .cloud)),
            Session(id: "s2", title: "Live Activity research", agent: .grokBuild, model: "Grok 4", project: "fennec",
                    location: .cloud, state: .needsInput, updatedAt: ago(15),
                    messages: [
                        .init(kind: .user("Research how to push agent state to a Lock Screen Live Activity.")),
                        .init(kind: .agent("ActivityKit has more than one update path, and the choice decides whether the backend needs APNs.")),
                    ],
                    question: AgentQuestion(prompt: "Which update channel should I use?", options: [
                        .init(id: 1, title: "APNs push from Runta", detail: "Updates while the app is closed"),
                        .init(id: 2, title: "In-app updates only", detail: "No server work, stops in background"),
                        .init(id: 3, title: "Broadcast channel", detail: "iOS 18+, one push to many"),
                    ])),
            Session(id: "s3", title: "Fennec iOS mockups", agent: .claudeCode, model: "Opus 5.5", project: "fennec",
                    location: .cloud, state: .working, updatedAt: ago(1),
                    messages: [
                        .init(kind: .user("Draft mockups for every screen of the iOS app.")),
                        .init(kind: .step("Thought for 12s")),
                        .init(kind: .agent("I read the Mac panel and DESIGN.md first and kept their state words: Working, Needs input, Done.")),
                        .init(kind: .step("Ran 4 commands")),
                        .init(kind: .agent("Next I'll draw sign-in, list, session, new session, and settings.")),
                    ]),
            Session(id: "s4", title: "Chart today's sar metrics", agent: .codex, model: "GPT-5", project: "ops",
                    location: .cloud, state: .working, updatedAt: ago(4),
                    messages: [.init(kind: .agent("Aggregating CPU and I/O samples for 08:00–19:00…"))]),
            Session(id: "s5", title: "Fix Magpie panel startup", agent: .claudeCode, model: "Opus 5.5", project: "fennec",
                    location: .cloud, state: .idle, updatedAt: ago(45),
                    messages: [
                        .init(kind: .user("The Magpie panel sometimes fails to open. Look into it.")),
                        .init(kind: .step("Worked for 6m 05s")),
                        .init(kind: .agent("The panel read a stale cached port on startup. It now waits for the gateway health check before loading. make check-ui passes.")),
                        .init(kind: .changes([
                            FileChange(path: "native/macos/UI/panel.js", added: 21, removed: 7),
                            FileChange(path: "internal/gateway/health.go", added: 14, removed: 5),
                            FileChange(path: "internal/gateway/health_test.go", added: 3, removed: 0),
                        ])),
                    ]),
            Session(id: "s6", title: "Onboarding Chinese copy", agent: .kimiCode, model: "K2", project: "fennec",
                    location: .cloud, state: .idle, updatedAt: ago(60 * 30),
                    messages: [.init(kind: .agent("Translations updated in translations.json"))]),
            Session(id: "s7", title: "errand release notes", agent: .pi, model: "Opus 5.5", project: "errand",
                    location: .mac, state: .idle, updatedAt: ago(60 * 50),
                    messages: [.init(kind: .agent("Drafted notes for v0.9.2"))]),
        ])
    }
}

// MARK: Live sessions from the cloud computer

struct SessionSummary: Decodable {
    var address: String
    var session_id: String
    var agent: String
    var title: String
    var status: String
    var activity: String?
    var status_ms: Int64?
}

/// transcript.Item from internal/transcript.
struct TranscriptItem: Codable, Hashable {
    var kind: String
    var text: String
    var tool: String?
    var time: Int64?
    var images: [String]?
    /// For "user": sent while the agent worked (Claude Code says so).
    var steer: Bool?
    /// For "end": milliseconds the agent says it worked, and why it stopped.
    var duration: Int64?
    var reason: String?
}

struct TranscriptChunk: Decodable {
    var items: [TranscriptItem]
    var offset: Int64
    var reset: Bool
    var path: String?
    /// The parser's version (transcript.Version); a cache from another is dropped.
    var version: Int?
}

/// What the app has read of one session's log, kept on disk so reopening the
/// app asks the cloud only for lines written since (offset), not the log again.
struct TranscriptCache: Codable {
    var version: Int?
    var path: String
    var offset: Int64
    var items: [TranscriptItem]

    private static var directory: URL {
        URL.cachesDirectory.appending(path: "transcripts", directoryHint: .isDirectory)
    }
    private static func file(_ id: String) -> URL { directory.appending(path: id + ".json") }

    static func load(_ id: String) -> TranscriptCache? {
        guard let data = try? Data(contentsOf: file(id)) else { return nil }
        return try? JSONDecoder().decode(TranscriptCache.self, from: data)
    }

    func save(_ id: String) {
        try? FileManager.default.createDirectory(at: Self.directory, withIntermediateDirectories: true)
        // Keep the latest 400 items; older ones come back from the log if needed.
        var trimmed = self
        if trimmed.items.count > 400 { trimmed.items.removeFirst(trimmed.items.count - 400) }
        if let data = try? JSONEncoder().encode(trimmed) { try? data.write(to: Self.file(id), options: .atomic) }
    }
}

extension SessionStore {
    static func live(auth: RuntaAuth) -> SessionStore {
        let directory = URL.applicationSupportDirectory.appending(path: "Fennec").path()
        return SessionStore(sessions: [], core: FennecmobileNewSessions(auth.core, directory))
    }

    /// Runs a blocking Fennec client call off the main actor and records its error.
    @discardableResult
    fileprivate func live(_ call: @escaping @Sendable () throws -> String) async -> String? {
        do {
            let result = try await Task.detached { try call() }.value
            loadError = nil
            return result
        } catch {
            loadError = (error as NSError).localizedDescription
            return nil
        }
    }

    func refresh() async {
        guard let core else { return }
        let revision = sessionListRevision
        guard let json = await live({ try goCall { core.list($0) } }), let data = json.data(using: .utf8),
              let rows = try? JSONDecoder().decode([SessionSummary].self, from: data) else { loaded = true; return }
        // A list requested before launch must not erase the newly opened chat.
        guard revision == sessionListRevision else { return }
        let previous = Dictionary(uniqueKeysWithValues: sessions.map { ($0.id, $0) })
        sessions = rows.compactMap { row in
            guard let agent = AgentKind(launcherID: row.agent) else { return nil }
            let state: SessionState = switch row.status {
            case "Working": .working
            case "Needs input": .needsInput
            default: .idle
            }
            var session = previous[row.session_id] ?? Session(
                id: row.session_id, title: row.title, agent: agent, model: "", project: "",
                location: .cloud, state: state, updatedAt: .now, messages: [])
            session.title = row.title
            if session.state != state { session.updatedAt = .now }
            session.state = state
            session.address = row.address
            session.activity = row.activity
            session.statusSince = row.status_ms.map { Date(timeIntervalSince1970: TimeInterval($0) / 1000) }
            return session
        }
        loaded = true
        // Keep each session's log current so the list shows its latest update.
        // The first read takes the tail of the log; later reads only new lines.
        for session in sessions where !session.transcriptLoaded || session.state != previous[session.id]?.state || session.state == .working {
            await loadTranscript(session.id)
        }
    }

    /// Reads new lines of the agent's own session log into the conversation.
    /// Only lines after the last offset cross the network; what was read is
    /// cached on disk per session. One read per session at a time.
    func loadTranscript(_ id: String) async {
        guard let core, !transcriptReads.contains(id), let index = sessions.firstIndex(where: { $0.id == id }),
              let address = sessions[index].address else { return }
        transcriptReads.insert(id)
        defer { transcriptReads.remove(id) }
        if sessions[index].transcriptOffset < 0, sessions[index].messages.isEmpty, let cache = TranscriptCache.load(id) {
            var restored = sessions[index]
            for item in cache.items { Self.append(item, to: &restored) }
            restored.transcriptOffset = cache.offset
            restored.transcriptPath = cache.path
            restored.transcriptVersion = cache.version
            restored.transcriptItems = cache.items
            restored.transcriptLoaded = true
            sessions[index] = restored
        }
        let offset = sessions[index].transcriptOffset
        let result = await Task.detached { () -> Result<String, Error> in
            Result { try goCall { core.transcript(address, sessionID: id, offset: offset, error: $0) } }
        }.value
        guard let index = sessions.firstIndex(where: { $0.id == id }) else { return }
        switch result {
        case .failure(let error):
            sessions[index].transcriptLoaded = true
            sessions[index].transcriptError = (error as NSError).localizedDescription
        case .success(let json):
            guard let data = json.data(using: .utf8), let chunk = try? JSONDecoder().decode(TranscriptChunk.self, from: data) else { return }
            var updated = sessions[index]
            // The agent moved to another log (e.g. a new conversation): start over from its tail.
            // Keep what is shown until the new read replaces it in one step
            // (a reset chunk); clearing first left the chat blank between polls.
            if let path = chunk.path, let known = updated.transcriptPath, path != known, !chunk.reset {
                sessions[index].transcriptOffset = -1
                return
            }
            // Read with another parser than the cached items: start over from the log.
            if let version = chunk.version, updated.transcriptVersion != version, !chunk.reset {
                sessions[index].transcriptOffset = -1
                return
            }
            updated.transcriptVersion = chunk.version ?? updated.transcriptVersion
            if chunk.reset { updated.messages = []; updated.transcriptItems = [] }
            for item in chunk.items { Self.append(item, to: &updated) }
            updated.transcriptItems += chunk.items
            reconcilePendingSend(id, items: updated.transcriptItems)
            updated.transcriptOffset = chunk.offset
            updated.transcriptPath = chunk.path ?? updated.transcriptPath
            updated.transcriptLoaded = true
            updated.transcriptError = nil
            if updated != sessions[index] {
                sessions[index] = updated
                if let path = updated.transcriptPath {
                    TranscriptCache(version: updated.transcriptVersion, path: path, offset: updated.transcriptOffset, items: updated.transcriptItems).save(id)
                }
            }
        }
    }

    static func append(_ item: TranscriptItem, to session: inout Session) {
        let time = item.time.map { Date(timeIntervalSince1970: TimeInterval($0) / 1000) }
        switch item.kind {
        case "user":
            if FennecmobileTranscriptEventKind(item.text) == "mac-online" {
                session.messages.append(.init(kind: .macOnline, time: time))
                return
            }
            let images = (item.images ?? []).compactMap { url -> Data? in
                guard let comma = url.firstIndex(of: ",") else { return nil }
                return Data(base64Encoded: String(url[url.index(after: comma)...]))
            }
            // Claude Code can record a message sent mid-turn twice: when it is
            // queued and beside the next tool result. Keep one.
            if let index = session.messages.indices.suffix(12).last(where: {
                if case .user(let text) = session.messages[$0].kind { return text == item.text } else { return false }
            }), !item.text.isEmpty {
                if session.messages[index].images.isEmpty { session.messages[index].images = images }
                return
            }
            session.messages.append(.init(kind: .user(item.text), time: time, images: images, steer: item.steer ?? false))
            if let time { session.turnStarted = time }
        case "agent":
            session.messages.append(.init(kind: .agent(item.text), time: time))
        case "compacted":
            session.messages.append(.init(kind: .compacted, time: time))
        case "end":
            let duration = item.duration.map { TimeInterval($0) / 1000 }
            session.messages.append(.init(kind: .end(duration: duration, stopped: item.reason == "interrupted"), time: time))
        case "tool":
            let use = ToolUse(name: item.tool ?? "", summary: item.text)
            if case .tools(let uses)? = session.messages.last?.kind {
                session.messages[session.messages.count - 1].kind = .tools(uses + [use])
                if let time { session.messages[session.messages.count - 1].time = time }
            } else {
                session.messages.append(.init(kind: .tools([use]), time: time))
            }
        default:
            break
        }
    }
}
