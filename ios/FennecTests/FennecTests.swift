import XCTest
@testable import Fennec

@MainActor
final class FennecTests: XCTestCase {
    func testNewSessionStartsWithoutAnInitialPrompt() async throws {
        let store = SessionStore.demo()
        let id = try await store.launch(agent: .codex)
        XCTAssertEqual(store.session(id)?.agent, .codex)
        XCTAssertEqual(store.session(id)?.messages.count, 0)
        XCTAssertEqual(store.session(id)?.state, .idle)
    }

    func testLanguageSelectionPersistsAndFollowsSupportedSystemLanguages() {
        let suite = "FennecLocalizationTests." + UUID().uuidString
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let preferences = LanguagePreferences(defaults: defaults)
        XCTAssertEqual(preferences.selection, "system")
        preferences.selection = "zh-CN"
        XCTAssertEqual(LanguagePreferences(defaults: defaults).selection, "zh-CN")
        XCTAssertEqual(L10n.resolveLanguage("system", preferred: ["zh-Hans-US"]), "zh-CN")
        XCTAssertEqual(L10n.resolveLanguage("system", preferred: ["de", "en-GB"]), "en-US")
        XCTAssertEqual(L10n.resolveLanguage("en-US", preferred: ["zh-Hans"]), "en-US")
        XCTAssertEqual(L10n.text("ios.system_settings", language: "zh-CN"), "系统设置")
        XCTAssertEqual(L10n.text("ios.message_agent", language: "zh-CN", values: ["agent": "Codex"]), "发送消息给 Codex")
        XCTAssertEqual(L10n.text("ios.system_settings", language: "unsupported"), "System settings")
    }

    func testMacOnlineXMLBecomesATimestampedTimelineNotice() {
        var session = SessionStore.demo().sessions[0]
        session.messages = []
        let xml = "<fennec-event type=\"mac-online\" device=\"aabbccdd\" generation=\"abc\">Mac is back online.</fennec-event>"
        SessionStore.append(.init(kind: "user", text: xml, time: 123000), to: &session)
        XCTAssertEqual(session.messages.count, 1)
        XCTAssertEqual(session.messages[0].kind, .macOnline)
        XCTAssertEqual(session.messages[0].time, Date(timeIntervalSince1970: 123))
        XCTAssertFalse(session.messages[0].isUser)
        let turns = ChatTurn.turns(session.messages, working: true)
        XCTAssertTrue(turns[0].finished)
        XCTAssertTrue(turns[0].work.isEmpty)
        SessionStore.append(.init(kind: "user", text: "Explain " + xml), to: &session)
        XCTAssertTrue(session.messages[1].isUser)
    }

    func testPendingSendSurvivesStaleTranscriptAndConfirmsNewEcho() {
        let store = SessionStore.demo()
        let now = Date.now
        let old = TranscriptItem(kind: "user", text: "Continue", time: Int64(now.addingTimeInterval(-60).timeIntervalSince1970 * 1000))
        let fresh = TranscriptItem(kind: "user", text: "Continue", time: Int64(now.timeIntervalSince1970 * 1000))
        store.pendingSends["s1"] = .init(message: .init(kind: .user("Continue"), time: now), previousMatches: 1)
        store.reconcilePendingSend("s1", items: [old])
        XCTAssertNotNil(store.pendingSends["s1"])
        store.reconcilePendingSend("s1", items: [old, fresh])
        XCTAssertNil(store.pendingSends["s1"])
    }

    func testHistoricalResetDoesNotConfirmPendingSend() {
        let store = SessionStore.demo()
        store.pendingSends["s1"] = .init(message: .init(kind: .user("Continue"), time: .now), previousMatches: 0)
        store.reconcilePendingSend("s1", items: [.init(kind: "user", text: "Continue", time: 1)])
        XCTAssertNotNil(store.pendingSends["s1"])
    }

    func testPendingSendConfirmsEchoPastLogEndDespiteClockSkew() {
        // The cloud computer's clock runs 60 s behind this phone: the echo's
        // timestamp predates the send, but it sits past where the log ended.
        let store = SessionStore.demo()
        var session = store.session("s1")!
        session.transcriptPath = "/log.jsonl"
        session.transcriptOffset = 1000
        session.transcriptLoaded = true
        session.transcriptItems = [.init(id: 100 * 16, kind: "user", text: "Who are you", time: 1)]
        let skewed = Int64(Date.now.addingTimeInterval(-60).timeIntervalSince1970 * 1000)
        store.pendingSends["s1"] = .init(message: .init(kind: .user("Who are you"), time: .now), session: session)
        XCTAssertEqual(store.pendingSends["s1"]?.previousMatches, 1)
        let echo = TranscriptItem(id: 1200 * 16, kind: "user", text: "Who are you", time: skewed)
        store.reconcilePendingSend("s1", items: session.transcriptItems + [echo], path: "/log.jsonl")
        XCTAssertNil(store.pendingSends["s1"])
    }

    func testPendingSendIgnoresOldEchoBeforeLogEnd() {
        let store = SessionStore.demo()
        var session = store.session("s1")!
        session.transcriptPath = "/log.jsonl"
        session.transcriptOffset = 1000
        session.transcriptLoaded = true
        store.pendingSends["s1"] = .init(message: .init(kind: .user("Continue"), time: .now), session: session)
        // A reset read brings back an older identical prompt from before the send.
        store.reconcilePendingSend("s1", items: [.init(id: 500 * 16, kind: "user", text: "Continue", time: 1)], path: "/log.jsonl")
        XCTAssertNotNil(store.pendingSends["s1"])
    }

    func testFirstPromptConfirmsWhenLogIsCreated() {
        let store = SessionStore.demo()
        var session = store.session("s1")!
        session.transcriptPath = nil
        session.transcriptItems = []
        session.transcriptLoaded = true
        store.pendingSends["s1"] = .init(message: .init(kind: .user("Who are you"), time: .now), session: session)
        let echo = TranscriptItem(id: 0, kind: "user", text: "Who are you", time: 1)
        store.reconcilePendingSend("s1", items: [echo], path: nil)
        XCTAssertNotNil(store.pendingSends["s1"])
        store.reconcilePendingSend("s1", items: [echo], path: "/rollout.jsonl")
        XCTAssertNil(store.pendingSends["s1"])
    }

    func testPendingSendMatchesPromptWithCollapsedSpaces() {
        XCTAssertEqual(SessionStore.promptKey("  a   b \n\n\n c  "), "a b\n\nc")
        let store = SessionStore.demo()
        let now = Date.now
        store.pendingSends["s1"] = .init(message: .init(kind: .user("fix   this"), time: now), previousMatches: 0)
        store.reconcilePendingSend("s1", items: [.init(kind: "user", text: "fix this", time: Int64(now.timeIntervalSince1970 * 1000))])
        XCTAssertNil(store.pendingSends["s1"])
    }

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

    func testReviewEmailMatchesOnlyAppReviewAddress() {
        XCTAssertTrue(ReviewDemo.matches(" AppReview@Runta.com\n"))
        XCTAssertFalse(ReviewDemo.matches("appreview@runta.co"))
        XCTAssertFalse(ReviewDemo.matches("someone@runta.com"))
    }

    func testDemoSendTitlesSessionAndStopEndsReply() {
        let store = SessionStore.demo()
        let id = store.create(agent: .codex, model: "", project: "", location: .cloud, prompt: "").id
        store.send("Fix duplicate webhook deliveries\nThen run the tests", to: id)
        XCTAssertEqual(store.session(id)?.title, "Fix duplicate webhook deliveries")
        XCTAssertEqual(store.session(id)?.state, .working)
        XCTAssertNotNil(store.demoReplies[id])
        store.stopDemoTurn(id)
        XCTAssertEqual(store.session(id)?.state, .idle)
        XCTAssertNil(store.demoReplies[id])
    }

    func testDemoReplyPlaysRecordedStepsAndFinishes() async throws {
        let store = SessionStore.demo()
        let id = store.create(agent: .pi, model: "", project: "", location: .cloud, prompt: "").id
        store.send("Add retry tests", to: id)
        let deadline = Date.now.addingTimeInterval(20)
        while store.session(id)?.state == .working, Date.now < deadline { try await Task.sleep(for: .milliseconds(200)) }
        let messages = try XCTUnwrap(store.session(id)?.messages)
        XCTAssertEqual(store.session(id)?.state, .idle)
        XCTAssertTrue(messages.contains { if case .tools(let uses) = $0.kind { uses.count > 1 } else { false } })
        if case .agent(let text)? = messages.last?.kind { XCTAssertTrue(text.contains("ok relay")) } else { XCTFail("expected the closing reply") }
    }

    func testLinkCardsTakePublicHTTPSLinksOutsideCode() {
        let text = """
        See https://fennec.runta.com and [docs](https://fennec.runta.com/) again.
        `curl https://example.com/inline` and http://plain.example.com
        ```
        open https://example.com/in-code
        ```
        Local: https://localhost:3000 https://192.168.1.4/x https://10.0.0.2 https://mac.local/
        Port: https://8080-7f3c2a91-5d4e.runta.dev/ File: fennec-preview:pv_123
        Third: https://github.com/runta-dev/fennec https://apple.com
        """
        XCTAssertEqual(LinkCards.urls(in: text).map(\.absoluteString),
                       ["https://fennec.runta.com", "https://github.com/runta-dev/fennec"])
        XCTAssertTrue(LinkCards.previewable(URL(string: "https://8.8.8.8/")!))
        XCTAssertFalse(LinkCards.previewable(URL(string: "https://172.20.1.1/")!))
    }

    func testConnectionIssueHidesRawErrorsAndReconnectsQuietlyAtFirst() {
        let start = Date.now
        let dropped = ConnectionIssue(message: "[network] read tcp 192.168.1.4:52311: connection reset by peer", since: start)
        XCTAssertEqual(dropped.kind, .network)
        XCTAssertEqual(dropped.detail, "read tcp 192.168.1.4:52311: connection reset by peer")
        XCTAssertFalse(dropped.title.contains("tcp"))
        XCTAssertTrue(dropped.reconnecting(at: start.addingTimeInterval(5)))
        XCTAssertTrue(dropped.reconnecting(at: start.addingTimeInterval(3600)))
        XCTAssertEqual(ConnectionIssue(message: "[signed_out] sign in first").kind, .signedOut)
        XCTAssertEqual(ConnectionIssue(message: "[cloud_stopped] the cloud computer is stopped").kind, .cloudStopped)
        XCTAssertEqual(ConnectionIssue(message: "[no_computer] none").kind, .noComputer)
        let other = ConnectionIssue(message: "session not found")
        XCTAssertEqual(other.kind, .other)
        XCTAssertFalse(other.reconnecting(at: .now))
        XCTAssertEqual(other.detail, "session not found")
    }

    func testCachedListAppliesAndKeepsKnownSessions() throws {
        let json = #"[{"address":"t1","session_id":"cone-leaf","agent":"codex","title":"Fix reconnect","status":"Working","status_ms":1700000000000},{"address":"t2","session_id":"x","agent":"unknown-agent","title":"Skip","status":"Idle"}]"#
        let rows = try JSONDecoder().decode([SessionSummary].self, from: Data(json.utf8))
        // What the cache file holds survives a round trip.
        let again = try JSONDecoder().decode([SessionSummary].self, from: JSONEncoder().encode(rows))
        let store = SessionStore(sessions: [])
        store.apply(again)
        XCTAssertEqual(store.sessions.map(\.id), ["cone-leaf"])
        XCTAssertEqual(store.session("cone-leaf")?.state, .working)
        XCTAssertEqual(store.session("cone-leaf")?.updatedAt, Date(timeIntervalSince1970: 1_700_000_000))
        store.update("cone-leaf") { $0.messages.append(.init(kind: .agent("kept"))) }
        let previous = store.apply(rows)
        XCTAssertNotNil(previous["cone-leaf"])
        XCTAssertEqual(store.session("cone-leaf")?.messages.count, 1)
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
        XCTAssertEqual(AgentKind(launcherID: "agy"), .antigravity)
        XCTAssertEqual(AgentKind.antigravity.launcherID, "agy")
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

    /// With stop records, a message sent before the agent stopped steers the
    /// turn; one sent after starts the next. Replies it stopped on stay
    /// visible, and the duration is the one it reports.
    func testTurnsFollowTheAgentsStopRecords() {
        let t0 = Date(timeIntervalSince1970: 0)
        let messages: [ChatMessage] = [
            .init(kind: .user("Check the disk"), time: t0),
            .init(kind: .tools([ToolUse(name: "ran", summary: "df -h")]), time: t0.addingTimeInterval(5)),
            .init(kind: .agent("97% used."), time: t0.addingTimeInterval(9)),
            .init(kind: .user("which folders?"), time: t0.addingTimeInterval(10)), // sent while it worked
            .init(kind: .tools([ToolUse(name: "ran", summary: "du -sh ~/*")]), time: t0.addingTimeInterval(20)),
            .init(kind: .agent("~/repos is 261 GiB."), time: t0.addingTimeInterval(40)),
            .init(kind: .end(duration: 42, stopped: false), time: t0.addingTimeInterval(40)),
            .init(kind: .user("thanks"), time: t0.addingTimeInterval(60)),
            .init(kind: .agent("Stopping."), time: t0.addingTimeInterval(61)),
            .init(kind: .end(duration: nil, stopped: true), time: t0.addingTimeInterval(62)),
            .init(kind: .user("go on"), time: t0.addingTimeInterval(70)),
        ]
        let turns = ChatTurn.turns(messages, working: true)
        XCTAssertEqual(turns.count, 3)
        XCTAssertTrue(turns[0].finished)
        XCTAssertEqual(turns[0].duration, 42)
        XCTAssertTrue(turns[0].work.contains { if case .user("which folders?") = $0.kind { return true } else { return false } })
        if case .agent(let text)? = turns[0].summary?.kind { XCTAssertEqual(text, "~/repos is 261 GiB.") } else { XCTFail("summary") }
        XCTAssertTrue(turns[1].stopped)
        XCTAssertFalse(turns[2].finished)
        // A reply the agent did not stop on is folded; one it stopped on is kept.
        let early = ChatTurn.turns([
            .init(kind: .user("a"), time: t0), .init(kind: .agent("first answer"), time: t0),
            .init(kind: .end(duration: 1, stopped: false), time: t0),
            .init(kind: .tools([ToolUse(name: "ran", summary: "ls")]), time: t0), // woken by a background task
            .init(kind: .agent("final"), time: t0), .init(kind: .end(duration: 2, stopped: false), time: t0),
        ], working: false)
        XCTAssertEqual(early.count, 1)
        XCTAssertEqual(early[0].closingReplies.count, 2)
        XCTAssertEqual(early[0].duration, 3)
    }

    /// Claude Code: each typed message starts a turn even when the previous
    /// stop was not recorded; messages it marked as sent mid-turn steer.
    func testClaudeCodeTurnsFollowPrompts() {
        let t0 = Date(timeIntervalSince1970: 0)
        let messages: [ChatMessage] = [
            .init(kind: .user("one"), time: t0),
            .init(kind: .tools([ToolUse(name: "ran", summary: "ls")]), time: t0),
            .init(kind: .user("also this"), time: t0, steer: true),
            .init(kind: .agent("done one"), time: t0),
            .init(kind: .user("two"), time: t0),
            .init(kind: .agent("done two"), time: t0),
            .init(kind: .end(duration: 5, stopped: false), time: t0),
            .init(kind: .user("three"), time: t0),
        ]
        let turns = ChatTurn.turns(messages, working: true, byPrompt: true)
        XCTAssertEqual(turns.count, 3)
        XCTAssertTrue(turns[0].work.contains { if case .user("also this") = $0.kind { return true } else { return false } })
        if case .agent(let text)? = turns[0].summary?.kind { XCTAssertEqual(text, "done one") } else { XCTFail("summary") }
        XCTAssertEqual(turns[1].duration, 5)
        XCTAssertFalse(turns[2].finished)
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
        XCTAssertEqual(Session.plainLine("隔离。 \u{E200}cite\u{E202}turn538016search2\u{E201}"), "隔离。")
    }

    @MainActor func testCodexMarkersAndCJKBold() {
        // FEN-85: Codex citation markers showed as boxes, and bold ending in 。 kept its asterisks.
        XCTAssertEqual(MarkdownBlock.stripAgentMarkers("原因。 \u{E200}cite\u{E202}turn538016search2\u{E201}\n下一行"), "原因。\n下一行")
        XCTAssertEqual(MarkdownBlock.stripAgentMarkers("由 \u{E200}entity\u{E202}[\"company\",\"OpenAI\"]\u{E201} 提供"), "由 OpenAI 提供")
        XCTAssertEqual(MarkdownBlock.parse("a \u{E200}cite\u{E202}turn1search2\u{E201}"), [.paragraph("a")])
        for source in ["**不是每一笔数据都要转发。**它们负责", "**现在怎么办：**追问", "中文**（括号）**后", "**done.** next"] {
            let value = MarkdownBlock.parseInline(source)
            let text = String(value.characters)
            XCTAssertFalse(text.contains("*"), text)
            XCTAssertTrue(value.runs.contains { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true }, source)
        }
        XCTAssertEqual(String(MarkdownBlock.parseInline("**现在怎么办：**追问").characters), "现在怎么办：追问")
        XCTAssertEqual(String(MarkdownBlock.parseInline("`a*：b` 和 2*3：4").characters), "a*：b 和 2*3：4")
    }
}
