import SwiftUI

/// 13 agents. Each switch is the Mac's Cloud computer switch.
struct AgentsView: View {
    @Environment(SessionStore.self) private var store

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                ListCard {
                    ForEach(Array(AgentKind.allCases.enumerated()), id: \.element) { index, agent in
                        agentRow(agent, last: index == AgentKind.allCases.count - 1)
                    }
                }
                GroupHeader(title: L10n.text("ui.coming_soon")).padding(.horizontal, -18)
                ListCard {
                    ListRow(title: "Gemini CLI", titleColor: Theme.fg3)
                    ListRow(title: "Copilot CLI", titleColor: Theme.fg3, showDivider: false)
                }
            }
            .padding(16)
        }
        .background(Theme.bg)
        .navigationTitle(L10n.text("ui.agents"))
        .navigationBarTitleDisplayMode(.inline)
    }

    private func agentRow(_ agent: AgentKind, last: Bool) -> some View {
        let count = store.sessions.filter { $0.agent == agent }.count
        let needsMacLogin = agent == .cursor
        return VStack(spacing: 0) {
            HStack(spacing: 14) {
                AgentIcon(agent: agent, size: 24)
                VStack(alignment: .leading, spacing: 1) {
                    Text(agent.displayName).font(.system(size: 15.5))
                    if needsMacLogin {
                        Text(L10n.text("ios.mac_signin")).font(.caption2Muted).foregroundStyle(Theme.accent)
                    } else if count > 0 {
                        Text(count == 1 ? L10n.text("ios.one_session") : L10n.text("ios.session_count", values: ["count": String(count)]))
                            .font(.caption2Muted).foregroundStyle(Theme.fg3)
                    }
                }
                Spacer()
                Toggle(agent.displayName, isOn: Binding(
                    get: { store.enabledAgents.contains(agent) },
                    set: { on in if on { store.enabledAgents.insert(agent) } else { store.enabledAgents.remove(agent) } }))
                    .labelsHidden()
                    .tint(Theme.accent)
                    .disabled(needsMacLogin)
            }
            .padding(.horizontal, 16).padding(.vertical, 11)
            if !last { Divider().overlay(Theme.line).padding(.leading, 16) }
        }
    }
}

enum Governance: String, CaseIterable, Identifiable {
    case ask = "Ask for approval"
    case approveForMe = "Approve for me"
    case fullAccess = "Full access"
    var id: String { rawValue }
}

/// 14 account.
struct AccountView: View {
    @AppStorage("appearance") private var appearance = "system"
    @Environment(RuntaAuth.self) private var auth
    @Environment(AppModel.self) private var app
    @Environment(SessionStore.self) private var store
    var openAgents: () -> Void
    @Environment(\.openURL) private var openURL
    @State private var usage = AccountUsage()
    @AppStorage("notifyApproval") private var notifyApproval = true
    @AppStorage("notifyQuestion") private var notifyQuestion = true
    @AppStorage("notifyFinished") private var notifyFinished = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 12) {
                    Text(auth.profile?.initials ?? "")
                        .font(.system(size: 16, weight: .medium))
                        .frame(width: 50, height: 50)
                        .background(Theme.surface2, in: Circle())
                        .environmentSwitchGesture()
                    VStack(alignment: .leading, spacing: 1) {
                        HStack(spacing: 8) {
                            Text(auth.profile?.displayName ?? L10n.text("ios.runta_account")).font(.system(size: 16, weight: .medium))
                            EnvironmentBadge(environment: app.environment)
                        }
                        Text(auth.profile?.email ?? "").font(.system(size: 13)).foregroundStyle(Theme.fg2).lineLimit(1)
                    }
                }
                .padding(.horizontal, 2)
                // Governance and agent settings live on the Mac; here, what the account is using.
                CloudComputerCard(metrics: usage.metrics)
                BillingCard(billing: usage.billing, failed: usage.billingFailed) {
                    if let url = URL(string: auth.core.dashboardURL()) { openURL(url) }
                }
                systemSettings
                GroupHeader(title: L10n.text("ios.notify_when")).padding(.horizontal, -18)
                    .accessibilityIdentifier("notification-settings-heading")
                ListCard {
                    toggleRow(L10n.text("ios.notify_approval"), $notifyApproval)
                    toggleRow(L10n.text("ios.notify_question"), $notifyQuestion)
                    toggleRow(L10n.text("ios.notify_finished"), $notifyFinished, last: true)
                }
                ListCard {
                    Button { if ReviewDemo.enabled { app.exitReviewDemo() } else { auth.signOut() } } label: {
                        ListRow(icon: "rectangle.portrait.and.arrow.right", iconColor: Theme.danger, title: L10n.text("ui.sign_out"), titleColor: Theme.danger, showDivider: false)
                    }
                    .buttonStyle(.plain)
                }
                if app.environment == .integration, let first = store.firstListSeconds {
                    // Integration only: how long the session list takes, by stage.
                    ListCard {
                        ListRow(icon: "stopwatch", title: String(format: "Session list %.1f s after sign-in", first),
                                subtitle: store.lastListTiming.map { t in
                                    String(format: "last %.2f s · connect %.2f s · list %.2f s%@", Double(t.total_ms) / 1000,
                                           Double(t.connect_ms) / 1000, Double(t.command_ms) / 1000, t.reused ? " · reused" : " · new connection")
                                }, showDivider: false)
                    }
                    .accessibilityIdentifier("list-timing")
                }
                #if DEBUG
                ListCard {
                    toggleRow("Debug: Mac online", Binding(get: { store.macOnline }, set: { store.macOnline = $0 }), last: true)
                }
                #endif
            }
            .padding(16)
        }
        .background(Theme.bg)
        .navigationTitle(L10n.text("ui.account"))
        .navigationBarTitleDisplayMode(.inline)
        .task { if app.preview { usage.showDemo() } else { await usage.loadBilling(auth.core) } }
        .task {
            guard let core = store.core else { return }
            await usage.watchMetrics(core)
        }
    }

    private var systemSettings: some View {
        @Bindable var language = L10n.preferences
        return VStack(alignment: .leading, spacing: 8) {
            GroupHeader(title: L10n.text("ios.system_settings")).padding(.horizontal, -18)
                .accessibilityIdentifier("system-settings-heading")
            ListCard {
                VStack(alignment: .leading, spacing: 12) {
                    Label(L10n.text("ui.appearance"), systemImage: "circle.lefthalf.filled")
                        .font(.system(size: 15.5, weight: .medium))
                    Picker(L10n.text("ui.appearance"), selection: $appearance) {
                        Text(L10n.text("ui.system")).tag("system")
                        Text(L10n.text("ui.light")).tag("light")
                        Text(L10n.text("ui.dark")).tag("dark")
                    }
                    .pickerStyle(.segmented)
                    .accessibilityIdentifier("appearance-picker")
                }
                .padding(16)
                Divider().overlay(Theme.line).padding(.leading, 16)
                HStack(spacing: 12) {
                    Label(L10n.text("ui.language"), systemImage: "globe")
                        .font(.system(size: 15.5, weight: .medium))
                    Spacer(minLength: 8)
                    Picker(L10n.text("ui.language"), selection: $language.selection) {
                        Text(L10n.text("ios.follow_system")).tag("system")
                        Text(L10n.text("ios.language_english", language: "en-US")).tag("en-US")
                        Text(L10n.text("ios.language_chinese", language: "zh-CN")).tag("zh-CN")
                    }
                    .labelsHidden()
                    .pickerStyle(.menu)
                    .tint(Theme.fg2)
                    .accessibilityIdentifier("language-picker")
                }
                .padding(.horizontal, 16).padding(.vertical, 10)
            }
        }
    }

    private func toggleRow(_ title: String, _ value: Binding<Bool>, last: Bool = false) -> some View {
        VStack(spacing: 0) {
            Toggle(title, isOn: value).tint(Theme.accent)
                .font(.system(size: 15.5))
                .padding(.horizontal, 16).padding(.vertical, 8)
            if !last { Divider().overlay(Theme.line).padding(.leading, 16) }
        }
    }
}
