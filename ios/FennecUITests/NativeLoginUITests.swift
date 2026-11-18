import XCTest

/// Live smoke test: starts authorization but never enters provider credentials.
final class NativeLoginUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testNativeProvidersAndEmailKeepScanAvailable() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "integration", "-appLanguage", "en-US"]
        app.launch()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        XCTAssertTrue(app.buttons["Google"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["GitHub"].exists)
        XCTAssertTrue(app.buttons["Email"].exists)
        XCTAssertTrue(app.buttons["Scan"].exists)
        XCTAssertFalse(app.buttons["Choose from Photos"].exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Native-Login-Choices"
        shot.lifetime = .keepAlways
        add(shot)
        app.buttons["Email"].tap()
        XCTAssertTrue(app.textFields["login-email-address"].exists)
        XCTAssertTrue(app.buttons["Send code"].exists)
        app.buttons["Back"].tap()
        app.buttons["Scan"].tap()
        let cameraPrompt = springboard.alerts.firstMatch
        if cameraPrompt.waitForExistence(timeout: 3), cameraPrompt.buttons["Allow"].firstMatch.exists {
            cameraPrompt.buttons["Allow"].firstMatch.tap()
        }
        XCTAssertTrue(app.staticTexts["Scan from Fennec on your Mac"].exists)
        XCTAssertTrue(app.buttons["Choose from Photos"].exists)
        app.buttons["Back"].tap()
        for provider in ["Google", "GitHub"] {
            let button = app.buttons[provider]
            let enabled = NSPredicate(format: "enabled == true")
            expectation(for: enabled, evaluatedWith: button)
            waitForExpectations(timeout: 10)
            button.tap()
            let prompt = springboard.alerts.firstMatch
            XCTAssertTrue(prompt.waitForExistence(timeout: 30), "Native provider did not open authentication")
            let text = prompt.staticTexts.allElementsBoundByIndex.map(\.label).joined(separator: " ")
            XCTAssertTrue(text.contains("runta.xyz"), "Wrong authentication environment")
            prompt.buttons["Cancel"].tap()
            XCTAssertTrue(app.buttons["Scan"].exists)
        }
    }
}
