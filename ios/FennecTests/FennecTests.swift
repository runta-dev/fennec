import XCTest
@testable import Fennec

@MainActor
final class FennecTests: XCTestCase {
    func testGroupsFollowStateOrderAndSkipEmpty() {
        let store = SessionStore.demo()
        XCTAssertEqual(store.groups.map(\.0), [.needsInput, .working, .idle])
        store.sessions.removeAll { $0.state == .working }
        XCTAssertEqual(store.groups.map(\.0), [.needsInput, .idle])
    }

    func testApproveClearsRequestAndResumesWork() {
        let store = SessionStore.demo()
        store.approve("s1", forSession: false)
        XCTAssertNil(store.session("s1")?.approval)
        XCTAssertEqual(store.session("s1")?.state, .working)
    }

    func testAnsweringQuestionSendsChoice() {
        let store = SessionStore.demo()
        store.answer("s2", with: "APNs push from Runta")
        XCTAssertNil(store.session("s2")?.question)
        if case .user(let text)? = store.session("s2")?.messages.last?.kind {
            XCTAssertEqual(text, "APNs push from Runta")
        } else { XCTFail("expected a user message") }
    }

    func testSearchMatchesTitlesAndAgentReplies() {
        let store = SessionStore.demo()
        XCTAssertEqual(store.search("reconnect").sessions.map(\.id), ["s1"])
        XCTAssertEqual(store.search("stale cached port").messages.map(\.0.id), ["s5"])
    }

    func testInitials() {
        XCTAssertEqual(Profile(displayName: "Ada Lovelace", email: "ada@example.com").initials, "AL")
    }

    func testAccountSnapshotMapsToStatus() {
        XCTAssertEqual(RuntaAuth.status(for: AccountSnapshot(status: "authorized")), .signedIn)
        XCTAssertEqual(RuntaAuth.status(for: AccountSnapshot(status: "authorizing", verification_url: "https://dashboard.runta.com/authorize?x=1")),
                       .authorizing(URL(string: "https://dashboard.runta.com/authorize?x=1")))
        XCTAssertEqual(RuntaAuth.status(for: AccountSnapshot(status: "signed_out", error: "authorization_expired")),
                       .failed("The sign-in link expired. Try again."))
    }

    func testLauncherAgentIDs() {
        XCTAssertEqual(AgentKind(launcherID: "claude"), .claudeCode)
        XCTAssertEqual(AgentKind(launcherID: "grok"), .grokBuild)
        XCTAssertNil(AgentKind(launcherID: "gemini"))
    }

    func testTranscriptItemsGroupToolCalls() {
        var session = SessionStore.demo().sessions[0]
        session.messages = []
        for item in [TranscriptItem(kind: "user", text: "Fix it", tool: nil, time: 1_000),
                     TranscriptItem(kind: "tool", text: "go test", tool: "Bash", time: nil),
                     TranscriptItem(kind: "tool", text: "ls", tool: "Bash", time: nil),
                     TranscriptItem(kind: "agent", text: "Fixed.", tool: nil, time: nil)] {
            SessionStore.append(item, to: &session)
        }
        XCTAssertEqual(session.messages.count, 3)
        if case .tools(let uses) = session.messages[1].kind { XCTAssertEqual(uses.count, 2) } else { XCTFail("expected tools") }
        XCTAssertEqual(session.turnStarted, Date(timeIntervalSince1970: 1))
        XCTAssertEqual(WorkingIndicator.elapsed(from: .init(timeIntervalSince1970: 0), to: .init(timeIntervalSince1970: 65)), "1m 5s")
    }

    func testFinishedTurnsFoldToSummary() {
        let t0 = Date(timeIntervalSince1970: 0)
        let messages: [ChatMessage] = [
            .init(kind: .user("Fix it"), time: t0),
            .init(kind: .agent("Looking."), time: t0.addingTimeInterval(5)),
            .init(kind: .tools([ToolUse(name: "Bash", summary: "go test")]), time: t0.addingTimeInterval(30)),
            .init(kind: .agent("Fixed."), time: t0.addingTimeInterval(65)),
            .init(kind: .user("Thanks"), time: t0.addingTimeInterval(100)),
            .init(kind: .agent("Checking"), time: t0.addingTimeInterval(105)),
        ]
        let turns = ChatTurn.turns(messages, working: true)
        XCTAssertEqual(turns.count, 2)
        XCTAssertTrue(turns[0].finished)
        XCTAssertEqual(turns[0].work.count, 2)
        if case .agent(let text)? = turns[0].summary?.kind { XCTAssertEqual(text, "Fixed.") } else { XCTFail("summary") }
        XCTAssertEqual(turns[0].duration, 65)
        XCTAssertFalse(turns[1].finished)
        XCTAssertNil(turns[1].summary)
        XCTAssertTrue(ChatTurn.turns(messages, working: false)[1].finished)
    }

    func testMarkdownBlocks() {
        let blocks = MarkdownBlock.parse("Disk is **full**.\n\n| Path | Size |\n|---|---:|\n| ~/repos | 261 GiB |\n| ~/.tart | 187 GiB |\n\n- one\n- two\n\n1. first\n\n```\ngo test\n```")
        XCTAssertEqual(blocks.count, 5)
        XCTAssertEqual(blocks[0], .paragraph("Disk is **full**."))
        guard case .table(let header, let alignments, let rows) = blocks[1] else { return XCTFail("\(blocks[1])") }
        XCTAssertEqual(header, ["Path", "Size"])
        XCTAssertEqual(rows, [["~/repos", "261 GiB"], ["~/.tart", "187 GiB"]])
        XCTAssertEqual(alignments.count, 2)
        XCTAssertEqual(blocks[2], .list([.init(marker: "•", depth: 0, text: "one"), .init(marker: "•", depth: 0, text: "two")]))
        XCTAssertEqual(blocks[3], .list([.init(marker: "1.", depth: 0, text: "first")]))
        XCTAssertEqual(blocks[4], .code("go test"))
    }

    func testPreviewDropsMarkdown() {
        XCTAssertEqual(Session.plainLine("\n查到了，主要是**项目产物**和 `go` 缓存。\n| a | b |"), "查到了，主要是项目产物和 go 缓存。")
        XCTAssertEqual(Session.plainLine("## Done"), "Done")
    }
}
