import XCTest

/// The conversation never scrolls sideways: wide tables, long code and
/// unbroken URLs stay inside the screen (code scrolls within its own block).
final class ChatWidthUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testWideContentDoesNotScrollConversationSideways() {
        let app = XCUIApplication()
        app.launchArguments += ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        let title = app.staticTexts["Compare SSH latency by region"]
        for _ in 0..<4 { if title.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        title.tap()

        let window = app.windows.firstMatch.frame
        let intro = app.staticTexts.containing(NSPredicate(format: "label BEGINSWITH 'Measured 200 round trips'")).firstMatch
        XCTAssertTrue(intro.waitForExistence(timeout: 5))
        let start = intro.frame
        let prompt = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'Compare round-trip latency'")).firstMatch
        XCTAssertTrue(prompt.exists)

        // Drag leftwards across the reply and the prompt. (Rightward drags are
        // the system's swipe back, which moves the page on purpose.)
        for element in [intro, prompt] {
            let from = element.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.5))
            from.press(forDuration: 0.05, thenDragTo: element.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.5)))
        }
        Thread.sleep(forTimeInterval: 1)
        XCTAssertEqual(intro.frame.minX, start.minX, accuracy: 1, "conversation moved sideways")

        // Nothing in the conversation reaches past the screen edge, except
        // code and wide tables, which scroll inside their own blocks.
        let scrolled = Set(app.scrollViews.scrollViews.staticTexts.allElementsBoundByIndex.map(\.label))
        let wide = app.staticTexts.allElementsBoundByIndex.filter {
            !scrolled.contains($0.label) && $0.frame.maxX > window.maxX + 1 && $0.frame.minX < window.maxX
        }
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Chat-Wide-Content"
        shot.lifetime = .keepAlways
        add(shot)
        XCTAssertTrue(wide.isEmpty, "Overflowing text: \(wide.map { "\($0.label.prefix(40)) maxX=\($0.frame.maxX)" })")
    }

    func testWebLinkGetsCard() {
        let app = XCUIApplication()
        app.launchArguments += ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        let title = app.staticTexts["Compare SSH latency by region"]
        for _ in 0..<4 { if title.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        title.tap()
        // The fennec.runta.com link gets a card from its Open Graph tags.
        let card = app.descendants(matching: .any)["link-card"].firstMatch
        let deadline = Date.now.addingTimeInterval(20)
        while !card.exists && Date.now < deadline { app.swipeUp(); Thread.sleep(forTimeInterval: 1) }
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Chat-Link-Card"
        shot.lifetime = .keepAlways
        add(shot)
        if !card.exists { print(app.debugDescription) }
        XCTAssertTrue(card.exists, "no link card for https://fennec.runta.com")
    }
}
