import SwiftUI

/// 12 new session: pick an agent and say what to do. It starts in a new
/// cloud session ~/.fen/<word>-<word>; no Mac folder, project or model to choose.
struct NewSessionView: View {
    @Environment(SessionStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    var opened: (String) -> Void
    @State private var agent: AgentKind = .codex
    @State private var prompt = ""
    @State private var starting = false
    @State private var failure: String?
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Button { dismiss() } label: { Image(systemName: "xmark").font(.system(size: 15, weight: .medium)).frame(width: 36, height: 36) }
                    .glassEffect(.regular.interactive(), in: Circle())
                    .accessibilityLabel("Close")
                Spacer()
                Text("New session").font(.system(size: 16, weight: .semibold))
                Spacer()
                Color.clear.frame(width: 36, height: 36)
            }
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(AgentKind.allCases.filter { store.enabledAgents.contains($0) || $0 == .codex }) { kind in
                        Button { agent = kind } label: {
                            HStack(spacing: 7) {
                                AgentIcon(agent: kind, size: 18)
                                Text(kind.displayName).font(.system(size: 13, weight: .medium))
                            }
                            .padding(.leading, 9).padding(.trailing, 12).frame(height: 34)
                            .foregroundStyle(agent == kind ? Theme.fg : Theme.fg2)
                            .background(Theme.surface, in: Capsule())
                            .selectionOutline(agent == kind, radius: 17)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal, 1).padding(.vertical, 1)
            }
            HStack(alignment: .bottom, spacing: 8) {
                TextField("Ask \(agent.displayName)", text: $prompt, axis: .vertical)
                    .lineLimit(1...6)
                    .focused($focused)
                    .font(.system(size: 16))
                    .padding(.vertical, 8)
                    .onSubmit(start)
                Button(action: start) {
                    Group {
                        if starting { ProgressView().tint(Theme.onPrimary) }
                        else { Image(systemName: "arrow.up").font(.system(size: 15, weight: .semibold)) }
                    }
                    .frame(width: 36, height: 36)
                    .foregroundStyle(Theme.onPrimary)
                    .background(Theme.primary.opacity(prompt.isEmpty ? 0.35 : 1), in: Circle())
                }
                .disabled(prompt.isEmpty || starting)
                .accessibilityLabel("Start session")
            }
            .padding(.leading, 14).padding(.trailing, 6).padding(.vertical, 6)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            if let failure {
                Label(failure, systemImage: "exclamationmark.circle").font(.system(size: 13)).foregroundStyle(Theme.danger)
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 16)
        .padding(.bottom, 8)
        .presentationDetents([.height(failure == nil ? 210 : 240)])
        .presentationDragIndicator(.hidden)
        .onAppear { focused = true }
    }

    private func start() {
        let text = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !starting else { return }
        starting = true
        failure = nil
        Task {
            do { opened(try await store.launch(agent: agent, prompt: text)) }
            catch { failure = (error as NSError).localizedDescription }
            starting = false
        }
    }
}

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
                GroupHeader(title: "Coming soon").padding(.horizontal, -18)
                ListCard {
                    ListRow(title: "Gemini CLI", titleColor: Theme.fg3)
                    ListRow(title: "Copilot CLI", titleColor: Theme.fg3, showDivider: false)
                }
            }
            .padding(16)
        }
        .background(Theme.bg)
        .navigationTitle("Agents")
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
                        Text("Sign in on your Mac").font(.caption2Muted).foregroundStyle(Theme.accent)
                    } else if count > 0 {
                        Text(count == 1 ? "1 session" : "\(count) sessions").font(.caption2Muted).foregroundStyle(Theme.fg3)
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
                            Text(auth.profile?.displayName ?? "Runta account").font(.system(size: 16, weight: .medium))
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
                GroupHeader(title: "Notify me when").padding(.horizontal, -18)
                ListCard {
                    toggleRow("An agent needs approval", $notifyApproval)
                    toggleRow("An agent asks a question", $notifyQuestion)
                    toggleRow("A session finishes", $notifyFinished, last: true)
                }
                ListCard {
                    Button { auth.signOut() } label: {
                        ListRow(icon: "rectangle.portrait.and.arrow.right", iconColor: Theme.danger, title: "Sign out", titleColor: Theme.danger, showDivider: false)
                    }
                    .buttonStyle(.plain)
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
        .navigationTitle("Account")
        .navigationBarTitleDisplayMode(.inline)
        .task { await usage.loadBilling(auth.core) }
        .task {
            guard let core = store.core else { return }
            await usage.watchMetrics(core)
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
