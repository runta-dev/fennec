import XCTest

/// Screenshots of the signed-in screens for review (skipped when signed out):
/// the newest idle chat and the Account page, saved under /tmp/fennec-tour.
final class ScreenTourUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testSettingsSegmentsLanguageAndPersistence() {
        let app = XCUIApplication()
        app.launchArguments = ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        XCTAssertTrue(app.buttons["Account"].waitForExistence(timeout: 10))
        app.buttons["Account"].tap()
        let appearance = app.segmentedControls["appearance-picker"]
        for _ in 0..<3 { if appearance.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(appearance.waitForExistence(timeout: 5))
        XCTAssertEqual(appearance.buttons.count, 3)
        appearance.buttons["Dark"].tap()
        XCTAssertTrue(appearance.buttons["Dark"].isSelected)
        XCTAssertLessThan(app.staticTexts["System settings"].frame.minY, app.staticTexts["Notify me when"].frame.minY)
        app.buttons["language-picker"].tap()
        app.buttons["简体中文"].tap()
        XCTAssertTrue(app.staticTexts["系统设置"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.navigationBars["账户"].exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Settings-Chinese-Dark"
        shot.lifetime = .keepAlways
        add(shot)
        app.terminate()
        app.launchArguments = ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production"]
        app.launch()
        XCTAssertTrue(app.buttons["账户"].waitForExistence(timeout: 10))
        app.buttons["账户"].tap()
        for _ in 0..<3 { if appearance.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(appearance.buttons["深色"].isSelected)
        app.buttons["language-picker"].tap()
        app.buttons["English"].tap()
        XCTAssertTrue(app.staticTexts["System settings"].waitForExistence(timeout: 5))
        appearance.buttons["System"].tap()
    }

    func testAgentMenuCreatesEmptyChatAndKeyboardSendsMessage() {
        let app = XCUIApplication()
        app.launchArguments = ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 10))
        app.buttons["new-session-menu"].tap()
        app.buttons["create-agent-codex"].tap()
        XCTAssertTrue(app.descendants(matching: .any)["empty-conversation"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertFalse(app.keyboards.firstMatch.exists)
        XCTAssertFalse(app.buttons["Open terminal"].exists)
        let emptyShot = XCTAttachment(screenshot: app.screenshot())
        emptyShot.name = "New-Agent-Empty-Conversation"
        emptyShot.lifetime = .keepAlways
        add(emptyShot)
        XCTAssertEqual(app.sheets.count, 0)
        let input = app.textViews["chat-composer"]
        XCTAssertTrue(input.waitForExistence(timeout: 5))
        input.tap()
        input.typeText("First message")
        XCTAssertEqual(input.value as? String, "First message")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Composer-Send-Key"
        shot.lifetime = .keepAlways
        add(shot)
        let send = app.keyboards.buttons.matching(NSPredicate(format: "label == 'Send' OR label == 'send'")).firstMatch
        XCTAssertTrue(send.exists)
        send.tap()
        XCTAssertEqual(input.value as? String, "")
        XCTAssertTrue(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'First message'")).firstMatch.waitForExistence(timeout: 5))
    }

    func testSendingWhileWorkingClearsComposer() {
        let app = XCUIApplication()
        app.launchArguments += ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        let title = app.staticTexts["Fennec iOS mockups"]
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        title.tap()
        let input = app.textViews["chat-composer"]
        XCTAssertTrue(input.waitForExistence(timeout: 5))
        input.tap()
        let text = "Follow up " + UUID().uuidString
        input.typeText(text)
        XCTAssertTrue((input.value as? String)?.contains(text) == true, "Composer value: \(String(describing: input.value)); typed: \(text)")
        app.buttons["composer-send"].tap()
        XCTAssertFalse((input.value as? String)?.contains(text) == true)
        let visible = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.waitForExistence(timeout: 3)
        if !visible {
            let screenshot = XCTAttachment(screenshot: app.screenshot())
            screenshot.name = "Missing-Sent-Message"
            screenshot.lifetime = .keepAlways
            add(screenshot)
            print(app.debugDescription)
        }
        XCTAssertTrue(visible)
        app.navigationBars.buttons.firstMatch.tap()
        title.tap()
        XCTAssertFalse((input.value as? String)?.contains(text) == true)
    }

    func testDraftSurvivesNavigationAndRelaunchAboveKeyboard() {
        let app = XCUIApplication()
        app.launchArguments += ["-PreviewSignedIn", "YES", "-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        app.launch()
        let title = app.staticTexts["Fix Magpie panel startup"]
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        title.tap()
        let input = app.textViews["chat-composer"]
        XCTAssertTrue(input.waitForExistence(timeout: 5))
        input.tap()
        let text = "Draft " + UUID().uuidString
        input.typeText(text)
        XCTAssertTrue((input.value as? String)?.contains(text) == true)
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        XCTAssertLessThanOrEqual(input.frame.maxY, app.keyboards.firstMatch.frame.minY)
        app.navigationBars.buttons.firstMatch.tap()
        title.tap()
        XCTAssertTrue((input.value as? String)?.contains(text) == true)
        app.terminate()
        app.launch()
        XCTAssertTrue(title.waitForExistence(timeout: 10))
        title.tap()
        XCTAssertTrue((input.value as? String)?.contains(text) == true)
        app.buttons["composer-send"].tap()
        XCTAssertFalse((input.value as? String)?.contains(text) == true)
    }

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
