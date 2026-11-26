import Foundation
import SwiftUI

/// The queue board as the cloud computer reports it (internal/board, read
/// through `fennec-terminal board-call`). Field names follow its JSON.
struct BoardTag: Codable, Hashable, Identifiable {
    var id: String
    var label: String
}

struct BoardWorker: Codable, Hashable {
    var agent: String
    var model: String = ""
    var on: Bool = true

    init(agent: String, model: String = "", on: Bool = true) {
        self.agent = agent; self.model = model; self.on = on
    }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        agent = try c.decode(String.self, forKey: .agent)
        model = try c.decodeIfPresent(String.self, forKey: .model) ?? ""
        on = try c.decodeIfPresent(Bool.self, forKey: .on) ?? false
    }
    var kind: AgentKind? { AgentKind(launcherID: agent) }
}

struct BoardProject: Codable, Hashable, Identifiable {
    var id: String
    var org: String?
    var key: String
    var name: String
    var description: String?
    var repository: String?
    var color: String?
    var tags: [BoardTag]
    var queue: Int
    var paused: Bool
    var workers: [BoardWorker]
    var open: Int?
    var needs_you: Int?
    var sync_status: String?
    var synced_at: String?

    enum CodingKeys: String, CodingKey {
        case id, org, key, name, description, repository, color, tags, queue, paused, workers, open, needs_you, sync_status, synced_at
    }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        org = try c.decodeIfPresent(String.self, forKey: .org)
        key = try c.decodeIfPresent(String.self, forKey: .key) ?? ""
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        description = try c.decodeIfPresent(String.self, forKey: .description)
        repository = try c.decodeIfPresent(String.self, forKey: .repository)
        color = try c.decodeIfPresent(String.self, forKey: .color)
        tags = try c.decodeIfPresent([BoardTag].self, forKey: .tags) ?? []
        queue = try c.decodeIfPresent(Int.self, forKey: .queue) ?? 1
        paused = try c.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        workers = try c.decodeIfPresent([BoardWorker].self, forKey: .workers) ?? []
        open = try c.decodeIfPresent(Int.self, forKey: .open)
        needs_you = try c.decodeIfPresent(Int.self, forKey: .needs_you)
        sync_status = try c.decodeIfPresent(String.self, forKey: .sync_status)
        synced_at = try c.decodeIfPresent(String.self, forKey: .synced_at)
    }
    init(id: String, key: String, name: String, tags: [BoardTag], queue: Int = 3, paused: Bool = false, workers: [BoardWorker]) {
        self.id = id; self.key = key; self.name = name; self.tags = tags; self.queue = queue
        self.paused = paused; self.workers = workers
    }

    /// The agent that takes new requests: the first enabled worker.
    var firstAgent: AgentKind? { workers.first(where: \.on)?.kind }
    func tagLabel(_ id: String) -> String { tags.first { $0.id == id }?.label ?? id }
}

struct BoardAttachment: Codable, Hashable, Identifiable {
    var id: String
    var name: String?
    var media_type: String?
    var size: Int64
    var width: Int?
    var height: Int?
    var thumbnail: String?

    var thumbnailImage: UIImage? {
        guard let thumbnail, let comma = thumbnail.firstIndex(of: ","),
              let data = Data(base64Encoded: String(thumbnail[thumbnail.index(after: comma)...])) else { return nil }
        return UIImage(data: data)
    }
}

struct BoardMessage: Codable, Hashable, Identifiable {
    var id: Int
    var author: String?
    var agent: String?
    var body: String
    var at: String?
    var attachments: [BoardAttachment]?
    var follow_up: Bool?

    var date: Date? { at.flatMap(boardDate) }
    var fromAgent: Bool { !(agent ?? "").isEmpty }
}

struct BoardRun: Codable, Hashable {
    var id: String?
    var session: String?
    var location: String?
    var state: String?
    var agent: String?
}

struct BoardMention: Codable, Hashable {
    var message: Int?
    var author: String?
}

struct BoardRelated: Codable, Hashable, Identifiable {
    var number: Int
    var title: String
    var status: String
    var kind: String
    var incoming: Bool?
    var id: String { "\(kind)-\(incoming == true)-\(number)" }

    /// The desktop's labels (thread_links.rs link_label).
    var label: String {
        switch (kind, incoming == true) {
        case ("duplicate", false): L10n.text("desktop.link_duplicate_of")
        case ("duplicate", true): L10n.text("desktop.link_merged_here")
        case ("mention", false): L10n.text("desktop.link_mentions")
        case ("mention", true): L10n.text("desktop.link_mentioned_in")
        default: L10n.text("desktop.link_related")
        }
    }
}

struct BoardRequest: Codable, Hashable, Identifiable {
    var number: Int
    var title: String
    var tag: String?
    var tag_auto: Bool?
    var status: String
    var author: String?
    var created: String?
    var updated: String?
    var failure: String?
    var agent: String?
    var session: String?
    var run_location: String?
    var replies: Int?
    var latest: BoardMessage?
    var messages: [BoardMessage]?
    var runs: [BoardRun]?
    var related: [BoardRelated]?

    var id: Int { number }
    var status_: RequestStatus { RequestStatus(rawValue: status) ?? .unknown }
    var updatedDate: Date { updated.flatMap(boardDate) ?? created.flatMap(boardDate) ?? .distantPast }
    var agentKind: AgentKind? { agent.flatMap(AgentKind.init(launcherID:)) }
    /// The newest run's sessions first: the one to show live output from.
    var runSessions: [String] {
        var ids = (runs ?? []).reversed().compactMap(\.session).filter { !$0.isEmpty }
        if let session, !session.isEmpty, !ids.contains(session) { ids.insert(session, at: 0) }
        return ids
    }
    /// A new request still on its way has a negative stand-in number (FEN-58).
    func reference(_ key: String) -> String { number < 0 ? "\(key)-…" : "\(key)-\(number)" }
}

enum RequestStatus: String, CaseIterable {
    case queued, working, unknown, you, review, failed, resolved

    var label: String {
        switch self {
        case .queued: L10n.text("desktop.queued")
        case .working: L10n.text("desktop.working")
        case .unknown: L10n.text("desktop.unknown")
        case .you: L10n.text("desktop.needs_you")
        case .review: L10n.text("desktop.review")
        case .failed: L10n.text("desktop.failed")
        case .resolved: L10n.text("desktop.resolved")
        }
    }
    var color: Color {
        switch self {
        case .queued, .resolved: Theme.fg3
        case .working, .unknown: Theme.working
        case .you, .review: Theme.accent
        case .failed: Theme.danger
        }
    }
    var needsYou: Bool { self == .you || self == .review || self == .failed }
    var running: Bool { self == .working || self == .unknown }
    /// The board lets a person mark these done (Store.Change).
    var canMarkDone: Bool { needsYou }
}

/// What agents learned working on a queue (an OKF concept).
struct KnowledgeConcept: Codable, Hashable, Identifiable {
    var id: String
    var type: String?
    var title: String
    var description: String?
    var tags: [String]?
    var sources: [String]?
    var status: String?
    var generated_by: String?
    var generated_at: String?
    var body: String?
}

/// Where a queued request stands, by the board's own start rule (Store.Claim
/// in internal/board/store.go, and the desktop's thread_wait.rs queue_wait):
/// the first queued request in list order starts once the queue isn't paused
/// and fewer than `limit` requests are working (or unknown).
enum QueueWait: Equatable {
    case paused
    case inLine(ahead: Int, working: Int, limit: Int)

    static func of(_ list: [BoardRequest], number: Int, limit: Int, paused: Bool) -> QueueWait {
        if paused { return .paused }
        let working = list.filter { $0.status_.running }.count
        let ahead = list.prefix { $0.number != number }.filter { $0.status_ == .queued }.count
        return .inLine(ahead: ahead, working: working, limit: max(limit, 1))
    }

    /// "Next up · 3/3 agents busy", or "Queue paused".
    var text: String {
        switch self {
        case .paused: return L10n.text("desktop.queued_paused")
        case let .inLine(ahead, working, limit):
            let place = ahead == 0 ? L10n.text("desktop.queued_next")
                : L10n.text("desktop.queued_ahead", values: ["n": String(ahead)])
            let busy = L10n.text("desktop.queued_busy", values: ["working": String(min(working, limit)), "limit": String(limit)])
            return "\(place) · \(busy)"
        }
    }
}

/// RFC 3339 with or without fractional seconds (the board writes nanoseconds).
func boardDate(_ text: String) -> Date? {
    let plain = ISO8601DateFormatter()
    if let date = plain.date(from: text) { return date }
    // Keep three fractional digits, which ISO8601DateFormatter understands.
    guard let dot = text.firstIndex(of: "."),
          let end = text[dot...].firstIndex(where: { $0 == "Z" || $0 == "+" || $0 == "-" }) else { return nil }
    let digits = text[text.index(after: dot)..<end].prefix(3)
    let trimmed = text[..<dot] + "." + digits.padding(toLength: 3, withPad: "0", startingAt: 0) + text[end...]
    let fractional = ISO8601DateFormatter()
    fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return fractional.date(from: String(trimmed))
}
