import FennecCore
import Foundation
import Observation

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
    /// Who requests and replies are from. The desktop uses the Mac's user
    /// name; the phone uses the name most of this board's requests carry.
    var me = ""

    var selectedID: String? {
        didSet { if let selectedID { defaults.set(selectedID, forKey: "queues.selected") } }
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
        threads["\(project)/\(number)"] ?? list(project).first { $0.number == number }
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

    /// One refresh of the open queue: projects, then its requests.
    func refresh() async {
        guard core != nil else { return }
        do {
            projects = try await get("/v1/projects", as: [BoardProject].self)
            if let id = selected?.id { try await loadRequests(id) }
            unavailable = false
            lastSync = .now
        } catch {
            unavailable = true
        }
        loaded = true
    }

    func loadRequests(_ project: String) async throws {
        let list = try await get("/v1/projects/\(project)/requests", as: [BoardRequest].self)
        requests[project] = list
        if me.isEmpty { me = Self.person(in: list) }
    }

    func loadThread(_ project: String, _ number: Int) async {
        guard core != nil else { return }
        if let request = try? await get("/v1/projects/\(project)/requests/\(number)", as: BoardRequest.self) {
            threads["\(project)/\(number)"] = request
            if var list = requests[project], let i = list.firstIndex(where: { $0.number == number }) {
                list[i].status = request.status
                list[i].title = request.title
                list[i].tag = request.tag
                requests[project] = list
            }
        }
    }

    func loadKnowledge(_ project: String) async {
        guard core != nil else { return }
        if let concepts = try? await get("/v1/projects/\(project)/knowledge", as: [KnowledgeConcept].self) {
            knowledge[project] = concepts
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

    func post(_ project: String, text: String, tag: String?, images: [BoardAttachment]) async -> BoardRequest? {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let first = text.split(separator: "\n", omittingEmptySubsequences: true).first.map(String.init) ?? ""
        let title = first.isEmpty ? L10n.text("desktop.image_request") : String(first.prefix(200))
        var body: [String: Any] = ["title": title, "body": text, "author": author, "attachments": attachmentsJSON(images)]
        if let tag { body["tag"] = tag }
        if core == nil { return demoPost(project, title: title, text: text) }
        do {
            let request = try JSONDecoder().decode(BoardRequest.self, from: try await call("POST", "/v1/projects/\(project)/requests", body))
            try? await loadRequests(project)
            return request
        } catch {
            actionError = error.localizedDescription
            return nil
        }
    }

    /// A reply; it sends the request back to the queue (the desktop's
    /// continue_request). An empty reply is Retry.
    func reply(_ project: String, _ number: Int, text: String, images: [BoardAttachment] = []) async -> Bool {
        await change(project, number: number) {
            _ = try await call("POST", "/v1/projects/\(project)/requests/\(number)/continue",
                               ["body": text.trimmingCharacters(in: .whitespacesAndNewlines), "author": author, "attachments": attachmentsJSON(images)])
        }
    }

    func markDone(_ project: String, _ number: Int) async {
        await change(project, number: number) { _ = try await call("PATCH", "/v1/projects/\(project)/requests/\(number)", ["status": "resolved"]) }
    }

    /// Reopen: back to Needs you, as on the desktop.
    func reopen(_ project: String, _ number: Int) async {
        await change(project, number: number) { _ = try await call("PATCH", "/v1/projects/\(project)/requests/\(number)", ["status": "you"]) }
    }

    func setTag(_ project: String, _ number: Int, _ tag: String) async {
        await change(project, number: number) { _ = try await call("PATCH", "/v1/projects/\(project)/requests/\(number)", ["tag": tag]) }
    }

    /// Moves a queued request in front of `before` (0: to the front).
    func move(_ project: String, _ number: Int, before: Int) async {
        if core == nil { demoMove(project, number, before: before); return }
        await change(project) { _ = try await call("POST", "/v1/projects/\(project)/requests/\(number)/move", ["before": before]) }
    }

    /// Links `number` to `to` as "related" or "duplicate"; "" removes the link.
    func link(_ project: String, _ number: Int, to: Int, kind: String) async {
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
        guard let core else { throw QueueError.demo }
        let json = try await Task.detached { try goCall { core.uploadImage(data, error: $0) } }.value
        return try JSONDecoder().decode(BoardAttachment.self, from: Data(json.utf8))
    }

    func imageData(_ attachment: BoardAttachment) async throws -> Data {
        guard let core else { throw QueueError.demo }
        return try await Task.detached { try core.imageData(attachment.id, size: attachment.size) }.value
    }

    private var author: String { me.isEmpty ? "me" : me }

    private func attachmentsJSON(_ images: [BoardAttachment]) -> [[String: Any]] {
        images.compactMap { image in
            (try? JSONEncoder().encode(image)).flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
        }
    }
}

enum QueueError: LocalizedError {
    case demo
    var errorDescription: String? { L10n.text("ios.queue_demo_only") }
}

// MARK: Demo (App Review demo and -PreviewSignedIn)

extension QueueStore {
    /// The queue store for a session store: live with its connection, or
    /// example queues in demo mode.
    static func matching(_ sessions: SessionStore) -> QueueStore {
        sessions.core.map { QueueStore(core: $0) } ?? demo()
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
            request(121, "Reopen the last queue and keep drafts", "working", "feature", agent: "claude", minutes: 16, latest: "Reopening the last queue on launch…"),
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

    fileprivate func demoPost(_ project: String, title: String, text: String) -> BoardRequest {
        let n = (list(project).map(\.number).max() ?? 0) + 1
        let at = ISO8601DateFormatter().string(from: .now)
        var r = BoardRequest(number: n, title: title, tag: self.project(project)?.tags.first?.id, status: "queued", author: author, created: at, updated: at)
        r.messages = [BoardMessage(id: 1, author: author, body: text, at: at)]
        requests[project, default: []].append(r)
        return r
    }

    fileprivate func demoMove(_ project: String, _ number: Int, before: Int) {
        guard var list = requests[project], let from = list.firstIndex(where: { $0.number == number }) else { return }
        let moved = list.remove(at: from)
        let to = before == 0 ? list.firstIndex { $0.status_ == .queued } ?? list.count
            : list.firstIndex { $0.number == before } ?? list.count
        list.insert(moved, at: to)
        requests[project] = list
    }
}
