import XCTest

/// FEN-47: at the largest accessibility text size (run it with Bold Text on too)
/// the thread's icons (back, menu, attach) keep their size and regular weight.
/// Screenshots go to /tmp/fennec-large-text.
final class LargeTextUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testThreadIconsAtLargestTextSize() {
        let app = XCUIApplication()
        app.launchArguments = ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US",
                               "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        app.launch()
        let reviewed = app.descendants(matching: .any)["request-117"].firstMatch
        XCTAssertTrue(reviewed.waitForExistence(timeout: 10))
        shot(app, "queues")
        reviewed.tap()
        let menu = app.buttons["request-menu"]
        XCTAssertTrue(menu.waitForExistence(timeout: 5))
        sleep(1)
        shot(app, "thread")
        let size = menu.frame.size
        XCTAssertLessThanOrEqual(max(size.width, size.height), 60, "the thread menu button grew with the text size: \(size)")
    }

    private func shot(_ app: XCUIApplication, _ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        let directory = URL(fileURLWithPath: "/tmp/fennec-large-text")
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try? screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"))
    }
}
