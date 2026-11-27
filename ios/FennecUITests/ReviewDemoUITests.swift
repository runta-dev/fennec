import XCTest

/// App Review's sign-in: appreview@runta.com sends no code, any code opens the
/// demo, and every main screen works in it with nothing from the network:
/// sessions with recorded replies, the terminal, file and web page previews,
/// queues (list, thread, reply), Account's cloud usage, the privacy policy
/// links, and Sign out. Also the QR sign-in page a reviewer may open first.
/// Runs in Release too (no Debug-only launch arguments). Screenshots go to
/// /tmp/fennec-review-demo.
final class ReviewDemoUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testReviewEmailOpensDemoWithRecordedReplies() {
        let app = launch()
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
        attach(app, "01-home")

        app.buttons["new-session-menu"].tap()
        app.buttons["create-agent-claudeCode"].tap()
        let input = app.textViews["chat-composer"]
        XCTAssertTrue(input.waitForExistence(timeout: 5))
        input.tap()
        input.typeText("Review authorization in my project")
        app.buttons["composer-send"].tap()
        XCTAssertTrue(text(app, "listing the fixture").waitForExistence(timeout: 10))
        XCTAssertTrue(text(app, "Nothing was committed").waitForExistence(timeout: 25))
        attach(app, "02-reply")

        // The demo survives a relaunch, and Sign out returns to sign-in.
        app.terminate()
        app.launch()
        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 10))
        signOutIfSignedIn(app)
        XCTAssertTrue(app.buttons["Sign in with email"].waitForExistence(timeout: 10))
    }

    /// The terminal opens from the session menu and replays the session's
    /// recorded terminal, read only.
    func testDemoTerminal() {
        let app = signedInDemo()
        openSession(app, "Fix Magpie panel startup")
        app.buttons["Session menu"].tap()
        let open = app.buttons["Open terminal"]
        XCTAssertTrue(open.waitForExistence(timeout: 3))
        XCTAssertTrue(open.isEnabled, "Open terminal is disabled in the demo")
        attach(app, "03-session-menu")
        open.tap()
        XCTAssertTrue(app.descendants(matching: .any)["demo-terminal"].firstMatch.waitForExistence(timeout: 5))
        sleep(3) // the replay ends
        XCTAssertFalse(app.keyboards.firstMatch.exists, "the read-only terminal raised the keyboard")
        attach(app, "04-terminal")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["Session menu"].waitForExistence(timeout: 5))
    }

    /// A reply's web page and image cards load and open from the phone.
    func testDemoPreviewCards() {
        let app = signedInDemo()
        openSession(app, "SSH latency report")
        let page = app.buttons["Preview SSH latency by office"]
        let chart = app.buttons["Preview p95 latency by office"]
        XCTAssertTrue(page.waitForExistence(timeout: 10))
        XCTAssertTrue(chart.waitForExistence(timeout: 10))
        XCTAssertFalse(text(app, "Couldn't load the preview").exists, "a preview card failed")
        // A card is disabled while it loads its file and thumbnail.
        let enabled = NSPredicate(format: "isEnabled == true")
        wait(for: [expectation(for: enabled, evaluatedWith: page), expectation(for: enabled, evaluatedWith: chart)], timeout: 20)
        attach(app, "05-preview-cards")

        page.tap()
        XCTAssertTrue(app.buttons["Close"].waitForExistence(timeout: 10), "the web page didn't open")
        sleep(1)
        attach(app, "06-web-page")
        app.buttons["Close"].tap()

        for _ in 0..<3 { if chart.isHittable { break }; app.swipeUp() }
        let menu = app.buttons["Session menu"]
        XCTAssertTrue(menu.isHittable)
        chart.tap()
        // Quick Look draws in its own process on iOS 26, so its buttons
        // aren't in the app's tree; the chat behind it stops being hittable.
        let covered = expectation(for: NSPredicate(format: "isHittable == false"), evaluatedWith: menu)
        wait(for: [covered], timeout: 10)
        sleep(2)
        // The whole screen: an app screenshot leaves Quick Look's view black.
        attach(app, "07-image", screen: true)
        // Close it with its xmark. It opens full screen, where the first
        // tap only brings back its controls.
        let xmark = app.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.098))
        for _ in 0..<3 where !menu.isHittable {
            xmark.tap()
            sleep(2)
        }
        attach(app, "07b-image-closed", screen: true)
        XCTAssertTrue(menu.isHittable, "the image preview didn't close")
    }

    /// Queues: turned on in Account, then the list, a thread, a reply the
    /// sample agent answers, Mark done, and a working request's live card.
    func testDemoQueues() {
        // Signed in with queues off (whatever an earlier run left), then
        // relaunched without that override; the demo stays signed in.
        let app = signedInDemo()
        app.terminate()
        app.launchArguments.removeAll { $0 == "-queuesEnabled" || $0 == "NO" }
        app.launch()
        setShowQueues(app, on: true)
        let queuesTab = app.tabBars.buttons["Projects"]
        if !queuesTab.isHittable { app.navigationBars.buttons.element(boundBy: 0).tap() }
        XCTAssertTrue(queuesTab.waitForExistence(timeout: 5))
        queuesTab.tap()
        let reviewed = app.descendants(matching: .any)["request-117"].firstMatch
        XCTAssertTrue(reviewed.waitForExistence(timeout: 5))
        attach(app, "08-queues")

        reviewed.tap()
        XCTAssertTrue(app.buttons["Mark done"].waitForExistence(timeout: 5))
        attach(app, "09-queue-thread")
        let composer = app.textFields["thread-composer"].exists ? app.textFields["thread-composer"] : app.textViews["thread-composer"]
        composer.tap()
        composer.typeText("Please also cover the empty state.")
        app.buttons["thread-send"].tap()
        XCTAssertTrue(text(app, "cover the empty state").waitForExistence(timeout: 5))
        // Send keeps the keyboard; a tap on the thread puts it away (FEN-62).
        XCTAssertTrue(app.keyboards.firstMatch.exists, "Send closed the keyboard")
        text(app, "cover the empty state").tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 3), "a tap outside the composer left the keyboard up")
        XCTAssertTrue(text(app, "Ready for your review").waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["Mark done"].waitForExistence(timeout: 5))
        attach(app, "10-queue-reply")
        app.buttons["Mark done"].tap()
        // Done: no Reopen button, a new message reopens it (FEN-118).
        XCTAssertTrue(app.buttons["undo-mark-done"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["Mark done"].waitForNonExistence(timeout: 5))
        XCTAssertFalse(app.buttons["Reopen"].exists)
        app.navigationBars.buttons.element(boundBy: 0).tap()

        let working = app.descendants(matching: .any)["request-121"].firstMatch
        XCTAssertTrue(working.waitForExistence(timeout: 5))
        working.tap()
        XCTAssertTrue(app.descendants(matching: .any)["thread-live"].firstMatch.waitForExistence(timeout: 5))
        attach(app, "11-queue-live")
        app.navigationBars.buttons.element(boundBy: 0).tap()

        // Off again: the tab bar goes and the app is back to the sessions list.
        app.tabBars.buttons["Sessions"].tap()
        setShowQueues(app, on: false)
        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 5))
    }

    /// Account: the cloud computer's meters and billing are filled in.
    func testDemoAccountUsage() {
        let app = signedInDemo()
        app.buttons["Account"].tap()
        XCTAssertTrue(text(app, "App Review").waitForExistence(timeout: 5))
        XCTAssertTrue(text(app, "14.2%").waitForExistence(timeout: 5))
        XCTAssertTrue(text(app, "$25.00").exists)
        XCTAssertTrue(text(app, "61.40 vCPU-h").exists)
        attach(app, "12-account")
    }

    /// The QR sign-in page, which a reviewer may open before using the demo.
    func testScanSignInPage() {
        let app = launch()
        signOutIfSignedIn(app)
        let scan = app.buttons["Scan QR code"]
        XCTAssertTrue(scan.waitForExistence(timeout: 10))
        scan.tap()
        // The camera prompt on first use.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons["Allow"]
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        XCTAssertTrue(app.buttons["Back"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.descendants(matching: .any)["sign-in-error"].exists)
        attach(app, "13-scan")
        app.buttons["Back"].tap()
        XCTAssertTrue(app.buttons["Sign in with email"].waitForExistence(timeout: 5))
    }

    /// The privacy policy App Store Connect points to opens from sign-in and
    /// from Account (in the demo, as App Review sees it).
    func testPrivacyPolicyLinksOpenInSafari() {
        let app = launch()
        signOutIfSignedIn(app)

        let signInLink = privacyLink(app)
        XCTAssertTrue(signInLink.waitForExistence(timeout: 10))
        attach(app, "Privacy-Link-Sign-In")
        signInLink.tap()
        assertSafariOpensPrivacyPolicy()
        app.activate()
        // Back from Safari: a tap before Fennec is in front again is lost.
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 10))
        let email = app.buttons["Sign in with email"]
        wait(for: [expectation(for: NSPredicate(format: "isHittable == true"), evaluatedWith: email)], timeout: 10)
        email.tap()
        let address = app.textFields["login-email-address"]
        XCTAssertTrue(address.waitForExistence(timeout: 5))
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

    // MARK: Helpers

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


    /// `queuesOff` keeps Show Queues off for this launch, whatever an
    /// earlier test left; only the queues test turns it on.
    private func launch(queuesOff: Bool = true) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments += ["-RuntaEnvironment", "production", "-appLanguage", "en-US"]
        if queuesOff { app.launchArguments += ["-queuesEnabled", "NO"] }
        app.launch()
        return app
    }

    /// The demo, signed in from the sign-in page as App Review would.
    private func signedInDemo(queuesOff: Bool = true) -> XCUIApplication {
        let app = launch(queuesOff: queuesOff)
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
        code.typeText("000000")
        app.buttons["login-email-verify"].tap()
        XCTAssertTrue(app.buttons["new-session-menu"].waitForExistence(timeout: 10))
        return app
    }

    private func openSession(_ app: XCUIApplication, _ title: String) {
        let row = app.staticTexts[title].firstMatch
        for _ in 0..<5 { if row.exists && row.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(row.isHittable, "\(title) isn't on the home list")
        row.tap()
    }

    /// Account › Queues › Show Queues, from the sessions list. Changing it
    /// rebuilds the main view, so the result is read from the tab bar.
    private func setShowQueues(_ app: XCUIApplication, on: Bool) {
        let tabs = app.tabBars.firstMatch
        if tabs.exists == on { return }
        app.buttons["Account"].tap()
        let toggle = app.switches["show-queues"]
        for _ in 0..<5 { if toggle.exists && toggle.isHittable { break }; app.swipeUp() }
        XCTAssertTrue(toggle.waitForExistence(timeout: 5))
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.93, dy: 0.5)).tap()
        let done = NSPredicate(format: "exists == %@", NSNumber(value: on))
        wait(for: [expectation(for: done, evaluatedWith: tabs)], timeout: 5)
    }

    private func text(_ app: XCUIApplication, _ fragment: String) -> XCUIElement {
        app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", fragment)).firstMatch
    }

    private func signOutIfSignedIn(_ app: XCUIApplication) {
        if app.tabBars.buttons["Sessions"].exists { app.tabBars.buttons["Sessions"].tap() }
        guard app.descendants(matching: .any)["new-session-menu"].firstMatch.waitForExistence(timeout: 5) else { return }
        app.buttons["Account"].tap()
        let signOut = app.buttons["Sign out"]
        for _ in 0..<5 { if signOut.isHittable { break }; app.swipeUp() }
        signOut.tap()
    }

    private func attach(_ app: XCUIApplication, _ name: String, screen: Bool = false) {
        let screenshot = screen ? XCUIScreen.main.screenshot() : app.screenshot()
        let shot = XCTAttachment(screenshot: screenshot)
        shot.name = "Review-Demo-" + name
        shot.lifetime = .keepAlways
        add(shot)
        let directory = URL(fileURLWithPath: "/tmp/fennec-review-demo")
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try? screenshot.pngRepresentation.write(to: directory.appendingPathComponent("\(name).png"))
    }
}
