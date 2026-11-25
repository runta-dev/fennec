import XCTest

/// App Review's sign-in: appreview@runta.com sends no code, any code opens the
/// demo, demo sessions answer with recorded replies, and Sign out leaves it.
final class ReviewDemoUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testReviewEmailOpensDemoWithRecordedReplies() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        signOutIfSignedIn(app)

        XCTAssertTrue(app.buttons["Sign in with email"].waitForExistence(timeout: 10))
        app.buttons["Sign in with email"].tap()
        let address = app.textFields["login-email-address"]
        address.tap()
        address.typeText("AppReview@runta.com ")
        app.buttons["Send code"].tap()
        // No request is made, so the code field appears at once.
        let code = app.textFields["login-email-code"]
        XCTAssertTrue(code.waitForExistence(timeout: 2))
        code.tap()
        code.typeText("1")
        app.buttons["login-email-verify"].tap()

        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 10))
        attach(app, "Review-Demo-Home")

        app.buttons["new-session-menu"].tap()
        app.buttons["create-agent-claudeCode"].tap()
        let input = app.textViews["chat-composer"]
        XCTAssertTrue(input.waitForExistence(timeout: 5))
        input.tap()
        input.typeText("Review authorization in my project")
        app.buttons["composer-send"].tap()
        XCTAssertTrue(text(app, "listing the fixture").waitForExistence(timeout: 10))
        XCTAssertTrue(text(app, "Nothing was committed").waitForExistence(timeout: 25))
        attach(app, "Review-Demo-Reply")

        // The demo survives a relaunch, and Sign out returns to sign-in.
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 10))
        signOutIfSignedIn(app)
        XCTAssertTrue(app.buttons["Sign in with email"].waitForExistence(timeout: 10))
    }

    /// The privacy policy App Store Connect points to opens from sign-in and
    /// from Account (in the demo, as App Review sees it).
    func testPrivacyPolicyLinksOpenInSafari() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        signOutIfSignedIn(app)

        let signInLink = privacyLink(app)
        XCTAssertTrue(signInLink.waitForExistence(timeout: 10))
        attach(app, "Privacy-Link-Sign-In")
        signInLink.tap()
        assertSafariOpensPrivacyPolicy()
        app.activate()

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

        app.buttons["Account"].tap()
        let accountLink = privacyLink(app)
        for _ in 0..<4 { if accountLink.isHittable { break }; app.swipeUp() }
        attach(app, "Privacy-Link-Account")
        accountLink.tap()
        assertSafariOpensPrivacyPolicy()
        app.activate()
        signOutIfSignedIn(app)
    }

    /// SwiftUI's Link is a link, not a button, to accessibility.
    private func privacyLink(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: "privacy-policy").firstMatch
    }

    private func assertSafariOpensPrivacyPolicy() {
        let safari = XCUIApplication(bundleIdentifier: "com.apple.mobilesafari")
        XCTAssertTrue(safari.wait(for: .runningForeground, timeout: 15))
        // The page's own heading, once it has loaded from runfennec.com.
        let heading = safari.staticTexts["Privacy Policy"].firstMatch
        XCTAssertTrue(heading.waitForExistence(timeout: 30))
        attach(safari, "Privacy-Policy-Safari")
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
