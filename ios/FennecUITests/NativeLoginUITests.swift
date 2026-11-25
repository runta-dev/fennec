import XCTest

/// Live smoke test: starts authorization but never enters provider credentials.
final class NativeLoginUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    /// Guideline 4.8: Sign in with Apple is offered, first among the providers.
    func testSignInWithAppleComesFirst() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        let apple = app.buttons["login-apple"]
        let google = app.buttons["Continue with Google"]
        XCTAssertTrue(apple.waitForExistence(timeout: 10))
        XCTAssertTrue(google.exists)
        XCTAssertTrue(apple.isHittable)
        XCTAssertLessThan(apple.frame.maxY, google.frame.minY)
        XCTAssertEqual(apple.frame.width, google.frame.width, accuracy: 1)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Sign-In-With-Apple-First"
        shot.lifetime = .keepAlways
        add(shot)
    }

    func testNativeProvidersAndEmailKeepScanAvailable() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "integration", "-appLanguage", "en-US"]
        app.launch()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        XCTAssertTrue(app.buttons["Continue with Google"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Continue with GitHub"].exists)
        XCTAssertTrue(app.buttons["Sign in with email"].exists)
        XCTAssertTrue(app.buttons["Scan QR code"].exists)
        XCTAssertFalse(app.buttons["Choose from Photos"].exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Native-Login-Choices"
        shot.lifetime = .keepAlways
        add(shot)
        app.buttons["Sign in with email"].tap()
        XCTAssertTrue(app.textFields["login-email-address"].exists)
        XCTAssertTrue(app.buttons["Send code"].exists)
        app.buttons["Back"].tap()
        app.buttons["Scan QR code"].tap()
        let cameraPrompt = springboard.alerts.firstMatch
        if cameraPrompt.waitForExistence(timeout: 3), cameraPrompt.buttons["Allow"].firstMatch.exists {
            cameraPrompt.buttons["Allow"].firstMatch.tap()
        }
        XCTAssertTrue(app.staticTexts["Scan from Fennec on your Mac"].exists)
        XCTAssertTrue(app.buttons["Choose from Photos"].exists)
        app.buttons["Back"].tap()
        for provider in ["Google", "GitHub"] {
            let button = app.buttons["Continue with \(provider)"]
            let enabled = NSPredicate(format: "enabled == true")
            expectation(for: enabled, evaluatedWith: button)
            waitForExpectations(timeout: 10)
            button.tap()
            let prompt = springboard.alerts.firstMatch
            XCTAssertTrue(prompt.waitForExistence(timeout: 30), "Native provider did not open authentication")
            let text = prompt.staticTexts.allElementsBoundByIndex.map(\.label).joined(separator: " ")
            XCTAssertTrue(text.contains("runta.xyz"), "Wrong authentication environment")
            prompt.buttons["Cancel"].tap()
            XCTAssertTrue(app.buttons["Scan QR code"].exists)
        }
    }
}
