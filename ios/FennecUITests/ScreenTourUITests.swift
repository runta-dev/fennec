import XCTest

/// Screenshots of the signed-in screens for review (skipped when signed out):
/// the newest idle chat and the Account page, saved under /tmp/fennec-tour.
final class ScreenTourUITests: XCTestCase {
    func testTour() throws {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "integration"]
        app.launch()
        let account = app.buttons.matching(NSPredicate(format: "label == 'Account'")).firstMatch
        try XCTSkipUnless(app.staticTexts["Idle"].waitForExistence(timeout: 20), "not signed in")
        let directory = URL(fileURLWithPath: "/tmp/fennec-tour")
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        func shot(_ name: String) {
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: directory.appendingPathComponent(name + ".png"))
        }
        let row = app.staticTexts.containing(NSPredicate(format: "label CONTAINS '磁盘'")).firstMatch
        if row.waitForExistence(timeout: 5) {
            row.tap()
            sleep(6)
            shot("chat-bottom")
            app.swipeDown(); app.swipeDown()
            shot("chat-up")
            app.terminate(); app.launch()
            _ = app.staticTexts["Idle"].waitForExistence(timeout: 20)
        }
        let pi = app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'fly-ink'")).firstMatch
        if pi.waitForExistence(timeout: 3) {
            pi.tap()
            sleep(6)
            shot("pi")
            app.terminate(); app.launch()
            _ = app.staticTexts["Idle"].waitForExistence(timeout: 20)
        }
        XCTAssertTrue(account.waitForExistence(timeout: 10))
        account.tap()
        sleep(7)
        shot("account")
    }
}
