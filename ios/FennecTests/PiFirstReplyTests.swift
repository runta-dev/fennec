import XCTest
@testable import Fennec
import FennecCore

/// A cloud computer scripted from what the Go core returns for a new Pi 1.5
/// session (THI-73): no log until the first prompt, then a log that holds
/// only the prompt, then the reply appended. Other sessions are working.
private final class ScriptedCore: FennecmobileSessions, @unchecked Sendable {
    let lock = NSLock()
    var chunks: [String: [String]] = [:]
    var reads: [String] = []
    let rows: String

    init(rows: String) {
        self.rows = rows
        super.init(ref: NSObject())
    }

    override func launch(_ agent: String?, error: NSErrorPointer) -> String {
        #"{"address":"t31b","labels":{"fennec.agent":"pi","fennec.session":"pear-anchor"}}"#
    }
    override func list(_ error: NSErrorPointer) -> String { rows }
    override func listTiming() -> String { "{}" }
    override func prompt(_ address: String?, sessionID: String?, error: NSErrorPointer) -> String { "{}" }
    override func send(_ address: String?, sessionID: String?, text: String?, error: NSErrorPointer) -> String {
        #"{"receipt":{"started":true,"status":"idle","turn":0}}"#
    }
    override func transcript(_ address: String?, sessionID: String?, offset: Int64, error: NSErrorPointer) -> String {
        lock.lock(); defer { lock.unlock() }
        let id = sessionID ?? ""
        reads.append(id)
        if var queue = chunks[id], !queue.isEmpty {
            let next = queue.removeFirst()
            chunks[id] = queue
            return next
        }
        return #"{"items":[],"offset":\#(offset),"reset":false}"#
    }
    func takeReads() -> [String] { lock.lock(); defer { lock.unlock() }; let r = reads; reads = []; return r }
}

@MainActor
final class PiFirstReplyTests: XCTestCase {
    static let id = "pear-anchor"
    static let path = "/root/.pi/agent/sessions/--root-.fen-pear-anchor--/2026-11-25T02-59-00-000Z_x.jsonl"
    static let noLog = #"{"items":[],"offset":-1,"reset":false}"#
    static let prompt = #"{"items":[{"id":100096,"kind":"user","text":"hello","time":1795574940104}],"offset":6256,"path":"\#(path)","reset":true,"version":6}"#
    static let nothingNew = #"{"items":[],"offset":6256,"path":"\#(path)","reset":false,"version":6}"#
    static let reply = #"{"items":[{"id":100064,"kind":"agent","text":"Hi! What can I help you with?","time":1795574942968},{"id":100065,"kind":"end","text":"","time":1795574942968}],"offset":7101,"path":"\#(path)","reset":false,"version":6}"#
    // Herdr's status time on a cloud clock behind the phone: before the send.
    static let rows = #"""
    [{"address":"t31b","session_id":"pear-anchor","agent":"pi","title":"π - pear-anchor","status":"Idle","status_ms":1000},
     {"address":"t305","session_id":"grain-jelly","agent":"claude","title":"a","status":"Working","status_ms":1000},
     {"address":"t310","session_id":"button-city","agent":"claude","title":"b","status":"Working","status_ms":1000},
     {"address":"t313","session_id":"cone-leaf","agent":"codex","title":"c","status":"Working","status_ms":1000}]
    """#

    /// A chat cached by an earlier run would hold "hello" already.
    override func setUp() {
        try? FileManager.default.removeItem(at: URL.cachesDirectory.appending(path: "transcripts/\(Self.id).json"))
    }

    private func hasReply(_ store: SessionStore) -> Bool {
        store.session(Self.id)?.messages.contains(where: \.isReply) ?? false
    }

    /// Launch from the phone, send the first message, and poll as the chat
    /// page does.
    private func sendFirstMessage(_ core: ScriptedCore, _ store: SessionStore) async throws {
        _ = try await store.launch(agent: .pi)
        core.chunks[Self.id] = [Self.noLog]
        _ = await store.pollChat(Self.id)
        core.chunks[Self.id] = [Self.prompt]
        store.send("hello", to: Self.id)
        for _ in 0..<100 where store.pendingSends[Self.id] != nil { try await Task.sleep(for: .milliseconds(10)) }
        XCTAssertNil(store.pendingSends[Self.id], "the prompt-only log confirms the send")
    }

    func testFirstPiReplyShows() async throws {
        let core = ScriptedCore(rows: Self.rows)
        let store = SessionStore(sessions: [], core: core)
        try await sendFirstMessage(core, store)
        core.chunks[Self.id] = [Self.nothingNew, Self.reply]
        var waits: [Duration] = []
        for _ in 0..<3 { waits.append(await store.pollChat(Self.id)) }
        XCTAssertTrue(hasReply(store))
        // Fast while the reply is awaited (the list never says Working), slow after.
        XCTAssertEqual(waits.first, .seconds(1.5))
        XCTAssertEqual(waits.last, .seconds(4))
        // The turn ended in the log: no "Working" left, though Herdr's status
        // time (cloud clock) is older than the send.
        XCTAssertFalse(store.workingOrStarting(try XCTUnwrap(store.session(Self.id))))
    }

    /// An open chat reads its own log first and leaves the other sessions'
    /// logs to the home list, so a reply never waits behind them.
    func testChatReadsOnlyItsOwnLog() async throws {
        let core = ScriptedCore(rows: Self.rows)
        let store = SessionStore(sessions: [], core: core)
        try await sendFirstMessage(core, store)
        _ = core.takeReads()
        core.chunks[Self.id] = [Self.reply]
        _ = await store.pollChat(Self.id)
        XCTAssertEqual(core.takeReads(), [Self.id])
        XCTAssertTrue(hasReply(store))
        // Back on the home list, the sessions that changed meanwhile are read.
        await store.refresh()
        XCTAssertEqual(Set(core.takeReads()), ["grain-jelly", "button-city", "cone-leaf"])
        XCTAssertTrue(store.transcriptsBehind.isEmpty)
    }

    func testEndedAfterLastPrompt() {
        let user = ChatMessage(kind: .user("hi")), end = ChatMessage(kind: .end(duration: nil, stopped: false))
        XCTAssertTrue(SessionStore.endedAfterLastPrompt([user, .init(kind: .agent("yo")), end]))
        XCTAssertFalse(SessionStore.endedAfterLastPrompt([end, user]))
        XCTAssertFalse(SessionStore.endedAfterLastPrompt([user]))
    }
}
