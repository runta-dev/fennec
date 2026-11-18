import SwiftUI

enum Route: Hashable {
    case chat(String)
    case account
    case agents
}

/// 04 sessions, 16 empty / Mac offline. Grouped by state like the Mac panel;
/// the list scrolls under the glass navigation buttons and search bar.
struct HomeView: View {
    @Environment(SessionStore.self) private var store
    @Environment(RuntaAuth.self) private var auth
    @Environment(AppModel.self) private var app
    @State private var path: [Route] = []
    @State private var showSearch = false
    @State private var creatingSession = false
    @State private var creationFailed = false
    @State private var renaming: Session?
    @State private var stopping: Session?
    @State private var menuFor: Session?

    var body: some View {
        NavigationStack(path: $path) {
            Group {
                if store.sessions.isEmpty && store.loaded { empty }
                else if store.sessions.isEmpty { SessionListSkeleton() }
                else { list }
            }
            .refreshable { await store.refresh() }
            .task {
                while !Task.isCancelled {
                    await store.refresh()
                    try? await Task.sleep(for: .seconds(5))
                }
            }
            .background(Theme.bg)
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .top, spacing: 0) {
                VStack(spacing: 4) {
                    ConnectionIssueBanner(issue: store.issue,
                                          onSignIn: { auth.signOut() })
                        .padding(.horizontal, 16)
                    if !store.macOnline { offlineBanner }
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) { searchBar }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { path.append(.account) } label: {
                        Text(auth.profile?.initials.isEmpty == false ? auth.profile!.initials : L10n.text("ios.me"))
                            .font(.system(size: 13, weight: .medium))
                    }
                    .accessibilityLabel(L10n.text("ui.account"))
                }
                if app.environment.badge != nil {
                    ToolbarItem(placement: .principal) { EnvironmentBadge(environment: app.environment) }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Menu { agentChoices } label: {
                        if creatingSession { ProgressView() }
                        else { Image(systemName: "plus") }
                    }
                        .disabled(creatingSession)
                        .accessibilityLabel(L10n.text("ios.new_session"))
                        .accessibilityIdentifier("new-session-menu")
                }
            }
            .navigationDestination(for: Route.self) { route in
                switch route {
                case .chat(let id): ChatView(sessionID: id)
                case .account: AccountView(openAgents: { path.append(.agents) })
                case .agents: AgentsView()
                }
            }
            .sheet(isPresented: $showSearch) {
                SearchView { id in
                    showSearch = false
                    path.append(.chat(id))
                }
            }
            .alert(L10n.text("ios.create_failed"), isPresented: $creationFailed) {
                Button(L10n.text("ui.ok"), role: .cancel) {}
            }
            .sheet(item: $menuFor) { session in
                SessionMenu(session: session, onStop: { store.stop(session.id) })
                    .presentationDetents([.height(360)])
            }
        }
        .tint(Theme.fg)
    }

    @ViewBuilder
    private var agentChoices: some View {
        ForEach(AgentKind.allCases.filter { store.enabledAgents.contains($0) || $0 == .codex }) { agent in
            Button { createSession(agent) } label: {
                Label { Text(agent.displayName) } icon: { AgentIcon(agent: agent, size: 20) }
            }
            .accessibilityIdentifier("create-agent-\(agent.rawValue)")
        }
    }

    private func createSession(_ agent: AgentKind) {
        guard !creatingSession else { return }
        creatingSession = true
        Task {
            defer { creatingSession = false }
            do { path.append(.chat(try await store.launch(agent: agent))) }
            catch { creationFailed = true }
        }
    }

    private var list: some View {
        List {
            ForEach(store.groups, id: \.0) { state, sessions in
                Section {
                    ForEach(sessions) { session in
                        Button { path.append(.chat(session.id)) } label: { SessionRow(session: session) }
                            .buttonStyle(.plain)
                            .listRowInsets(EdgeInsets(top: 0, leading: 0, bottom: 0, trailing: 0))
                            .listRowSeparator(.hidden)
                            .listRowBackground(Theme.bg)
                            .swipeActions(edge: .trailing) {
                                Button(role: .destructive) { store.stop(session.id) } label: { Label(L10n.text("ui.stop"), systemImage: "stop.fill") }
                                    .tint(Theme.danger)
                                Button { store.togglePin(session.id) } label: {
                                    Label(session.pinned ? L10n.text("ios.unpin") : L10n.text("ios.pin"), systemImage: session.pinned ? "pin.slash" : "pin")
                                }
                                .tint(Theme.accent)
                            }
                            .contextMenu {
                                Button(L10n.text("ios.session_menu"), systemImage: "ellipsis.circle") { menuFor = session }
                            }
                    }
                } header: {
                    GroupHeader(title: state.label, count: sessions.count)
                        .padding(.horizontal, -18)
                        .textCase(nil)
                }
            }
        }
        .listStyle(.plain)
        // Plain lists add a top margin and space around sections; the group
        // headers carry their own spacing, so the list adds none.
        .listSectionSpacing(0)
        .contentMargins(.top, 0, for: .scrollContent)
        .environment(\.defaultMinListHeaderHeight, 0)
        .environment(\.defaultMinListRowHeight, 0)
        .scrollContentBackground(.hidden)
    }

    private var empty: some View {
        VStack(spacing: 10) {
            Spacer()
            FoxMark(size: 56)
            Text(L10n.text("ios.no_sessions")).font(.system(size: 18, weight: .medium)).padding(.top, 6)
            Menu { agentChoices } label: { Text(L10n.text("ios.new_session")) }
                .disabled(creatingSession)
                .buttonStyle(PrimaryButtonStyle())
                .frame(width: 180)
                .padding(.top, 8)
            Spacer()
        }
        .frame(maxWidth: .infinity)
    }

    private var offlineBanner: some View {
        HStack(spacing: 10) {
            Image(systemName: "laptopcomputer").foregroundStyle(Theme.fg3)
            Text(L10n.text("ios.mac_offline")).font(.system(size: 14, weight: .medium))
            Spacer()
        }
        .padding(12)
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .padding(.horizontal, 16)
        .padding(.bottom, 4)
    }

    private var searchBar: some View {
        Button { showSearch = true } label: {
            HStack(spacing: 10) {
                Image(systemName: "magnifyingglass")
                Text(L10n.text("ios.search"))
                Spacer()
            }
            .font(.system(size: 17))
            .foregroundStyle(Theme.fg2)
            .padding(.horizontal, 18)
            .frame(height: 50)
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .glassEffect(.regular.interactive(), in: Capsule())
        .padding(.horizontal, 16)
        .padding(.bottom, 4)
    }
}

struct SessionRow: View {
    var session: Session
    var subtitle: String?

    var body: some View {
        HStack(spacing: 13) {
            AgentIcon(agent: session.agent, state: session.state)
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    if session.pinned { Image(systemName: "pin.fill").font(.system(size: 10)).foregroundStyle(Theme.fg3) }
                    Text(session.title).font(.rowTitle).lineLimit(1)
                    Spacer(minLength: 4)
                    Text(shortTime(session.updatedAt)).font(.system(size: 12.5)).foregroundStyle(Theme.fg3).monospacedDigit()
                }
                Text(subtitle ?? session.preview).font(.rowBody).foregroundStyle(Theme.fg2).lineLimit(1)
            }
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 9)
        .contentShape(Rectangle())
    }
}


/// Placeholder rows shown while the first session list loads. Same geometry as
/// SessionRow and GroupHeader, so nothing moves when the real rows arrive.
struct SessionListSkeleton: View {
    private let groups = [2, 3]
    private let titleWidths: [CGFloat] = [0.62, 0.48, 0.7, 0.55, 0.4]

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(groups.enumerated()), id: \.offset) { group, count in
                SkeletonBar(width: 70, height: 11).padding(.horizontal, 18).padding(.top, 18).padding(.bottom, 8)
                ForEach(0..<count, id: \.self) { row in
                    let index = (group * 2 + row) % titleWidths.count
                    HStack(spacing: 13) {
                        Circle().fill(Theme.surface2).frame(width: 30, height: 30)
                        VStack(alignment: .leading, spacing: 8) {
                            GeometryReader { geometry in
                                HStack {
                                    SkeletonBar(width: geometry.size.width * titleWidths[index], height: 13)
                                    Spacer()
                                    SkeletonBar(width: 34, height: 10)
                                }
                            }
                            .frame(height: 13)
                            GeometryReader { geometry in
                                SkeletonBar(width: geometry.size.width * 0.85, height: 11)
                            }
                            .frame(height: 11)
                        }
                    }
                    .padding(.horizontal, 18)
                    .padding(.vertical, 12)
                }
            }
            Spacer(minLength: 0)
        }
        .shimmer()
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .accessibilityElement()
        .accessibilityLabel(L10n.text("ios.loading_sessions"))
    }
}

struct SkeletonBar: View {
    var width: CGFloat
    var height: CGFloat
    var body: some View {
        RoundedRectangle(cornerRadius: height / 2, style: .continuous)
            .fill(Theme.surface2)
            .frame(width: max(0, width), height: height)
    }
}

/// A soft highlight sweeping left to right; static when Reduce Motion is on.
struct Shimmer: ViewModifier {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var phase: CGFloat = -1

    func body(content: Content) -> some View {
        content
            .overlay {
                if !reduceMotion {
                    GeometryReader { geometry in
                        let width = geometry.size.width
                        LinearGradient(colors: [.clear, Theme.fg.opacity(0.08), .clear],
                                       startPoint: .leading, endPoint: .trailing)
                            .frame(width: width * 0.6)
                            .offset(x: phase * width * 1.6 - width * 0.3)
                    }
                    .mask(content)
                    .allowsHitTesting(false)
                }
            }
            .onAppear {
                guard !reduceMotion else { return }
                withAnimation(.linear(duration: 1.3).repeatForever(autoreverses: false)) { phase = 1 }
            }
    }
}

/// Text that shimmers like Codex's "Working for 1m 5s": dim text with a bright
/// band sweeping through the glyphs. Driven by the clock, not view state, so a
/// label that changes every second (the elapsed time) doesn't restart it.
struct TextShimmer: ViewModifier {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.scenePhase) private var scenePhase
    @State private var started = Date.now

    func body(content: Content) -> some View {
        if reduceMotion || scenePhase != .active {
            content.foregroundStyle(Theme.fg2)
        } else {
            // ChatGPT.app's Codex cadencedShimmer: 600 ms lead-in, a 1 s
            // sweep in 48 steps, repeated every 4 s; mask peaks at 20–30%.
            TimelineView(.animation(minimumInterval: 1.0 / 48)) { context in
                let elapsed = context.date.timeIntervalSince(started) - 0.6
                let phase = max(0, elapsed).truncatingRemainder(dividingBy: 4)
                let progress = floor(min(phase, 1) * 48) / 48
                content.foregroundStyle(Theme.fg2.opacity(0.55))
                    .overlay {
                        content.foregroundStyle(Theme.fg.opacity(0.75))
                            .mask {
                                GeometryReader { geometry in
                                    LinearGradient(stops: [
                                        .init(color: .clear, location: 0),
                                        .init(color: .white, location: 0.2),
                                        .init(color: .white, location: 0.3),
                                        .init(color: .clear, location: 0.5),
                                        .init(color: .clear, location: 1)
                                    ], startPoint: .leading, endPoint: .trailing)
                                    .offset(x: geometry.size.width * (-0.5 + 1.75 * progress))
                                }
                            }
                            .opacity(elapsed >= 0 && phase < 1 ? 1 : 0)
                            .accessibilityHidden(true)
                    }
            }
            .onAppear { started = .now }
        }
    }
}

extension View {
    func shimmer() -> some View { modifier(Shimmer()) }
    func textShimmer() -> some View { modifier(TextShimmer()) }
}
