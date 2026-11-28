import UserNotifications
import SwiftUI

/// 03 / 03b connecting. An offline Mac is not an error: gray row, no ×.
struct ConnectingView: View {
    @Environment(RuntaAuth.self) private var auth
    @Environment(SessionStore.self) private var store
    var onContinue: () -> Void
    @State private var agentsLoaded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            FoxMark(size: 52).padding(.top, 40)
            Text(L10n.text("ios.signed_in")).font(.system(size: 24, weight: .semibold))
            ListCard {
                ListRow(icon: "person", title: L10n.text("ios.runta_account"), subtitle: auth.profile?.email) { check(true) }
                ListRow(icon: "cloud", title: L10n.text("ui.cloud_computer"),
                        subtitle: store.cloudRunning ? "Running · \(store.sessions.count) live sessions" : L10n.text("ios.stopped")) { check(store.cloudRunning) }
                if store.macOnline {
                    ListRow(icon: "laptopcomputer", title: "MacBook Pro", subtitle: L10n.text("ios.online")) { check(true) }
                } else {
                    ListRow(icon: "laptopcomputer", iconColor: Theme.fg3, title: "MacBook Pro", subtitle: L10n.text("ui.offline"), titleColor: Theme.fg2) {
                        Circle().fill(Theme.fg3).frame(width: 7, height: 7).frame(width: 20)
                    }
                }
                ListRow(icon: "apple.terminal", title: L10n.text("ui.agents"), showDivider: false) {
                    if agentsLoaded { check(true) } else { ProgressView().tint(Theme.accent).frame(width: 20) }
                }
            }
            Spacer()
            Button(L10n.text("ui.continue"), action: onContinue).buttonStyle(PrimaryButtonStyle())
            Button(L10n.text("ios.enable_notifications")) {
                UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in }
            }
            .font(.system(size: 14, weight: .medium)).foregroundStyle(Theme.fg2).frame(maxWidth: .infinity)
        }
        .padding(.horizontal, 20)
        .padding(.bottom, 24)
        .pageBackground()
        .task {
            await auth.loadProfile()
            try? await Task.sleep(for: .milliseconds(700))
            agentsLoaded = true
        }
    }

    private func check(_ done: Bool) -> some View {
        Image(systemName: "checkmark").regularIconWeight().font(.system(size: 15, weight: .semibold))
            .foregroundStyle(Theme.accent).frame(width: 20).opacity(done ? 1 : 0)
    }
}
