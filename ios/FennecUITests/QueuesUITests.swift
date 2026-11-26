import XCTest

/// Queues (THI-124) in the demo: off by default, turned on in Account, then
/// the Queues tab, a queued thread's waiting card and a reviewed request.
/// Screenshots go to /tmp/fennec-queues for review.
final class QueuesUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    private func launch(queues: Bool) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US",
                               "-queuesEnabled", queues ? "YES" : "NO"]
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

    func testQueuesAreOffUntilTurnedOnInAccount() {
        let app = launch(queues: false)
        XCTAssertTrue(app.buttons["Account"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.tabBars.firstMatch.exists, "a tab bar shows while Queues are off")
        app.buttons["Account"].tap()
        let toggle = app.switches["show-queues"]
        for _ in 0..<4 { if toggle.exists && toggle.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        XCTAssertEqual(toggle.value as? String, "0")
        shot(app, "17-account-queues-off")
    }

    func testQueuedThreadShowsItsPlaceInLine() {
        let app = launch(queues: true)
        let queuesTab = app.tabBars.buttons["Queues"]
        XCTAssertTrue(queuesTab.waitForExistence(timeout: 10))
        shot(app, "18b-sessions-tab")
        queuesTab.tap()
        sleep(2)
        shot(app, "18-queues-tab-first")
        let queued = app.descendants(matching: .any)["request-124"].firstMatch
        XCTAssertTrue(queued.waitForExistence(timeout: 5))
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
        XCTAssertFalse(app.tabBars.firstMatch.isHittable, "the tab bar stays over a thread")
        shot(app, "25-thread-queued")
        app.buttons["request-menu"].tap()
        XCTAssertTrue(app.buttons["Run next"].waitForExistence(timeout: 3))
        XCTAssertFalse(app.buttons["Mark done"].exists, "a queued request offers Mark done")
        shot(app, "30-thread-menu")
    }

    func testReviewedRequestOffersMarkDone() {
        let app = launch(queues: true)
        app.tabBars.buttons["Queues"].tap()
        let reviewed = app.descendants(matching: .any)["request-117"].firstMatch
        XCTAssertTrue(reviewed.waitForExistence(timeout: 10))
        reviewed.tap()
        XCTAssertTrue(app.buttons["Mark done"].waitForExistence(timeout: 5))
        shot(app, "28-thread-review")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        app.buttons["new-request"].tap()
        XCTAssertTrue(app.buttons["send-request"].waitForExistence(timeout: 5))
        // The sheet can attach images (FEN-64).
        XCTAssertTrue(app.buttons["new-request-image"].isHittable)
        shot(app, "33-new-request")
    }
}
