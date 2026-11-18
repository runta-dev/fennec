import XCTest

/// A conversation with a long history opens without a stall, and scrolling
/// up still reaches its first turn.
final class ChatOpenUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testLongConversationOpensQuicklyAndScrollsBackToStart() {
        let app = XCUIApplication()
        app.launchArguments += ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        let title = app.staticTexts["Long conversation"]
        for _ in 0..<5 { if title.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        let last = app.staticTexts.containing(NSPredicate(format: "label BEGINSWITH 'Answer 120.'")).firstMatch
        title.tap()
        XCTAssertTrue(last.waitForExistence(timeout: 5))
        // Only turns on screen are built when it opens.
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label BEGINSWITH 'Answer 60.'")).firstMatch.exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Long-Conversation-Open"
        shot.lifetime = .keepAlways
        add(shot)
        // The oldest turns are drawn as you reach the top.
        let first = app.staticTexts.containing(NSPredicate(format: "label BEGINSWITH 'Answer 1.'")).firstMatch
        for _ in 0..<80 { if first.exists { break }; app.swipeDown(velocity: .fast) }
        XCTAssertTrue(first.waitForExistence(timeout: 5), "never reached the first turn")
    }
}
