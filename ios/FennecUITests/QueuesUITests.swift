import XCTest

/// Queues (THI-124) in the demo: home is the queue (FEN-108), with a queued
/// thread's waiting card and a reviewed request.
/// Screenshots go to /tmp/fennec-queues for review.
final class QueuesUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        return app
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        let directory = URL(fileURLWithPath: "/tmp/fennec-queues")
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try? screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"))
    }

    /// FEN-108: home is the queue, with no tab bar, no session list and no
    /// new-session menu; search finds requests.
    func testHomeIsTheQueue() {
        let app = launch()
        XCTAssertTrue(app.descendants(matching: .any)["request-117"].firstMatch.waitForExistence(timeout: 10))
        XCTAssertFalse(app.tabBars.firstMatch.exists, "home shows a tab bar")
        XCTAssertFalse(app.buttons["new-session-menu"].exists, "home offers a new session")
        XCTAssertFalse(app.switches["show-queues"].exists)
        shot(app, "18-home")
        app.buttons["search-requests"].tap()
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.typeText("117")
        let hit = app.descendants(matching: .any)["request-117"].firstMatch
        XCTAssertTrue(hit.waitForExistence(timeout: 5))
        shot(app, "24-search")
        hit.tap()
        XCTAssertTrue(app.buttons["request-menu"].waitForExistence(timeout: 5))
    }

    func testQueuedThreadShowsItsPlaceInLine() {
        let app = launch()
        let queued = app.descendants(matching: .any)["request-124"].firstMatch
        XCTAssertTrue(queued.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Next up · 3/3 agents busy"].exists)
        shot(app, "18-queues-tab")

        app.buttons["queue-menu"].tap()
        XCTAssertTrue(app.buttons["Pause queue"].waitForExistence(timeout: 3))
        shot(app, "19-queue-menu")
        app.buttons["Pause queue"].tap()
        XCTAssertTrue(app.buttons["queue-resume"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.staticTexts["Queue paused"].exists)
        shot(app, "20-queue-paused")
        app.buttons["queue-resume"].tap()

        queued.tap()
        let wait = app.descendants(matching: .any)["thread-wait"].firstMatch
        XCTAssertTrue(wait.waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Waiting for an agent"].exists)
        XCTAssertTrue(app.staticTexts["Next up · 3/3 agents busy"].exists)
        shot(app, "25-thread-queued")
        app.buttons["request-menu"].tap()
        XCTAssertTrue(app.buttons["Run next"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["Mark done"].exists, "a queued request offers Mark done")
        shot(app, "30-thread-menu")
    }

    func testReviewedRequestOffersMarkDone() {
        let app = launch()
        let reviewed = app.descendants(matching: .any)["request-117"].firstMatch
        XCTAssertTrue(reviewed.waitForExistence(timeout: 10))
        reviewed.tap()
        XCTAssertTrue(app.buttons["Mark done"].waitForExistence(timeout: 5))
        shot(app, "28-thread-review")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        // + picks the agent, then opens the request's blank page (FEN-110).
        app.buttons["new-request"].tap()
        let codex = app.buttons["new-request-agent-codex"]
        XCTAssertTrue(codex.waitForExistence(timeout: 5))
        shot(app, "32-new-request-agents")
        codex.tap()
        XCTAssertTrue(app.buttons["send-request"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.descendants(matching: .any)["empty-conversation"].exists)
        // The page can attach images (FEN-64).
        XCTAssertTrue(app.buttons["new-request-image"].isHittable)
        shot(app, "33-new-request")
        let text = app.textViews["new-request-text"]
        text.tap()
        text.typeText("Check the login screen")
        app.buttons["send-request"].tap()
        // Send opens the new thread in place of the blank page.
        XCTAssertTrue(app.staticTexts["Check the login screen"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["send-request"].exists)
    }
}
