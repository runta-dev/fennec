import XCTest

final class EnvironmentSwitchUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    /// Five taps on the logo open the environment picker; choosing Integration
    /// shows the INT badge and sign-in opens dashboard.runta.xyz.
    func testFiveTapsSwitchToIntegrationAndSignInUsesIntegrationDashboard() {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "production"]
        app.launch()

        let logo = app.images["fennec-logo"]
        XCTAssertTrue(logo.waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["INT"].exists)
        logo.tap(withNumberOfTaps: 5, numberOfTouches: 1)
        let integration = app.buttons["Integration"]
        XCTAssertTrue(integration.waitForExistence(timeout: 5))
        integration.tap()
        XCTAssertTrue(app.staticTexts["INT"].waitForExistence(timeout: 5))

        XCTAssertTrue(app.buttons["Continue with Google"].exists)
        XCTAssertTrue(app.buttons["Continue with GitHub"].exists)
        app.buttons["Continue with email"].tap()
        // iOS asks before opening the sign-in sheet and names the domain.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let prompt = springboard.alerts.firstMatch
        XCTAssertTrue(prompt.waitForExistence(timeout: 20), "sign-in sheet prompt did not appear")
        let text = prompt.label + " " + prompt.staticTexts.allElementsBoundByIndex.map(\.label).joined(separator: " ")
        XCTAssertTrue(text.contains("runta.xyz"), "prompt named: \(text)")
        prompt.buttons["Cancel"].tap()

        // Restore production for the next run.
        logo.tap(withNumberOfTaps: 5, numberOfTouches: 1)
        app.buttons["Production"].tap()
    }
}
