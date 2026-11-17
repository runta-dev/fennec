import Foundation

enum AgentKind: String, CaseIterable, Identifiable, Codable {
    /// Agent ids used by internal/launcher (launcher.Supported).
    init?(launcherID: String) {
        switch launcherID {
        case "claude": self = .claudeCode
        case "codex": self = .codex
        case "grok": self = .grokBuild
        case "kimi": self = .kimiCode
        case "pi": self = .pi
        case "cursor": self = .cursor
        case "opencode": self = .openCode
        default: return nil
        }
    }

    // Display order: Codex first (the default), then Claude Code, then Pi.
    case codex, claudeCode, pi, grokBuild, kimiCode, cursor, openCode

    /// The id internal/launcher uses.
    var launcherID: String {
        switch self {
        case .claudeCode: "claude"
        case .codex: "codex"
        case .grokBuild: "grok"
        case .kimiCode: "kimi"
        case .pi: "pi"
        case .cursor: "cursor"
        case .openCode: "opencode"
        }
    }

    var id: String { rawValue }

    var displayName: String {
        switch self {
        case .claudeCode: "Claude Code"
        case .codex: "Codex"
        case .grokBuild: "Grok Build"
        case .kimiCode: "Kimi Code"
        case .pi: "Pi"
        case .cursor: "Cursor CLI"
        case .openCode: "OpenCode"
        }
    }

    /// Asset catalog image name. Monochrome marks are template images.
    var iconName: String {
        switch self {
        case .claudeCode: "agent-claudecode"
        case .codex: "agent-codex"
        case .grokBuild: "agent-xai"
        case .kimiCode: "agent-kimi"
        case .pi: "agent-pi"
        case .cursor: "agent-cursor"
        case .openCode: "agent-opencode"
        }
    }
}

/// Same state words as the Mac panel and Herdr.
enum SessionState: String, Codable, CaseIterable {
    case needsInput, working, idle

    var label: String {
        switch self {
        case .needsInput: "Needs input"
        case .working: "Working"
        case .idle: "Idle"
        }
    }
}

enum Location: String, Codable {
    case cloud, mac
}

struct ChatMessage: Identifiable, Hashable {
    enum Kind: Hashable {
        case user(String)
        case agent(String)
        /// Collapsed tool calls or thinking, e.g. "Ran 3 commands".
        case step(String)
        case changes([FileChange])
        /// Consecutive tool calls from the session log, shown as one step.
        case tools([ToolUse])
        /// The agent summarized its context; shown as a hairline divider.
        case compacted
    }
    let id = UUID()
    var kind: Kind
    /// When it was written, from the session log (nil for example data).
    var time: Date? = nil
    /// Images pasted into a user message.
    var images: [Data] = []
}

extension ChatMessage {
    var isReply: Bool { if case .agent = kind { return true } else { return false } }
    var isUser: Bool { if case .user = kind { return true } else { return false } }
    var isCompaction: Bool { if case .compacted = kind { return true } else { return false } }
}

/// One user message and everything the agent did for it.
struct ChatTurn: Identifiable {
    var id: UUID
    var prompt: ChatMessage?
    var work: [ChatMessage]
    /// The agent's closing reply, shown when the turn is folded.
    var summary: ChatMessage?
    var finished: Bool

    var duration: TimeInterval? {
        // A turn may begin before the part of the log that was read, or right
        // after a compaction, with no prompt: time it from its first step.
        let start = prompt?.time ?? work.compactMap(\.time).first
        let end = (summary ?? work.last(where: { $0.time != nil }))?.time
        guard let start, let end, end > start else { return nil }
        return end.timeIntervalSince(start)
    }

    /// Splits messages at each user message. Every turn but the last is
    /// finished; the last one is finished when the agent is not working.
    static func turns(_ messages: [ChatMessage], working: Bool) -> [ChatTurn] {
        var turns: [ChatTurn] = []
        var afterCompaction = false
        for message in messages {
            if case .compacted = message.kind {
                // Always its own row between turns, never inside one; work the
                // agent continues after it forms the next turn.
                turns.append(ChatTurn(id: message.id, prompt: message, work: [], summary: nil, finished: true))
                afterCompaction = true
            } else if case .user = message.kind {
                afterCompaction = false
                // A message sent while the agent was still working (its turn has
                // no reply yet) steers that turn, as in Codex; it doesn't start one.
                if let last = turns.last?.work.last, !last.isReply {
                    turns[turns.count - 1].work.append(message)
                } else {
                    turns.append(ChatTurn(id: message.id, prompt: message, work: [], summary: nil, finished: true))
                }
            } else if turns.isEmpty || afterCompaction {
                turns.append(ChatTurn(id: message.id, prompt: nil, work: [message], summary: nil, finished: true))
                afterCompaction = false
            } else {
                turns[turns.count - 1].work.append(message)
            }
        }
        if working, !turns.isEmpty { turns[turns.count - 1].finished = false }
        for index in turns.indices where turns[index].finished {
            if let last = turns[index].work.last, case .agent = last.kind {
                turns[index].summary = turns[index].work.removeLast()
            }
        }
        return turns
    }
}

/// One step from internal/transcript: a verb (ran, read, edited, created,
/// searched, viewed, fetched, waited, typed, delegated, called) and its target.
struct ToolUse: Hashable {
    var name: String
    var summary: String

    var verb: String { name.prefix(1).uppercased() + name.dropFirst() }

    /// SF Symbol for the verb, in the expanded step list.
    var symbol: String {
        switch name {
        case "ran": "terminal"
        case "read": "doc.text"
        case "viewed": "photo"
        case "created": "doc.badge.plus"
        case "edited": "pencil"
        case "searched": "magnifyingglass"
        case "fetched": "globe"
        case "waited": "hourglass"
        case "typed": "keyboard"
        case "delegated": "person.2"
        default: "wrench.and.screwdriver"
        }
    }

    /// Codex-style header for a run of steps: "Explored 3 files, 1 search, ran 2 commands".
    static func headline(_ uses: [ToolUse]) -> String {
        func count(_ verbs: Set<String>) -> Int { uses.filter { verbs.contains($0.name) }.count }
        func plural(_ n: Int, _ one: String, _ many: String) -> String { n == 1 ? "1 \(one)" : "\(n) \(many)" }
        var parts: [String] = []
        let files = count(["read", "viewed"]), searches = count(["searched", "fetched"])
        var explored: [String] = []
        if files > 0 { explored.append(plural(files, "file", "files")) }
        if searches > 0 { explored.append(plural(searches, "search", "searches")) }
        if !explored.isEmpty { parts.append("explored " + explored.joined(separator: ", ")) }
        let ran = count(["ran"])
        if ran > 0 { parts.append(uses.count == 1 ? "ran " + uses[0].summary : "ran " + plural(ran, "command", "commands")) }
        let edited = count(["edited", "created"])
        if edited > 0 { parts.append("edited " + plural(edited, "file", "files")) }
        let other = uses.count - files - searches - ran - edited
        if other > 0 { parts.append("used " + plural(other, "tool", "tools")) }
        let text = parts.joined(separator: ", ")
        return text.prefix(1).uppercased() + text.dropFirst()
    }
}

struct FileChange: Hashable {
    var path: String
    var added: Int
    var removed: Int
}

struct ApprovalRequest: Hashable {
    var command: String
    var directory: String
    var location: Location
}

struct AgentQuestion: Hashable {
    struct Option: Hashable, Identifiable {
        var id: Int
        var title: String
        var detail: String
    }
    var prompt: String
    var options: [Option]
}

struct Session: Identifiable, Hashable {
    let id: String
    var title: String
    var agent: AgentKind
    var model: String
    var project: String
    var location: Location
    var state: SessionState
    var updatedAt: Date
    var pinned = false
    var messages: [ChatMessage]
    var approval: ApprovalRequest?
    var question: AgentQuestion?
    /// Herdr terminal address for live sessions from the cloud computer.
    var address: String?
    /// Herdr's line about what the agent is doing now, and when it changed.
    var activity: String?
    var statusSince: Date?
    /// When the current turn began: the last message the user sent.
    var turnStarted: Date?
    /// Read position in the agent's session log; -1 before the first read.
    var transcriptOffset: Int64 = -1
    /// Which log the offset belongs to, and the items read from it (for the cache).
    var transcriptPath: String?
    var transcriptVersion: Int?
    var transcriptItems: [TranscriptItem] = []
    var transcriptLoaded = false
    var transcriptError: String?

    var preview: String {
        if address != nil {
            // Herdr reports a line only while the agent works; otherwise the
            // latest update is the end of the agent's own session log.
            if state == .working, let activity, !activity.isEmpty { return activity }
            for message in messages.reversed() {
                if case .agent(let text) = message.kind { return Self.plainLine(text) }
            }
            for message in messages.reversed() {
                if case .user(let text) = message.kind { return text }
            }
            return ""
        }
        if let approval { return "Approve: \(approval.command)" }
        if let question { return question.prompt }
        for message in messages.reversed() {
            switch message.kind {
            case .agent(let text), .user(let text): return text
            default: continue
            }
        }
        return ""
    }
}

extension Session {
    /// A reply's first line as plain text for the list: Markdown marks
    /// (bold, code, headings, bullets, table pipes) dropped.
    static func plainLine(_ markdown: String) -> String {
        let line = markdown.split(separator: "\n").map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? ""
        var text = line.replacingOccurrences(of: #"^(#{1,6}|[-*+]|>)\s+"#, with: "", options: .regularExpression)
        text = text.replacingOccurrences(of: "|", with: " ").trimmingCharacters(in: .whitespaces)
        let parsed = try? AttributedString(markdown: text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))
        return parsed.map { String($0.characters) } ?? text
    }
}

struct Profile: Equatable {
    var displayName: String
    var email: String

    var initials: String {
        let source = displayName.isEmpty ? email : displayName
        let parts = source.split(whereSeparator: { $0 == " " || $0 == "@" || $0 == "." })
        return parts.prefix(2).compactMap { $0.first.map(String.init) }.joined().uppercased()
    }
}
