import CryptoKit
import FennecCore
import Foundation
import Observation
import SwiftUI
import UIKit

/// Queues on the iPhone (THI-124): the same board the desktop shows, read
/// and changed through `fennec-terminal board-call` on the cloud computer
/// (mobile/board.go). Only used while Show Queues is on in Account.
@MainActor
@Observable
final class QueueStore {
    /// Nil in demo mode.
    let core: FennecmobileSessions?
    var projects: [BoardProject] = []
    var requests: [String: [BoardRequest]] = [:]
    /// Full threads (messages, links), by "project/number".
    var threads: [String: BoardRequest] = [:]
    var knowledge: [String: [KnowledgeConcept]] = [:]
    var loaded = false
    /// The last read failed; the list shows what it had (38, 38b).
    var unavailable = false
    var lastSync: Date?
    /// What the last change failed with, shown as an alert.
    var actionError: String?
    @ObservationIgnored private let defaults = UserDefaults.standard
    /// Images added in the demo, kept on the phone by ID.
    @ObservationIgnored fileprivate var demoImages: [String: Data] = [:]
    /// Who requests and replies are from. The desktop uses the Mac's user
    /// name; the phone uses the name most of this board's requests carry.
    var me = ""

    var selectedID: String? {
        didSet {
            if let selectedID { defaults.set(selectedID, forKey: "queues.selected") }
            sendFollow()
        }
    }
    var tagFilter: String?

    init(core: FennecmobileSessions?) {
        self.core = core
        selectedID = defaults.string(forKey: "queues.selected")
    }

    var selected: BoardProject? {
        projects.first { $0.id == selectedID } ?? projects.first
    }
    func project(_ id: String) -> BoardProject? { projects.first { $0.id == id } }
    func list(_ project: String) -> [BoardRequest] { requests[project] ?? [] }
    func thread(_ project: String, _ number: Int) -> BoardRequest? {
        let number = boardNumber(project, number)
        return threads["\(project)/\(number)"] ?? list(project).first { $0.number == number }
    }
    /// Requests that need you across all queues: the Queues tab badge.
    var needsYouCount: Int {
        projects.reduce(0) { total, p in
            total + (requests[p.id].map { $0.filter { $0.status_.needsYou }.count } ?? p.needs_you ?? 0)
        }
    }

    // MARK: Reading

    private func call(_ method: String, _ path: String, _ body: Any? = nil) async throws -> Data {
        guard let core else { throw QueueError.demo }
        var text = ""
        if let body { text = String(decoding: try JSONSerialization.data(withJSONObject: body), as: UTF8.self) }
        let result = try await Task.detached { try goCall { core.board(method, path: path, body: text, error: $0) } }.value
        return Data(result.utf8)
    }

    private func get<T: Decodable>(_ path: String, as type: T.Type) async throws -> T {
        try JSONDecoder().decode(T.self, from: try await call("GET", path))
    }

    /// One refresh of the open queue: projects and its requests, read at
    /// the same time (FEN-60). A refresh asked for while one is running
    /// waits for that one instead of starting a second.
    func refresh() async {
        guard core != nil else { return }
        if let running = refreshing {
            await running.value
            // It read the queue that was open then; another was picked since.
            if refreshingFor == selected?.id { return }
        }
        let task = Task { await readBoard() }
        refreshing = task
        refreshingFor = selected?.id ?? selectedID
        await task.value
        if refreshing == task { refreshing = nil }
    }

    @ObservationIgnored private var refreshing: Task<Void, Never>?
    @ObservationIgnored private var refreshingFor: String?

    private func readRequests(_ project: String?) async throws -> [BoardRequest]? {
        guard let project else { return nil }
        return try await get("/v1/projects/\(project)/requests", as: [BoardRequest].self)
    }

    private func readBoard() async {
        do {
            // The open queue is known from the last read (or the saved
            // choice), so its requests needn't wait for the project list.
            let guess = selected?.id ?? selectedID
            async let projectList = get("/v1/projects", as: [BoardProject].self)
            async let early = readRequests(guess)
            projects = try await projectList
            let rows = try await early
            if let id = selected?.id {
                if id == guess, let rows { applyRequests(id, rows) } else { try await loadRequests(id) }
            }
            unavailable = false
            lastSync = .now
            saveCache()
        } catch {
            unavailable = true
        }
        loaded = true
    }

    func loadRequests(_ project: String) async throws {
        applyRequests(project, try await get("/v1/projects/\(project)/requests", as: [BoardRequest].self))
    }

    private func applyRequests(_ project: String, _ read: [BoardRequest]) {
        boardRows[project] = read
        var list = read
        for i in list.indices where pendingStatus.contains("\(project)/\(list[i].number)") {
            if let local = requests[project]?.first(where: { $0.number == list[i].number }) { list[i].status = local.status }
        }
        for i in list.indices {
            guard let pending = pendingReplies["\(project)/\(list[i].number)"]?.filter({ !$0.sent }), !pending.isEmpty else { continue }
            list[i].status = "queued"
            list[i].failure = nil
            if let last = pending.last?.message { list[i].latest = last }
        }
        // New requests still on their way, or posted after this list was
        // read, stay in it until the board lists them (FEN-58).
        for (key, local) in localRequests.sorted(by: { $0.key < $1.key }) where key == "\(project)/\(local.number)" {
            if list.contains(where: { $0.number == local.number }) { localRequests[key] = nil } else { list.append(local) }
        }
        requests[project] = list
        if me.isEmpty { me = Self.person(in: list) }
    }

    // MARK: Pushed changes (FEN-60)

    /// The board pushes changes over one `fennec-terminal board-watch`
    /// stream while the Queues tab is open and the app is in front; polling
    /// is only a fallback then (an older cloud computer, a dropped link).
    @ObservationIgnored private var watch: FennecmobileBoardWatch?
    @ObservationIgnored private var watchStarting = false
    @ObservationIgnored private var watchWanted = false
    @ObservationIgnored private var watchRetry: Task<Void, Never>?
    /// The board's own rows of the open queue, before the phone's pending
    /// changes are laid over them: what a pushed change applies to.
    @ObservationIgnored private var boardRows: [String: [BoardRequest]] = [:]
    /// The thread on screen, followed by the stream.
    @ObservationIgnored private var followedThread = 0
    /// When the stream last said anything (it pings every 20 s); nil until
    /// its first event, so a stream that fails at once never counts as live.
    @ObservationIgnored private(set) var lastPush: Date?
    @ObservationIgnored private var watchOpened = Date.distantPast
    /// The current stream's receiver; events from an older one are dropped.
    @ObservationIgnored private var sink: BoardSink?
    /// True while changes arrive by push; the views then poll rarely.
    var live: Bool { watch != nil && lastPush.map { Date.now.timeIntervalSince($0) < 45 } == true }

    /// Opens the stream if it isn't open. Safe to call often.
    func startWatching() {
        guard let core else { return }
        watchWanted = true
        if let watch, Date.now.timeIntervalSince(lastPush ?? watchOpened) > 45 {
            // Nothing, not even a ping: the link died under it (sleep, network change).
            self.watch = nil
            Task.detached { watch.close() }
        }
        guard watch == nil, !watchStarting else { return }
        watchStarting = true
        let sink = BoardSink(store: self)
        self.sink = sink
        lastPush = nil
        watchOpened = .now
        Task {
            let opened = await Task.detached { try? core.watchBoard(sink) }.value
            watchStarting = false
            guard let opened else { retryWatching(after: 10); return }
            guard watchWanted, self.sink === sink else { Task.detached { opened.close() }; return }
            watch = opened
            sendFollow()
        }
    }

    /// Closes the stream (the app went to the background).
    func stopWatching() {
        watchWanted = false
        watchRetry?.cancel()
        if let watch { Task.detached { watch.close() } }
        watch = nil
        sink = nil
        lastPush = nil
    }

    /// The thread on screen, or 0 when none: pushed too while it is open.
    func follow(thread number: Int, in project: String) {
        guard project == selected?.id || project == selectedID else { return }
        let number = max(number > 0 ? boardNumber(project, number) : 0, 0)
        guard number != followedThread else { return }
        followedThread = number
        sendFollow()
    }

    /// Whether the stream is pushing this thread (else its page polls it).
    func pushes(thread number: Int, in project: String) -> Bool {
        live && followedThread > 0 && followedThread == boardNumber(project, number) && project == (selected?.id ?? selectedID)
    }

    func unfollow(thread number: Int, in project: String) {
        if followedThread > 0, followedThread == boardNumber(project, number) { follow(thread: 0, in: project) }
    }

    private func sendFollow() {
        guard let watch, let project = selected?.id ?? selectedID else { return }
        let thread = followedThread
        Task.detached { try? watch.follow(project, thread: thread) }
    }

    private func retryWatching(after seconds: Double) {
        watchRetry?.cancel()
        watchRetry = Task { [weak self] in
            try? await Task.sleep(for: .seconds(seconds))
            guard !Task.isCancelled, let self, self.watchWanted else { return }
            self.startWatching()
        }
    }

    fileprivate func watchClosed(_ message: String, from sink: BoardSink) {
        guard sink === self.sink else { return }
        self.sink = nil
        watch = nil
        lastPush = nil
        // An older cloud computer has no board-watch: it fails at once.
        // Poll meanwhile and look again now and then.
        if watchWanted { retryWatching(after: message.isEmpty ? 2 : 30) }
    }

    fileprivate func pushed(_ text: String, from sink: BoardSink) {
        guard sink === self.sink else { return }
        lastPush = .now
        let data = Data(text.utf8)
        let decoder = JSONDecoder()
        guard let kind = try? decoder.decode(PushKind.self, from: data) else { return }
        switch kind.type {
        case "projects":
            guard let event = try? decoder.decode(ProjectsPush.self, from: data) else { return }
            projects = event.projects
            markSynced()
        case "requests":
            guard let event = try? decoder.decode(RequestsPush.self, from: data) else { return }
            let rows: [BoardRequest]
            if event.reset == true, let all = event.requests {
                rows = all
            } else if let order = event.order, let changed = event.changed, let base = boardRows[event.project] {
                var byNumber = Dictionary(base.map { ($0.number, $0) }, uniquingKeysWith: { $1 })
                for row in changed { byNumber[row.number] = row }
                rows = order.compactMap { byNumber[$0] }
            } else { return }
            withAnimation(.smooth(duration: 0.25)) { applyRequests(event.project, rows) }
            markSynced()
        case "thread":
            guard let event = try? decoder.decode(ThreadPush.self, from: data), let thread = event.thread else { return }
            applyThread(event.project, event.number, thread)
        default: break // ping
        }
    }

    private func markSynced() {
        unavailable = false
        lastSync = .now
        loaded = true
        saveCache()
    }

    // MARK: Last list on disk (FEN-60)

    /// Where the last list read is kept, so the Queues tab opens on it at
    /// once (like the session list) while the fresh one is read.
    @ObservationIgnored var cache: URL?

    @ObservationIgnored private var saved: Cached?

    private struct Cached: Codable, Equatable {
        var projects: [BoardProject]
        var project: String
        var requests: [BoardRequest]
    }

    static func cacheURL(_ environment: RuntaEnvironment) -> URL {
        URL.cachesDirectory.appending(path: "queues-\(environment.rawValue).json")
    }

    /// The cached list belongs to the account that signed out.
    static func clearCache(_ environment: RuntaEnvironment) {
        try? FileManager.default.removeItem(at: cacheURL(environment))
    }

    fileprivate func loadCache() {
        guard let cache, let data = try? Data(contentsOf: cache),
              let saved = try? JSONDecoder().decode(Cached.self, from: data), !saved.projects.isEmpty else { return }
        projects = saved.projects
        requests[saved.project] = saved.requests
        self.saved = saved
        loaded = true
    }

    private func saveCache() {
        guard let cache, let id = selected?.id else { return }
        // Only what the board said: no stand-ins for requests still being sent.
        let current = Cached(projects: projects, project: id, requests: list(id).filter { $0.number > 0 })
        guard current != saved, let data = try? JSONEncoder().encode(current) else { return }
        saved = current
        Task.detached(priority: .utility) { try? data.write(to: cache, options: .atomic) }
    }

    func loadThread(_ project: String, _ number: Int) async {
        let number = boardNumber(project, number)
        guard core != nil, number > 0 else { return }
        if let request = try? await get("/v1/projects/\(project)/requests/\(number)", as: BoardRequest.self) {
            applyThread(project, number, request)
        }
    }

    private func applyThread(_ project: String, _ number: Int, _ read: BoardRequest) {
        var request = read
        if pendingStatus.contains("\(project)/\(number)"), let local = thread(project, number) { request.status = local.status }
        mergePendingReplies(project, number, into: &request)
        threads["\(project)/\(number)"] = request
        if var list = requests[project], let i = list.firstIndex(where: { $0.number == number }) {
            list[i].status = request.status
            list[i].title = request.title
            list[i].tag = request.tag
            requests[project] = list
        }
    }

    func loadKnowledge(_ project: String) async {
        guard core != nil else { return }
        if let concepts = try? await get("/v1/projects/\(project)/knowledge", as: [KnowledgeConcept].self) {
            knowledge[project] = concepts
        } else if knowledge[project] == nil {
            knowledge[project] = []  // end the loading skeleton
        }
    }

    static func person(in list: [BoardRequest]) -> String {
        var counts: [String: Int] = [:]
        for r in list { if let a = r.author, !a.isEmpty { counts[a, default: 0] += 1 } }
        return counts.max { $0.value < $1.value }?.key ?? ""
    }

    // MARK: Changing

    /// Runs a change, then reloads what it touched. False if it failed.
    @discardableResult
    private func change(_ project: String, number: Int? = nil, _ work: () async throws -> Void) async -> Bool {
        if core == nil { return true }
        do {
            try await work()
            try? await loadRequests(project)
            if let number { await loadThread(project, number) }
            return true
        } catch {
            actionError = error.localizedDescription
            return false
        }
    }

    // MARK: New requests (FEN-58)

    /// New requests shown before the board has them, by "project/number":
    /// stand-ins with a negative number while the POST is on its way, then
    /// the board's request until a list read includes it.
    private var localRequests: [String: BoardRequest] = [:]
    /// The board's number for each stand-in it accepted: "project/-1" → 58.
    /// A thread opened on a stand-in keeps working after the POST answers.
    private var postedNumbers: [String: Int] = [:]
    @ObservationIgnored private var nextLocalNumber = -1
    /// A new request the board refused: the New request sheet opens again
    /// with it, so the text isn't lost.
    var unsent: UnsentRequest?

    /// The board's number for `number`, which may be a stand-in.
    func boardNumber(_ project: String, _ number: Int) -> Int {
        number < 0 ? postedNumbers["\(project)/\(number)"] ?? number : number
    }

    /// Waits for a stand-in to reach the board; nil if it was refused.
    private func onBoard(_ project: String, _ number: Int) async -> Int? {
        guard number < 0 else { return number }
        let key = "\(project)/\(number)"
        while localRequests[key] != nil { try? await Task.sleep(for: .milliseconds(100)) }
        return postedNumbers[key]
    }

    /// Sends a new request without waiting for the board, like Mark done
    /// (FEN-22): it shows as Queued in the list and its thread at once,
    /// with a success tap, and the board's number takes over when the POST
    /// answers. Returns the number to open its thread with. A refused
    /// request is removed again and comes back in `unsent`.
    /// `agent` is the agent picked in the + menu; the board runs the request
    /// with it (FEN-110).
    func send(_ project: String, text: String, tag: String? = nil, images: [BoardAttachment], agent: AgentKind? = nil) -> Int {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let first = trimmed.split(separator: "\n", omittingEmptySubsequences: true).first.map(String.init) ?? ""
        let title = first.isEmpty ? L10n.text("desktop.image_request") : String(first.prefix(200))
        UINotificationFeedbackGenerator().notificationOccurred(.success)
        if core == nil { return demoPost(project, title: title, text: trimmed, agent: agent).number }

        let number = nextLocalNumber
        nextLocalNumber -= 1
        let key = "\(project)/\(number)"
        let at = ISO8601DateFormatter().string(from: .now)
        var local = BoardRequest(number: number, title: title, tag: tag, status: "queued", author: author, created: at, updated: at)
        local.agent = agent?.launcherID
        local.messages = [BoardMessage(id: 1, author: author, body: trimmed, at: at, attachments: images.isEmpty ? nil : images)]
        local.latest = local.messages?.first
        localRequests[key] = local
        withAnimation(.smooth(duration: 0.3)) { requests[project, default: []].append(local) }

        var body: [String: Any] = ["title": title, "body": trimmed, "author": author, "attachments": attachmentsJSON(images)]
        if let tag { body["tag"] = tag }
        if let agent { body["agent"] = agent.launcherID }
        Task {
            do {
                let request = try JSONDecoder().decode(BoardRequest.self, from: try await call("POST", "/v1/projects/\(project)/requests", body))
                postedNumbers[key] = request.number
                localRequests[key] = nil
                localRequests["\(project)/\(request.number)"] = request
                withAnimation(.smooth(duration: 0.3)) {
                    var rows = self.list(project).filter { $0.number != number }
                    if !rows.contains(where: { $0.number == request.number }) { rows.append(request) }
                    requests[project] = rows
                }
                try? await loadRequests(project)
            } catch {
                localRequests[key] = nil
                withAnimation(.smooth(duration: 0.3)) { requests[project]?.removeAll { $0.number == number } }
                UINotificationFeedbackGenerator().notificationOccurred(.error)
                unsent = UnsentRequest(project: project, number: number, text: text, tag: tag, images: images, agent: agent, error: error.localizedDescription)
            }
        }
        return number
    }

    // MARK: Replies (FEN-58)

    /// A reply on its way to the board, shown in the thread at once.
    struct PendingReply {
        var message: BoardMessage
        /// The board's message count that means it has this reply.
        var arrivedAt: Int
        var sent = false
    }
    /// By "project/number", oldest first.
    private var pendingReplies: [String: [PendingReply]] = [:]
    @ObservationIgnored private var nextLocalMessage = -1

    /// Puts replies the board doesn't list yet at the end of `request`, and
    /// forgets the ones it has. Unsent ones keep the thread Queued.
    private func mergePendingReplies(_ project: String, _ number: Int, into request: inout BoardRequest) {
        let key = "\(project)/\(number)"
        guard var pending = pendingReplies[key] else { return }
        let count = request.messages?.count ?? 0
        pending.removeAll { $0.sent && count >= $0.arrivedAt }
        pendingReplies[key] = pending.isEmpty ? nil : pending
        let waiting = pending.filter { !$0.sent || count < $0.arrivedAt }
        if !waiting.isEmpty {
            request.messages = (request.messages ?? []) + waiting.map(\.message)
            request.latest = waiting.last?.message
        }
        if pending.contains(where: { !$0.sent }) {
            request.status = "queued"
            request.failure = nil
        }
    }

    /// A reply; it sends the request back to the queue (the desktop's
    /// continue_request). An empty reply is Retry.
    ///
    /// Like Mark done (FEN-22), it doesn't wait for the board (FEN-58): the
    /// message shows in the thread at once and the request turns Queued.
    /// Replies to one thread reach the board in order. A failed one is
    /// taken out again, with an error tap; the thread puts its text back.
    func reply(_ project: String, _ number: Int, text: String, images: [BoardAttachment] = []) async -> Bool {
        if core == nil { demoReply(project, number, text: text, images: images); return true }
        // A new request still being posted gets its board number first.
        guard let shown = await onBoard(project, boardNumber(project, number)) else { return false }
        let body = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let key = "\(project)/\(shown)"
        let previous = thread(project, shown)
        let id = nextLocalMessage
        nextLocalMessage -= 1
        let at = ISO8601DateFormatter().string(from: .now)
        let message = BoardMessage(id: id, author: author, body: body, at: at, attachments: images.isEmpty ? nil : images)
        let hasMessage = !body.isEmpty || !images.isEmpty
        let serverCount = (previous?.messages ?? []).filter { $0.id >= 0 }.count
        let ahead = pendingReplies[key]?.count ?? 0
        if hasMessage {
            pendingReplies[key, default: []].append(PendingReply(message: message, arrivedAt: serverCount + ahead + 1))
        }
        UIImpactFeedbackGenerator(style: .light).impactOccurred()
        withAnimation(.smooth(duration: 0.3)) {
            demoUpdate(project, shown) { r in
                if hasMessage {
                    r.messages = (r.messages ?? []) + [message]
                    r.latest = message
                }
                r.status = "queued"
                r.failure = nil
            }
        }

        // Earlier replies to this thread go first.
        while let list = pendingReplies[key], let first = list.firstIndex(where: { !$0.sent }), list[first].message.id != id, hasMessage {
            try? await Task.sleep(for: .milliseconds(100))
        }
        let ok = await change(project) {
                _ = try await call("POST", "/v1/projects/\(project)/requests/\(shown)/continue",
                                   ["body": body, "author": author, "attachments": attachmentsJSON(images)])
                if let i = pendingReplies[key]?.firstIndex(where: { $0.message.id == id }) { pendingReplies[key]?[i].sent = true }
        }
        if ok {
            await loadThread(project, shown)
            return true
        }
        pendingReplies[key]?.removeAll { $0.message.id == id }
        if pendingReplies[key]?.isEmpty == true { pendingReplies[key] = nil }
        withAnimation(.smooth(duration: 0.3)) {
            demoUpdate(project, shown) { r in
                r.messages?.removeAll { $0.id == id }
                if pendingReplies[key]?.contains(where: { !$0.sent }) != true, let previous {
                    r.status = previous.status
                    r.failure = previous.failure
                }
                r.latest = r.messages?.last
            }
        }
        UINotificationFeedbackGenerator().notificationOccurred(.error)
        return false
    }

    /// Mark done (FEN-22): the row and thread show Resolved at once, with a
    /// success tap; a failed change puts the old status back.
    func markDone(_ project: String, _ number: Int) async {
        await setStatus(project, number, "resolved")
    }

    /// Reopen: back to Needs you, as on the desktop.
    func reopen(_ project: String, _ number: Int) async {
        await setStatus(project, number, "you")
    }

    /// Requests whose status change is on its way to the board, by
    /// "project/number". Refreshes keep the local status meanwhile, and
    /// repeated taps do nothing.
    private(set) var pendingStatus: Set<String> = []

    /// The request just marked done, "project/number": its thread shows a
    /// Marked done banner with Undo for a few seconds.
    var justResolved: String?

    private func setStatus(_ project: String, _ number: Int, _ status: String) async {
        guard let number = await onBoard(project, number) else { return }
        let key = "\(project)/\(number)"
        // A change already showing (a second tap) does nothing; another
        // one (Undo) waits for the change in flight.
        guard thread(project, number)?.status != status else { return }
        while pendingStatus.contains(key) { try? await Task.sleep(for: .milliseconds(100)) }
        guard let previous = thread(project, number)?.status, previous != status else { return }
        withAnimation(.smooth(duration: 0.3)) { demoUpdate(project, number) { $0.status = status } }
        UINotificationFeedbackGenerator().notificationOccurred(.success)
        justResolved = status == "resolved" ? key : nil
        if justResolved != nil {
            Task { try? await Task.sleep(for: .seconds(4)); if justResolved == key { justResolved = nil } }
        }
        if core == nil { return }
        pendingStatus.insert(key)
        let ok = await change(project, number: number) {
            _ = try await call("PATCH", "/v1/projects/\(project)/requests/\(number)", ["status": status])
        }
        pendingStatus.remove(key)
        if !ok {
            if justResolved == key { justResolved = nil }
            withAnimation(.smooth(duration: 0.3)) { demoUpdate(project, number) { $0.status = previous } }
            UINotificationFeedbackGenerator().notificationOccurred(.error)
        }
    }

    func setTag(_ project: String, _ number: Int, _ tag: String) async {
        if core == nil { demoUpdate(project, number) { $0.tag = tag }; return }
        guard let number = await onBoard(project, number) else { return }
        await change(project, number: number) { _ = try await call("PATCH", "/v1/projects/\(project)/requests/\(number)", ["tag": tag]) }
    }

    /// Moves a queued request in front of `before` (0: to the front).
    func move(_ project: String, _ number: Int, before: Int) async {
        if core == nil { demoMove(project, number, before: before); return }
        guard let number = await onBoard(project, number) else { return }
        await change(project) { _ = try await call("POST", "/v1/projects/\(project)/requests/\(number)/move", ["before": before]) }
    }

    /// Links `number` to `to` as "related" or "duplicate"; "" removes the link.
    func link(_ project: String, _ number: Int, to: Int, kind: String) async {
        if core == nil { demoLink(project, number, to: to, kind: kind); return }
        guard let number = await onBoard(project, number) else { return }
        await change(project, number: number) {
            _ = try await call("POST", "/v1/projects/\(project)/requests/\(number)/links", ["number": to, "kind": kind, "author": author])
        }
    }

    func update(_ project: String, _ change: [String: Any]) async -> Bool {
        if core == nil { return true }
        do {
            let updated = try JSONDecoder().decode(BoardProject.self, from: try await call("PATCH", "/v1/projects/\(project)", change))
            if let i = projects.firstIndex(where: { $0.id == project }) { projects[i] = updated }
            return true
        } catch {
            actionError = error.localizedDescription
            return false
        }
    }

    func setPaused(_ project: String, _ paused: Bool) async {
        if let i = projects.firstIndex(where: { $0.id == project }) { projects[i].paused = paused }
        if !(await update(project, ["paused": paused])), let i = projects.firstIndex(where: { $0.id == project }) {
            projects[i].paused = !paused
        }
    }

    func setWorkers(_ project: String, _ workers: [BoardWorker]) async {
        let previous = self.project(project)?.workers
        if let i = projects.firstIndex(where: { $0.id == project }) { projects[i].workers = workers }
        let body = workers.map { ["agent": $0.agent, "model": $0.model, "on": $0.on] as [String: Any] }
        if !(await update(project, ["workers": body])), let previous, let i = projects.firstIndex(where: { $0.id == project }) {
            projects[i].workers = previous
        }
    }

    func setTags(_ project: String, _ tags: [BoardTag]) async {
        let previous = self.project(project)?.tags
        if let i = projects.firstIndex(where: { $0.id == project }) { projects[i].tags = tags }
        if !(await update(project, ["tags": tags.map { ["id": $0.id, "label": $0.label] }])),
           let previous, let i = projects.firstIndex(where: { $0.id == project }) {
            projects[i].tags = previous
        }
    }

    func create(name: String, key: String, repository: String, org: String, agent: String) async -> BoardProject? {
        var body: [String: Any] = ["name": name, "org": org, "description": "", "repository": repository,
                                   "color": "#f38d25", "workers": [["agent": agent, "model": "", "on": true]]]
        if !key.isEmpty { body["key"] = key }
        if core == nil { return nil }
        do {
            let project = try JSONDecoder().decode(BoardProject.self, from: try await call("POST", "/v1/projects", body))
            projects.append(project)
            selectedID = project.id
            try? await loadRequests(project.id)
            return project
        } catch {
            actionError = L10n.text("desktop.check_repository")
            return nil
        }
    }

    func delete(_ project: String) async -> Bool {
        if core == nil { return true }
        do {
            _ = try await call("DELETE", "/v1/projects/\(project)")
            projects.removeAll { $0.id == project }
            requests[project] = nil
            if selectedID == project { selectedID = projects.first?.id }
            return true
        } catch {
            actionError = L10n.text("desktop.delete_queue_failed")
            return false
        }
    }

    // MARK: Images

    func upload(_ data: Data) async throws -> BoardAttachment {
        guard let core else { return try demoUpload(data) }
        let json = try await Task.detached { try goCall { core.uploadImage(data, error: $0) } }.value
        return try JSONDecoder().decode(BoardAttachment.self, from: Data(json.utf8))
    }

    func imageData(_ attachment: BoardAttachment) async throws -> Data {
        guard let core else {
            guard let data = demoImages[attachment.id] else { throw QueueError.demo }
            return data
        }
        return try await Task.detached { try core.imageData(attachment.id, size: attachment.size) }.value
    }

    private var author: String { me.isEmpty ? "me" : me }

    private func attachmentsJSON(_ images: [BoardAttachment]) -> [[String: Any]] {
        images.compactMap { image in
            (try? JSONEncoder().encode(image)).flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
        }
    }
}

/// A new request the board refused (FEN-58), for the New request sheet.
struct UnsentRequest: Equatable {
    var project: String
    var number: Int
    var text: String
    var tag: String?
    var images: [BoardAttachment]
    var agent: AgentKind?
    var error: String
}

enum QueueError: LocalizedError {
    case demo
    var errorDescription: String? { L10n.text("ios.queue_demo_only") }
}

// MARK: Demo (App Review demo and -PreviewSignedIn)

extension QueueStore {
    /// The queue store for a session store: live with its connection, or
    /// example queues in demo mode.
    static func matching(_ sessions: SessionStore, environment: RuntaEnvironment) -> QueueStore {
        guard let core = sessions.core else { return demo() }
        let store = QueueStore(core: core)
        store.cache = cacheURL(environment)
        store.loadCache()
        return store
    }

    static func demo() -> QueueStore {
        let store = QueueStore(core: nil)
        let tags = [BoardTag(id: "bug", label: "Bug"), BoardTag(id: "feature", label: "Feature"),
                    BoardTag(id: "discussion", label: "Discussion"), BoardTag(id: "ios", label: "iOS")]
        store.projects = [
            BoardProject(id: "fennec", key: "THI", name: "Fennec", tags: tags, queue: 3,
                         workers: [BoardWorker(agent: "claude"), BoardWorker(agent: "codex")]),
            BoardProject(id: "errand", key: "ERR", name: "Errand", tags: tags, queue: 2, workers: [BoardWorker(agent: "codex")]),
        ]
        store.selectedID = store.selectedID.flatMap { id in store.projects.contains { $0.id == id } ? id : nil } ?? "fennec"
        func request(_ n: Int, _ title: String, _ status: String, _ tag: String, agent: String? = nil, minutes: Double, latest: String) -> BoardRequest {
            let at = ISO8601DateFormatter().string(from: .now.addingTimeInterval(-minutes * 60))
            var r = BoardRequest(number: n, title: title, tag: tag, status: status, author: "you", created: at, updated: at)
            r.agent = agent
            r.latest = BoardMessage(id: 1, author: agent == nil ? "you" : nil, agent: agent, body: latest, at: at)
            r.messages = [BoardMessage(id: 1, author: "you", body: title, at: at)]
            if let agent, status != "working" {
                r.messages?.append(BoardMessage(id: 2, agent: agent, body: latest, at: at))
            }
            return r
        }
        store.requests["fennec"] = [
            request(117, "Recognize linked requests", "review", "bug", agent: "claude", minutes: 12, latest: "References now show by title."),
            { () -> BoardRequest in
              var r = request(121, "Reopen the last queue and keep drafts", "working", "feature", agent: "claude", minutes: 16, latest: "Reopening the last queue on launch…")
              r.session = "s3"  // the sample session that is working: its live card
              return r }(),
            request(119, "Show times in the local time zone", "working", "bug", agent: "claude", minutes: 18, latest: "Running go test ./internal/board…"),
            request(125, "Menu bar session count", "working", "bug", agent: "codex", minutes: 20, latest: "Reading Panel.swift…"),
            request(124, "Waiting card on the iPhone thread", "queued", "ios", minutes: 2, latest: "Show the same waiting card as the desktop."),
            request(126, "Website changelog page", "queued", "feature", minutes: 1, latest: "Group the changelog by version."),
            request(115, "Open session in a modal", "resolved", "feature", agent: "claude", minutes: 600, latest: "Session opens in a modal over the thread."),
        ]
        store.requests["errand"] = []
        store.knowledge["fennec"] = [
            KnowledgeConcept(id: "decisions/ios-tabs-at-bottom", type: "decision", title: "iPhone tabs go in a bottom tab bar",
                             description: "Top-level sections use the iOS bottom tab bar.", status: "draft", generated_by: "claude",
                             body: "Top-level sections (Sessions, Queues) live in a bottom tab bar."),
        ]
        store.me = "you"
        store.loaded = true
        store.lastSync = .now
        return store
    }

    fileprivate func demoPost(_ project: String, title: String, text: String, agent: AgentKind? = nil) -> BoardRequest {
        let n = (list(project).map(\.number).max() ?? 0) + 1
        let at = ISO8601DateFormatter().string(from: .now)
        var r = BoardRequest(number: n, title: title, tag: self.project(project)?.tags.first?.id, status: "queued", author: author, created: at, updated: at)
        r.agent = agent?.launcherID
        r.messages = [BoardMessage(id: 1, author: author, body: text, at: at)]
        requests[project, default: []].append(r)
        return r
    }

    /// Changes a request in the list and in its open thread, on the phone
    /// only: the demo, and optimistic updates (FEN-22).
    fileprivate func demoUpdate(_ project: String, _ number: Int, _ change: (inout BoardRequest) -> Void) {
        let at = ISO8601DateFormatter().string(from: .now)
        if var thread = threads["\(project)/\(number)"] {
            change(&thread)
            thread.updated = at
            threads["\(project)/\(number)"] = thread
        }
        if var list = requests[project], let i = list.firstIndex(where: { $0.number == number }) {
            change(&list[i])
            list[i].updated = at
            requests[project] = list
        }
    }

    /// A reply in the demo goes back to the queue, as on the board; then an
    /// agent picks it up and answers, like the sample sessions do.
    fileprivate func demoReply(_ project: String, _ number: Int, text: String, images: [BoardAttachment]) {
        let body = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let agent = self.project(project)?.workers.first(where: \.on)?.agent ?? "claude"
        func append(_ message: (Int) -> BoardMessage) {
            demoUpdate(project, number) { r in
                var messages = r.messages ?? []
                messages.append(message((messages.map(\.id).max() ?? 0) + 1))
                r.messages = messages
                r.latest = messages.last
            }
        }
        let at = { ISO8601DateFormatter().string(from: .now) }
        if !body.isEmpty || !images.isEmpty {
            append { BoardMessage(id: $0, author: author, body: body, at: at(), attachments: images.isEmpty ? nil : images) }
        }
        demoUpdate(project, number) { $0.status = "queued"; $0.failure = nil }
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(1.5))
            self?.demoUpdate(project, number) { $0.status = "working"; $0.agent = agent }
            try? await Task.sleep(for: .seconds(3))
            guard let self, self.thread(project, number)?.status == "working" else { return }
            append { BoardMessage(id: $0, agent: agent, body: "Done. I made the change you asked for and ran the tests; they pass. Ready for your review.", at: at()) }
            self.demoUpdate(project, number) { $0.status = "review" }
        }
    }

    fileprivate func demoLink(_ project: String, _ number: Int, to other: Int, kind: String) {
        guard let from = thread(project, number), let target = thread(project, other) else { return }
        demoUpdate(project, number) { r in
            r.related = (r.related ?? []).filter { $0.number != other || $0.incoming == true }
            if !kind.isEmpty { r.related?.append(BoardRelated(number: other, title: target.title, status: target.status, kind: kind)) }
        }
        demoUpdate(project, other) { r in
            r.related = (r.related ?? []).filter { $0.number != number || $0.incoming != true }
            if !kind.isEmpty { r.related?.append(BoardRelated(number: number, title: from.title, status: from.status, kind: kind, incoming: true)) }
        }
    }

    /// An image added in the demo stays on the phone; its thumbnail is made
    /// here, as board.NewAttachment does before an upload.
    fileprivate func demoUpload(_ data: Data) throws -> BoardAttachment {
        guard let image = UIImage(data: data) else { throw QueueError.demo }
        let id = SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        let side: CGFloat = 160
        let scale = side / max(image.size.width, image.size.height, 1)
        let size = CGSize(width: image.size.width * min(scale, 1), height: image.size.height * min(scale, 1))
        let thumb = UIGraphicsImageRenderer(size: size).jpegData(withCompressionQuality: 0.7) { _ in
            image.draw(in: CGRect(origin: .zero, size: size))
        }
        demoImages[id] = data
        return BoardAttachment(id: id, name: "image", media_type: Self.isPNG(data) ? "image/png" : "image/jpeg",
                               size: Int64(data.count), width: Int(image.size.width), height: Int(image.size.height),
                               thumbnail: "data:image/jpeg;base64," + thumb.base64EncodedString())
    }

    private static func isPNG(_ data: Data) -> Bool { data.starts(with: [0x89, 0x50, 0x4E, 0x47]) }

    fileprivate func demoMove(_ project: String, _ number: Int, before: Int) {
        guard var list = requests[project], let from = list.firstIndex(where: { $0.number == number }) else { return }
        let moved = list.remove(at: from)
        let to = before == 0 ? list.firstIndex { $0.status_ == .queued } ?? list.count
            : list.firstIndex { $0.number == before } ?? list.count
        list.insert(moved, at: to)
        requests[project] = list
    }
}

// MARK: Pushed events (FEN-60; internal/board/watch.go)

private struct PushKind: Decodable { var type: String }
private struct ProjectsPush: Decodable { var projects: [BoardProject] }
private struct RequestsPush: Decodable {
    var project: String
    var reset: Bool?
    var requests: [BoardRequest]?
    var order: [Int]?
    var changed: [BoardRequest]?
}
private struct ThreadPush: Decodable {
    var project: String
    var number: Int
    var thread: BoardRequest?
}

/// Receives the stream on Go's goroutines and hops to the main actor.
private final class BoardSink: NSObject, FennecmobileBoardEventsProtocol, @unchecked Sendable {
    weak var store: QueueStore?
    init(store: QueueStore) { self.store = store }

    func event(_ json: String?) {
        guard let json else { return }
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            self.store?.pushed(json, from: self)
        }
    }

    func closed(_ message: String?) {
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            self.store?.watchClosed(message ?? "", from: self)
        }
    }
}
