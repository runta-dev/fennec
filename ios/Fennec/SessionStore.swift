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
        /// Where the session log ended when the message was sent: a matching
        /// prompt on a line at or after it was written after the send. This
        /// needs no clock, unlike comparing the log's timestamps with this
        /// phone's (a cloud computer's clock can run behind it).
        var logPath: String?
        var logEnd: Int64 = -1
        /// The log had not been created yet (Codex writes it on the first
        /// prompt), so any matching prompt in it is this one.
        var logMissing = false

        init(message: ChatMessage, previousMatches: Int) {
            self.message = message
            self.previousMatches = previousMatches
        }

        init(message: ChatMessage, session: Session) {
            self.message = message
            previousMatches = 0
            rebase(on: session)
        }

        /// Records what the session log holds now, before the message is sent.
        mutating func rebase(on session: Session) {
            if case .user(let text) = message.kind {
                previousMatches = SessionStore.matchingPrompts(text, in: session.transcriptItems)
            }
            logPath = session.transcriptPath
            logEnd = session.transcriptPath == nil ? -1 : session.transcriptOffset
            logMissing = session.transcriptLoaded && session.transcriptPath == nil && session.transcriptItems.isEmpty
        }
    }
    var pendingSends: [String: PendingSend] = [:]
    /// Sessions whose message was delivered but whose agent hasn't been seen
    /// working yet: they show "Working" at once, without a time.
    var awaitingWork: [String: Date] = [:]

    /// Whether the session should show that its agent is working on a message.
    func workingOrStarting(_ session: Session) -> Bool {
        session.state == .working || awaitingWork[session.id].map { Date.now.timeIntervalSince($0) < 90 } ?? false
    }
    var attachmentDrafts: [String: [DraftAttachment]] = [:]
    var attachmentErrors: [String: String] = [:]
    @ObservationIgnored var previewDownloads: [String: Task<URL, Error>] = [:]
    /// Demo sessions' recorded replies in progress (ReviewDemo.swift).
    @ObservationIgnored var demoReplies: [String: Task<Void, Never>] = [:]

    /// A prompt as the session log parser shows it (transcript.userItem):
    /// spaces within each line collapsed, runs of blank lines kept to one,
    /// ends trimmed. Comparing raw text left "a  b" pending forever.
    nonisolated static func promptKey(_ text: String) -> String {
        var lines: [String] = []
        for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let joined = line.split(whereSeparator: \.isWhitespace).joined(separator: " ")
            if joined.isEmpty, lines.last?.isEmpty == true { continue }
            lines.append(joined)
        }
        return lines.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    nonisolated static func matchingPrompts(_ text: String, in items: [TranscriptItem]) -> Int {
        let key = promptKey(text)
        return items.filter { $0.kind == "user" && promptKey($0.text) == key }.count
    }

    func matchingPrompts(_ text: String, in items: [TranscriptItem]) -> Int {
        Self.matchingPrompts(text, in: items)
    }

    /// Drops the optimistic copy of a sent message once the session log shows it.
    /// `path` is the log the items came from.
    func reconcilePendingSend(_ id: String, items: [TranscriptItem], path: String? = nil) {
        guard let pending = pendingSends[id], case .user(let text) = pending.message.kind,
              matchingPrompts(text, in: items) > pending.previousMatches else { return }
        let key = Self.promptKey(text)
        let echoes = items.filter { $0.kind == "user" && Self.promptKey($0.text) == key }
        // Reset reads may include old identical prompts, so a higher count alone
        // proves nothing. The echo is new when it sits past where the log ended
        // at the send, or when there was no log yet.
        if let path, path == pending.logPath, pending.logEnd >= 0,
           echoes.contains(where: { ($0.id ?? -1) >= pending.logEnd * 16 }) {
            pendingSends[id] = nil
            return
        }
        if pending.logMissing, path != nil {
            pendingSends[id] = nil
            return
        }
        // Otherwise (another log, or none read yet) fall back to its timestamp.
        let sentAt = pending.message.time ?? .now
        guard echoes.contains(where: {
            $0.time.map { Date(timeIntervalSince1970: Double($0) / 1000) >= sentAt.addingTimeInterval(-5) } ?? false
        }) else { return }
        pendingSends[id] = nil
    }

    var sessions: [Session]
    var macOnline: Bool
    var cloudRunning: Bool
    var enabledAgents: Set<AgentKind>
    /// Nil in demo mode.
    let core: FennecmobileSessions?
    /// Why the last call to the cloud computer failed; nil once one succeeds.
    var issue: ConnectionIssue?
    /// The last list read from the cloud computer, shown at once at the next
    /// launch while a fresh one is read (nil in demo mode).
    @ObservationIgnored var listCache: URL?
    /// When this store started, and how long its first fresh list took
    /// (Integration diagnostics on the Account page).
    @ObservationIgnored var startedAt = Date.now
    var firstListSeconds: Double?
    var lastListTiming: ListTiming?
    /// What each waiting live session asks (LivePromptSheet.swift).
    var livePrompts: [String: LivePrompt] = [:]
    var answeringPrompts: Set<String> = []
    var loaded = false
    /// Sessions whose log is being read now.
    @ObservationIgnored var transcriptReads: Set<String> = []
    /// Sessions that changed (or work) since their log was last read. A list
    /// refreshed from a chat leaves them for the home list to read.
    @ObservationIgnored var transcriptsBehind: Set<String> = []
    @ObservationIgnored private var sessionListRevision = 0

    init(sessions: [Session], macOnline: Bool = true, cloudRunning: Bool = true,
         enabledAgents: Set<AgentKind> = [.claudeCode, .codex, .grokBuild, .kimiCode, .pi, .antigravity],
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
                // Newest-created first, so a new session opens at the top of its group.
                .sorted {
                    ($0.pinned ? 1 : 0, $0.createdAt ?? $0.updatedAt, $0.updatedAt)
                        > ($1.pinned ? 1 : 0, $1.createdAt ?? $1.updatedAt, $1.updatedAt)
                }
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
            pendingSends[id] = PendingSend(message: .init(kind: .user(text), time: .now), session: session)
            let messageID = pendingSends[id]?.message.id
            Task {
                do {
                    _ = try await Task.detached { try goCall { core.send(address, sessionID: id, text: text, error: $0) } }.value
                    if pendingSends[id]?.message.id == messageID {
                        pendingSends[id]?.delivery = .sent
                        awaitingWork[id] = .now
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
                              location: .cloud, state: .idle, updatedAt: .now, createdAt: .now, messages: [])
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
            project: project, location: location, state: prompt.isEmpty ? .idle : .working, updatedAt: .now, createdAt: .now,
            messages: prompt.isEmpty ? [] : [.init(kind: .user(prompt))])
        sessions.append(session)
        return session
    }
}

extension SessionStore {
    static func demo() -> SessionStore {
        let now = Date.now
        func ago(_ minutes: Double) -> Date { now.addingTimeInterval(-minutes * 60) }
        let store = SessionStore(sessions: [
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
        store.sessions.append(DemoPreviews.session(updatedAt: ago(90)))
        store.sessions.append(wideSession(updatedAt: ago(60 * 70)))
        store.sessions.append(longSession(updatedAt: ago(60 * 80)))
        return store
    }

    /// Wide content drawn the way a live session is (turns from a log), so
    /// tests cover the real conversation view: a many-column table, long
    /// commands, a long path, code and URLs must never widen the page.
    /// A long history (120 turns of Markdown replies) drawn through the live
    /// conversation view, for opening speed and scrolling back to the start.
    static func longSession(updatedAt: Date) -> Session {
        var session = Session(id: "s9", title: "Long conversation", agent: .claudeCode, model: "Opus 5.5", project: "fennec",
                              location: .cloud, state: .idle, updatedAt: updatedAt, messages: [])
        session.address = "tdemo9"
        session.transcriptLoaded = true
        var time = Int64(updatedAt.timeIntervalSince1970 * 1000) - 120 * 60_000
        var items: [TranscriptItem] = []
        for n in 1...120 {
            items.append(.init(kind: "user", text: "Question \(n): what changed in step \(n)?", time: time))
            items.append(.init(kind: "tool", text: "rg -n step\(n) internal", tool: "ran", time: time + 2000))
            items.append(.init(kind: "agent", text: """
            Answer \(n). Step **\(n)** changed `internal/step\(n).go`:

            - The retry now waits *\(n) ms* before the second attempt.
            - Tests cover the [edge case](https://example.com/\(n)).

            | File | Added | Removed |
            | --- | ---: | ---: |
            | step\(n).go | \(n) | 1 |
            """, time: time + 9000))
            items.append(.init(kind: "end", text: "", time: time + 9500, duration: 9500))
            time += 60_000
        }
        for item in items { append(item, to: &session) }
        session.transcriptItems = items
        return session
    }

    static func wideSession(updatedAt: Date) -> Session {
        var session = Session(id: "s8", title: "Compare SSH latency by region", agent: .codex, model: "GPT-5", project: "ops",
                              location: .cloud, state: .idle, updatedAt: updatedAt, messages: [])
        session.address = "tdemo8"
        session.transcriptLoaded = true
        let base = Int64(updatedAt.timeIntervalSince1970 * 1000) - 120_000
        let items: [TranscriptItem] = [
            .init(kind: "user", text: "Compare round-trip latency to the cloud computer from each office, using /Users/alex/Library/Application Support/Fennec/benchmarks/ssh-latency-2026-11-18/offices-singapore-sanfrancisco-berlin.json", time: base),
            .init(kind: "tool", text: "ssh -o ServerAliveInterval=15 -o ServerAliveCountMax=4 runtime@ssh.runta.com 'for i in $(seq 1 200); do date +%s%N; done' | ./rtt-histogram --buckets 10,20,50,100,200,500 --output /tmp/rtt-histogram-singapore-sanfrancisco-berlin.json", tool: "ran", time: base + 4000),
            .init(kind: "tool", text: "/home/runtime/repos/runta-dev/fennec/docs/benchmarks/2026-11-16-budget128-96-deployment-singapore-singtel.md", tool: "read", time: base + 6000),
            .init(kind: "agent", text: """
            Measured 200 round trips from each office over the resident SSH channel:

            | Office | Network | Median RTT | p95 RTT | Reconnects | Notes |
            | --- | --- | ---: | ---: | ---: | --- |
            | Singapore | Singtel fibre | 182 ms | 241 ms | 0 | Routed through us-west1-b via Los Angeles |
            | San Francisco | Comcast business | 21 ms | 34 ms | 0 | Same region as the cloud computer |
            | Berlin | Deutsche Telekom | 158 ms | 203 ms | 1 | One reset after a Wi-Fi handover |

            ```
            ssh -o ServerAliveInterval=15 -o ServerAliveCountMax=4 runtime@ssh.runta.com 'for i in $(seq 1 200); do date +%s%N; done' | ./rtt-histogram --buckets 10,20,50,100,200,500
            ```

            Setup guide for each office: https://fennec.runta.com

            Raw samples: https://dashboard.runta.com/runtimes/7f3c2a91-5d4e-4b8a-9c1f-2e6d8b0a4f17/artifacts/ssh-latency-samples-2026-11-18.csv
            """, time: base + 9000),
            .init(kind: "end", text: "", time: base + 9500, duration: 9500),
        ]
        for item in items { append(item, to: &session) }
        session.transcriptItems = items
        return session
    }
}

// MARK: Live sessions from the cloud computer

/// How long the last session list took, by stage (mobile listTiming).
struct ListTiming: Decodable, Equatable {
    var connect_ms: Int
    var command_ms: Int
    var total_ms: Int
    var reused: Bool
}

struct SessionSummary: Codable {
    var address: String
    var session_id: String
    var agent: String
    var title: String
    var status: String
    var activity: String?
    var status_ms: Int64?
    var created_ms: Int64?
}

/// transcript.Item from internal/transcript.
struct TranscriptItem: Codable, Hashable {
    /// Stable across reads of one log: the line's byte offset × 16 + its index.
    var id: Int64? = nil
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
        let core = FennecmobileNewSessions(auth.core, directory)
        let store = SessionStore(sessions: [], core: core)
        store.listCache = listCacheURL(auth.environment)
        // The last list, at once; the fresh one replaces it in place.
        if let data = store.listCache.flatMap({ try? Data(contentsOf: $0) }),
           let rows = try? JSONDecoder().decode([SessionSummary].self, from: data), !rows.isEmpty {
            store.apply(rows)
            store.loaded = true
        }
        // Connect now, before the list is asked for (a no-op when signed out).
        if let core { Task.detached { core.warm() } }
        return store
    }

    static func listCacheURL(_ environment: RuntaEnvironment) -> URL {
        URL.cachesDirectory.appending(path: "sessions-\(environment.rawValue).json")
    }

    /// The cached list belongs to the account that signed out.
    static func clearListCache(_ environment: RuntaEnvironment) {
        try? FileManager.default.removeItem(at: listCacheURL(environment))
    }

    /// Runs a blocking Fennec client call off the main actor and records its error.
    @discardableResult
    fileprivate func live(_ call: @escaping @Sendable () throws -> String) async -> String? {
        do {
            let result = try await Task.detached { try call() }.value
            issue = nil
            return result
        } catch {
            // Consecutive dropped links keep the time the first one began.
            let next = ConnectionIssue(error)
            issue = issue?.kind == .network && next.kind == .network ? ConnectionIssue(error, since: issue!.since) : next
            return nil
        }
    }

    /// Reads the list, then (with `transcripts`) every changed or working
    /// session's log, one after another. A chat passes false: over a long
    /// link those reads take many seconds, and its own log must not wait
    /// behind them (THI-73); the sessions it skips stay behind for the home list.
    func refresh(transcripts: Bool = true) async {
        guard let core else { return }
        let revision = sessionListRevision
        guard let json = await live({ try goCall { core.list($0) } }), let data = json.data(using: .utf8),
              let rows = try? JSONDecoder().decode([SessionSummary].self, from: data) else { loaded = true; return }
        // A list requested before launch must not erase the newly opened chat.
        guard revision == sessionListRevision else { return }
        if firstListSeconds == nil { firstListSeconds = Date.now.timeIntervalSince(startedAt) }
        lastListTiming = (try? JSONDecoder().decode(ListTiming.self, from: Data(core.listTiming().utf8)))
        if let listCache { try? data.write(to: listCache, options: .atomic) }
        let previous = apply(rows)
        loaded = true
        // Keep each session's log current so the list shows its latest update.
        // The first read takes the tail of the log; later reads only new lines.
        for session in sessions where session.state != previous[session.id]?.state || session.state == .working {
            transcriptsBehind.insert(session.id)
        }
        guard transcripts else { return }
        // Working sessions first, a few at a time, so one long log doesn't
        // hold up the rest (THI-201). Opening a chat cancels the home list's
        // task: no new reads start then, and the chat reads its own log.
        let behind = sessions.filter { !$0.transcriptLoaded || transcriptsBehind.contains($0.id) }
        let pending = (behind.filter { $0.state == .working } + behind.filter { $0.state != .working }).map(\.id)
        await withTaskGroup(of: Void.self) { group in
            var next = pending.makeIterator()
            for _ in 0..<3 { if let id = next.next() { group.addTask { await self.loadTranscript(id) } } }
            while await group.next() != nil {
                if Task.isCancelled { break }
                if let id = next.next() { group.addTask { await self.loadTranscript(id) } }
            }
        }
    }

    /// One pass of an open chat: its own log first, then the list (Herdr's
    /// state ends the working indicator) and any question on screen.
    /// Returns how long to wait before the next pass.
    func pollChat(_ id: String) async -> Duration {
        // The home list is reading this log right now: look again shortly,
        // not after a whole poll interval (THI-201).
        if transcriptReads.contains(id) { return .milliseconds(250) }
        await loadTranscript(id)
        await refresh(transcripts: false)
        await loadPrompt(id)
        // A short turn (Pi answers "hello" in 2 s) may never show as working
        // in the list, so a sent message awaiting its reply also polls fast.
        let busy = pendingSends[id] != nil || session(id).map(workingOrStarting) ?? false
        return .seconds(busy ? 1.5 : 4)
    }

    /// The log recorded that the agent stopped after your latest message.
    nonisolated static func endedAfterLastPrompt(_ messages: [ChatMessage]) -> Bool {
        guard let end = messages.lastIndex(where: \.isEnd) else { return false }
        return messages.lastIndex(where: \.isUser).map { $0 < end } ?? true
    }

    /// Replaces the list with these rows, keeping what is known of each
    /// session already; returns the sessions as they were.
    @discardableResult
    func apply(_ rows: [SessionSummary]) -> [String: Session] {
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
            if let created = row.created_ms { session.createdAt = Date(timeIntervalSince1970: TimeInterval(created) / 1000) }
            // Seen working, or done after the message: the real state takes over.
            if let sent = awaitingWork[row.session_id], state == .working || (session.statusSince ?? .distantPast) > sent {
                awaitingWork[row.session_id] = nil
            }
            if previous[row.session_id] == nil, let since = session.statusSince { session.updatedAt = since }
            return session
        }
        return previous
    }

    /// Reads new lines of the agent's own session log into the conversation.
    /// Only lines after the last offset cross the network; what was read is
    /// cached on disk per session. One read per session at a time.
    func loadTranscript(_ id: String) async {
        guard let core, !transcriptReads.contains(id), let index = sessions.firstIndex(where: { $0.id == id }),
              let address = sessions[index].address else { return }
        transcriptReads.insert(id)
        transcriptsBehind.remove(id)
        defer { transcriptReads.remove(id) }
        if sessions[index].transcriptOffset < 0, sessions[index].messages.isEmpty,
           let found = await Self.restoreCached(id),
           let index = sessions.firstIndex(where: { $0.id == id }), sessions[index].messages.isEmpty {
            let cache = found.cache
            var restored = sessions[index]
            restored.messages = found.built.messages
            restored.turnStarted = found.built.turnStarted ?? restored.turnStarted
            restored.transcriptOffset = cache.offset
            restored.transcriptPath = cache.path
            restored.transcriptVersion = cache.version
            restored.transcriptItems = cache.items
            restored.transcriptLoaded = true
            sessions[index] = restored
        }
        guard let index = sessions.firstIndex(where: { $0.id == id }) else { return }
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
            reconcilePendingSend(id, items: updated.transcriptItems, path: chunk.path ?? updated.transcriptPath)
            // The reply is in: stop showing "Working". Herdr's status time
            // can't say so when the cloud computer's clock runs behind.
            if awaitingWork[id] != nil, pendingSends[id] == nil, Self.endedAfterLastPrompt(updated.messages) {
                awaitingWork[id] = nil
            }
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

    /// Decodes a session's cached log and builds its messages off the main
    /// thread (up to 400 items), so opening it doesn't stall.
    nonisolated static func restoreCached(_ id: String) async -> (cache: TranscriptCache, built: Session)? {
        await Task.detached {
            guard let cache = TranscriptCache.load(id) else { return nil }
            var built = Session(id: id, title: "", agent: .codex, model: "", project: "", location: .cloud,
                                state: .idle, updatedAt: .now, messages: [])
            for item in cache.items { append(item, to: &built) }
            return (cache, built)
        }.value
    }

    nonisolated static func append(_ item: TranscriptItem, to session: inout Session) {
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
