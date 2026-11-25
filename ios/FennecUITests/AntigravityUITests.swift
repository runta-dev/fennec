import XCTest

/// Antigravity CLI (agy) is an agent of its own on the phone: offered in the
/// new-session menu with its mark, its sessions open as chats and answer.
/// Runs in the App Review demo, which needs no account or cloud computer.
final class AntigravityUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testAntigravitySessionInDemo() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        signOutIfSignedIn(app)

        XCTAssertTrue(app.buttons["Sign in with email"].waitForExistence(timeout: 10))
        app.buttons["Sign in with email"].tap()
        let address = app.textFields["login-email-address"]
        address.tap()
        address.typeText("appreview@runta.com")
        app.buttons["Send code"].tap()
        let code = app.textFields["login-email-code"]
        XCTAssertTrue(code.waitForExistence(timeout: 2))
        code.tap()
        code.typeText("1")
        app.buttons["login-email-verify"].tap()
        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 10))

        app.buttons["new-session-menu"].tap()
        let agy = app.buttons["create-agent-antigravity"]
        XCTAssertTrue(agy.waitForExistence(timeout: 5), "Antigravity CLI is not offered")
        XCTAssertTrue(agy.label.contains("Antigravity CLI"), agy.label)
        attach(app, "Antigravity-New-Session-Menu")
        agy.tap()

        let input = app.textViews["chat-composer"]
        XCTAssertTrue(input.waitForExistence(timeout: 5))
        input.tap()
        input.typeText("Add tests for the retry backoff")
        app.buttons["composer-send"].tap()
        XCTAssertTrue(text(app, "RetryDelay").waitForExistence(timeout: 15))
        XCTAssertTrue(text(app, "No commit was created").waitForExistence(timeout: 25))
        attach(app, "Antigravity-Chat")

        // Back on the list, the session is Antigravity CLI's.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 10))
        attach(app, "Antigravity-Session-List")
        signOutIfSignedIn(app)
    }

    private func text(_ app: XCUIApplication, _ fragment: String) -> XCUIElement {
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", fragment)).firstMatch
    }

    private func signOutIfSignedIn(_ app: XCUIApplication) {
        guard app.buttons["new-session-menu"].waitForExistence(timeout: 5) else { return }
        app.buttons["Account"].tap()
        let signOut = app.buttons["Sign out"]
        for _ in 0..<4 { if signOut.isHittable { break }; app.swipeUp() }
        signOut.tap()
    }

    private func attach(_ app: XCUIApplication, _ name: String) {
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
    }
}
