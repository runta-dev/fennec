import BackgroundTasks
import FennecCore
import SwiftUI
import UIKit

/// FEN-234: the home list is read while the app isn't in front, so opening
/// it shows the current list from the cache instead of the one from the
/// last visit, which then waited 2–3 s for a new connection before
/// changing. iOS wakes the app now and then (Background App Refresh) for
/// one board read; `QueueStore.refresh` saves what it read to the list
/// cache that the home screen opens on.
@MainActor
enum BackgroundRefresh {
    /// Also in `BGTaskSchedulerPermittedIdentifiers` (ios/project.yml).
    static let identifier = "com.runta.fennec.ios.refresh"
    /// The model a background launch refreshes; set when the app starts.
    static weak var app: AppModel?

    /// Asks iOS to wake the app for a refresh in about 15 minutes. iOS
    /// decides when, going by how often the app is used, and skips it in
    /// Low Power Mode or when Background App Refresh is off.
    static func schedule() {
        let request = BGAppRefreshTaskRequest(identifier: identifier)
        request.earliestBeginDate = .now.addingTimeInterval(15 * 60)
        try? BGTaskScheduler.shared.submit(request)
    }

    /// One woken refresh: the open project's list, within the time iOS
    /// gives (about 30 s; a slow link stops waiting at 25).
    static func run() async {
        schedule()
        guard let app, !app.preview, app.auth.status != .signedOut, let core = app.queues.core else { return }
        let read = Task { await app.queues.refresh() }
        await finished(read, within: 25)
        // The connection used here may die once the app is suspended again:
        // the next use checks it first (FEN-68).
        core.suspend()
    }

    /// Waits for `work`, but no longer than `seconds` or until this task
    /// is cancelled (iOS ran out of time for it).
    private static func finished(_ work: Task<Void, Never>, within seconds: Double) async {
        let gate = Once()
        await withTaskCancellationHandler {
            await withCheckedContinuation { continuation in
                gate.wait(continuation)
                Task { await work.value; gate.resume() }
                Task { try? await Task.sleep(for: .seconds(seconds)); gate.resume() }
            }
        } onCancel: {
            Task { @MainActor in gate.resume() }
        }
    }

    /// Resumes its continuation once, whichever comes first; a resume
    /// before the wait (cancelled at once) ends the wait as it starts.
    @MainActor
    private final class Once {
        private var continuation: CheckedContinuation<Void, Never>?
        private var resumed = false
        func wait(_ continuation: CheckedContinuation<Void, Never>) {
            if resumed { continuation.resume() } else { self.continuation = continuation }
        }
        func resume() {
            guard !resumed else { return }
            resumed = true
            continuation?.resume()
            continuation = nil
        }
    }
}

/// FEN-234: leaving the app for a moment (a message, another app) used to
/// close the board stream at once, so coming back always waited for a new
/// connection. Now the stream and its connection stay open for a short
/// while in the background, and a quick return finds the list live.
@MainActor
final class BackgroundGrace {
    /// How long the stream stays open after the app leaves the screen.
    /// iOS gives a background task about 30 s.
    static let seconds = 25.0

    private var task: UIBackgroundTaskIdentifier = .invalid
    private var timer: Task<Void, Never>?
    private var close: (() -> Void)?

    /// The app went to the background: run `close` after the grace period,
    /// or right away when iOS gives no background time.
    func begin(_ close: @escaping () -> Void) {
        end(running: true)
        self.close = close
        task = UIApplication.shared.beginBackgroundTask(withName: "fennec.board-stream") { [weak self] in
            MainActor.assumeIsolated { self?.end(running: true) }
        }
        guard task != .invalid else { end(running: true); return }
        timer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(Self.seconds))
            guard !Task.isCancelled else { return }
            self?.end(running: true)
        }
    }

    /// Back in front within the grace period: keep everything open.
    func cancel() { end(running: false) }

    private func end(running: Bool) {
        timer?.cancel()
        timer = nil
        let close = self.close
        self.close = nil
        if running { close?() }
        if task != .invalid {
            UIApplication.shared.endBackgroundTask(task)
            task = .invalid
        }
    }
}
