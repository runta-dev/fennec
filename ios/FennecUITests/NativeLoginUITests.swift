import XCTest

/// Live smoke test: starts authorization but never enters provider credentials.
final class NativeLoginUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testNativeProvidersKeepPairingAndCancelBackToScanner() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "integration", "-appLanguage", "en-US"]
        app.launch()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let cameraPrompt = springboard.alerts.firstMatch
        if cameraPrompt.waitForExistence(timeout: 3), cameraPrompt.buttons["Allow"].firstMatch.exists {
            cameraPrompt.buttons["Allow"].firstMatch.tap()
        }
        XCTAssertTrue(app.staticTexts["Scan from Fennec on your Mac"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Choose from Photos"].exists)
        XCTAssertTrue(app.buttons["Continue with email"].exists)
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
            XCTAssertTrue(app.staticTexts["Scan from Fennec on your Mac"].exists)
        }
    }
}
