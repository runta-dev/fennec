import SwiftUI

enum QueueRoute: Hashable {
    case thread(String, Int)
    case done(String)
    case settings(String)
    case workers(String)
    case tags(String)
    case knowledge(String)
    case concept(String, String)
    case chat(String)
    /// The blank page of a new request for an agent (FEN-110).
    case newRequest(String, AgentKind)
    case account
    case agents
}

/// Destinations of the home screen's navigation stack.
struct QueueDestinations: ViewModifier {
    @Binding var path: [QueueRoute]
    func body(content: Content) -> some View {
        content.navigationDestination(for: QueueRoute.self) { route in
            switch route {
            case let .thread(project, number): RequestThreadView(projectID: project, number: number, path: $path)
            case .done(let project): DoneView(projectID: project, path: $path)
            case .settings(let project): QueueSettingsView(projectID: project, path: $path)
            case .workers(let project): QueueWorkersView(projectID: project)
            case .tags(let project): QueueTagsView(projectID: project)
            case .knowledge(let project): KnowledgeView(projectID: project, path: $path)
            case let .concept(project, id): ConceptView(projectID: project, conceptID: id, path: $path)
            case .chat(let id): ChatView(sessionID: id)
            case let .newRequest(project, agent): NewRequestView(projectID: project, agent: agent, path: $path)
            case .account: AccountView(openAgents: { path.append(.agents) })
            case .agents: AgentsView()
            }
        }
    }
}

/// 18 Home: the open queue's requests grouped like the desktop board. The
/// app's only list: sessions are opened from a thread (FEN-108).
struct QueuesView: View {
    @Environment(QueueStore.self) private var queues
    @Environment(SessionStore.self) private var store
    @Environment(RuntaAuth.self) private var auth
    @State private var path: [QueueRoute] = []
    @State private var searching = false
    /// The agent home's + starts a request with (FEN-184).
    @AppStorage("newRequestAgent") private var newRequestAgent = ""
    @State private var reordering = false
    @State private var creatingQueue = false
    @State private var linking: BoardRequest?
    @State private var refreshTick = 0
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        NavigationStack(path: $path) {
            Group {
                if !queues.loaded { ListSkeleton() }
                // FEN-231: the board always keeps a project (the starter, with
                // Fennec's icon), so none yet means it hasn't arrived.
                else if queues.projects.isEmpty { ListSkeleton() }
                else if let project = queues.selected { list(project) }
            }
            .pageBackground()
            .refreshable { await queues.refresh() }
            .task(id: refreshTick) {
                // FEN-60: the board pushes changes over one stream while the
                // app is in front (QueueStore.startWatching). Read once now
                // anyway, then poll only as a fallback: rarely while pushes
                // arrive, otherwise every 2 s on the list and 5 s under a
                // thread. Back from the background, or back on the list,
                // restarts this, so nothing waits for the next tick.
                guard scenePhase == .active else { return }
                queues.startWatching()
                while !Task.isCancelled {
                    if !queues.live || path.isEmpty { await queues.refresh() }
                    // The board can't be read: ask the cloud client why
                    // (signed out, computer stopped, no network), for the banner.
                    if queues.unavailable { await store.refresh(transcripts: false) }
                    // Which agents the + menu offers (FEN-110, as FEN-73 did
                    // for new sessions): at most once a minute.
                    Task { await store.loadCloudAgentsIfStale() }
                    try? await Task.sleep(for: .seconds(queues.live ? 30 : path.isEmpty ? 2 : 5))
                    queues.startWatching() // reopens a stream that went quiet
                }
            }
            .onChange(of: scenePhase) { _, phase in
                // The stream closes a little after the app leaves the screen
                // (BackgroundGrace in FennecApp, FEN-234), not here.
                if phase == .active { refreshTick += 1 }
            }
            .onChange(of: path.isEmpty) { _, onList in if onList { refreshTick += 1 } }
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .top, spacing: 0) { banners }
            .safeAreaInset(edge: .bottom, spacing: 0) { if !queues.projects.isEmpty { bottomBar } }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { path.append(.account) } label: {
                        Text(auth.profile?.initials.isEmpty == false ? auth.profile!.initials : L10n.text("ios.me"))
                            .font(.system(size: 13, weight: .medium))
                    }
                    .accessibilityLabel(L10n.text("ui.account"))
                }
                ToolbarItem(placement: .principal) { queueMenu }
                ToolbarItem(placement: .topBarTrailing) {
                    Button { searching = true } label: { Image(systemName: "magnifyingglass").regularIconWeight() }
                        .disabled(queues.projects.isEmpty)
                        .accessibilityLabel(L10n.text("ios.search_requests"))
                        .accessibilityIdentifier("search-requests")
                }
            }
            .modifier(QueueDestinations(path: $path))
            .onChange(of: queues.unsent) { _, unsent in
                // A new request the board refused (FEN-58): leave its thread
                // and open its blank page again with it.
                guard let unsent else { return }
                path.removeAll { $0 == .thread(unsent.project, unsent.number) }
                let agent = unsent.agent ?? queues.project(unsent.project)?.firstAgent ?? .codex
                path.append(.newRequest(unsent.project, agent))
            }
            .sheet(isPresented: $reordering) {
                if let project = queues.selected { ReorderView(project: project) }
            }
            .sheet(isPresented: $creatingQueue) { NewQueueSheet() }
            .sheet(isPresented: $searching) {
                RequestSearchSheet { route in
                    searching = false
                    path.append(route)
                }
            }
            .sheet(item: $linking) { request in
                if let project = queues.selected { LinkSheet(project: project, request: request) }
            }
            .queueActionAlert()
        }
        .tint(Theme.fg)
    }

    // MARK: New request (33)

    /// The agent a new request runs with: the one last picked at the bottom
    /// left, while it's still offered, else the first offered (FEN-184).
    private var currentAgent: AgentKind? {
        let agents = store.newSessionAgents
        return agents.first { $0.rawValue == newRequestAgent } ?? agents.first
    }

    /// Bottom bar within thumb reach (FEN-184): the agent picker on the left
    /// (switching agents is rare), + on the right opens the picked agent's
    /// blank page in one tap. Search moved to the top right.
    private var bottomBar: some View {
        HStack(spacing: 12) {
            agentPicker
            Spacer(minLength: 0)
            Button {
                if let project = queues.selected, let agent = currentAgent { path.append(.newRequest(project.id, agent)) }
            } label: {
                Image(systemName: "plus").font(.system(size: 22, weight: .medium))
                    .foregroundStyle(Theme.fg)
                    .frame(width: 50, height: 50)
                    .contentShape(Circle())
            }
            .buttonStyle(.plain)
            .glassEffect(.regular.interactive(), in: Circle())
            .disabled(queues.selected == nil || currentAgent == nil)
            .accessibilityLabel(L10n.text("desktop.new_request"))
            .accessibilityIdentifier("new-request")
        }
        .padding(.horizontal, 16)
        .padding(.bottom, 4)
    }

    /// Tapping it pops up the agents ready on the cloud computer (FEN-110,
    /// FEN-73); the pick is remembered for the next +.
    private var agentPicker: some View {
        Menu {
            let agents = store.newSessionAgents
            ForEach(agents) { agent in
                Button { newRequestAgent = agent.rawValue } label: {
                    if agent == currentAgent { Label(agent.displayName, systemImage: "checkmark") }
                    else { Label { Text(agent.displayName) } icon: { AgentIcon(agent: agent, size: 20) } }
                }
                .accessibilityIdentifier("new-request-agent-\(agent.rawValue)")
            }
            if agents.isEmpty {
                Text(L10n.text("ios.no_cloud_agents"))
            }
        } label: {
            HStack(spacing: 8) {
                if let agent = currentAgent {
                    AgentIcon(agent: agent, size: 22)
                    Text(agent.displayName).lineLimit(1)
                } else {
                    Text(L10n.text("ios.no_cloud_agents")).lineLimit(1)
                }
                Image(systemName: "chevron.up.chevron.down").regularIconWeight().font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(Theme.fg3)
            }
            .font(.system(size: 16, weight: .medium))
            .foregroundStyle(Theme.fg)
            .padding(.horizontal, 16)
            .frame(height: 50)
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .glassEffect(.regular.interactive(), in: Capsule())
        .accessibilityIdentifier("agent-picker")
    }

    // MARK: Queue menu (19)

    private var queueMenu: some View {
        Menu {
            Section {
                ForEach(queues.projects) { project in
                    Button { queues.selectedID = project.id; Task { await queues.refresh() } } label: {
                        if project.id == queues.selected?.id { Label(project.name, systemImage: "checkmark") }
                        else { Text(project.name) }
                        Text(project.paused ? L10n.text("desktop.paused") : projectSummary(project))
                    }
                }
                Button(L10n.text("desktop.new_project"), systemImage: "plus") { creatingQueue = true }
            }
            if let project = queues.selected {
                Section {
                    Picker(selection: Binding(get: { queues.tagFilter ?? "" }, set: { queues.tagFilter = $0.isEmpty ? nil : $0 })) {
                        Text(L10n.text("desktop.all_tags")).tag("")
                        ForEach(project.tags) { Text($0.label).tag($0.id) }
                    } label: { Label(L10n.text("ios.tag"), systemImage: "tag") }
                        .pickerStyle(.menu)
                    Button(L10n.text("ios.reorder_queued"), systemImage: "arrow.up.arrow.down") { reordering = true }
                        .disabled(queues.list(project.id).filter { $0.status_ == .queued }.count < 2)
                }
                Section {
                    Button(project.paused ? L10n.text("desktop.resume_queue") : L10n.text("desktop.pause_queue"),
                           systemImage: project.paused ? "play" : "pause") {
                        Task { await queues.setPaused(project.id, !project.paused) }
                    }
                    Button(L10n.text("desktop.knowledge"), systemImage: "book") { path.append(.knowledge(project.id)) }
                    Button(L10n.text("desktop.project_settings"), systemImage: "gearshape") { path.append(.settings(project.id)) }
                }
            }
        } label: {
            VStack(spacing: 0) {
                HStack(spacing: 3) {
                    Text(queues.selected?.name ?? L10n.text("ios.queues")).font(.system(size: 15, weight: .semibold))
                    Image(systemName: "chevron.down").regularIconWeight().font(.system(size: 10, weight: .semibold))
                }
                if let project = queues.selected {
                    Text(project.paused ? L10n.text("desktop.paused") : projectSummary(project))
                        .font(.system(size: 11)).foregroundStyle(Theme.fg3).monospacedDigit()
                }
            }
            .foregroundStyle(Theme.fg)
        }
        .accessibilityIdentifier("queue-menu")
    }

    private func projectSummary(_ project: BoardProject) -> String {
        let list = queues.list(project.id)
        let working = list.filter { $0.status_.running }.count
        return L10n.text("desktop.queued_busy", values: ["working": String(min(working, project.queue)), "limit": String(project.queue)])
    }

    // MARK: List (18, 20, 21)

    private func list(_ project: BoardProject) -> some View {
        let all = queues.list(project.id).filter { queues.tagFilter == nil || $0.tag == queues.tagFilter }
        let needs = all.filter { $0.status_.needsYou }.sorted { $0.updatedDate > $1.updatedDate }
        let running = all.filter { $0.status_.running }.sorted { $0.updatedDate > $1.updatedDate }
        let queued = all.filter { $0.status_ == .queued }
        let done = all.filter { $0.status_ == .resolved }
        let full = queues.list(project.id)
        return List {
            if project.paused { pausedBanner(project) }
            if all.isEmpty || (needs.isEmpty && running.isEmpty && queued.isEmpty) {
                Text(L10n.text("desktop.queue_empty")).font(.rowBody).foregroundStyle(Theme.fg2)
                    .frame(maxWidth: .infinity).padding(.vertical, 40)
                    .listRowSeparator(.hidden).listRowBackground(Theme.bg)
            }
            section(L10n.text("desktop.needs_you"), needs, project) { r in r.latestLine }
            section(L10n.text("desktop.working"), running, project) { r in r.latestLine }
            section(L10n.text("desktop.queued"), queued, project) { r in
                QueueWait.of(full, number: r.number, limit: project.queue, paused: project.paused).text
            }
            Button { path.append(.done(project.id)) } label: {
                HStack(spacing: 6) {
                    Text(L10n.text("desktop.done")).foregroundStyle(Theme.fg2)
                    Text("\(done.count)").foregroundStyle(Theme.fg3).monospacedDigit()
                    Spacer()
                    Image(systemName: "chevron.right").regularIconWeight().font(.system(size: 13)).foregroundStyle(Theme.fg3)
                }
                .font(.system(size: 14.5))
                .padding(.horizontal, 18).padding(.vertical, 12)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Theme.bg)
            .accessibilityIdentifier("queue-done")
        }
        .listStyle(.plain)
        .listSectionSpacing(0)
        .contentMargins(.top, 0, for: .scrollContent)
        .environment(\.defaultMinListHeaderHeight, 0)
        .environment(\.defaultMinListRowHeight, 0)
        .scrollContentBackground(.hidden)
        .animation(.snappy, value: all.map(\.number))
    }

    @ViewBuilder
    private func section(_ title: String, _ rows: [BoardRequest], _ project: BoardProject, line: @escaping (BoardRequest) -> String) -> some View {
        if !rows.isEmpty {
            Section {
                ForEach(rows) { r in
                    Button { path.append(.thread(project.id, r.number)) } label: {
                        RequestRow(request: r, project: project, line: line(r))
                    }
                    .buttonStyle(.plain)
                    .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Theme.bg)
                    .requestActions(r, project: project, link: { linking = r })
                }
            } header: {
                GroupHeader(title: title, count: rows.count).padding(.horizontal, -18).textCase(nil)
            }
        }
    }

    private func pausedBanner(_ project: BoardProject) -> some View {
        HStack(spacing: 10) {
            Image(systemName: "pause.fill").regularIconWeight().foregroundStyle(Theme.fg2)
            VStack(alignment: .leading, spacing: 1) {
                Text(L10n.text("desktop.queued_paused")).font(.system(size: 14, weight: .medium))
                Text(L10n.text("ios.queue_paused_hint")).font(.system(size: 12.5)).foregroundStyle(Theme.fg2)
            }
            Spacer()
            Button(L10n.text("ios.resume")) { Task { await queues.setPaused(project.id, false) } }
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(Theme.onPrimary)
                .padding(.horizontal, 14).frame(height: 32)
                .background(Theme.primary, in: Capsule())
                .buttonStyle(.plain)
                .accessibilityIdentifier("queue-resume")
        }
        .padding(12)
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .padding(.horizontal, 16).padding(.vertical, 4)
        .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Theme.bg)
    }

    // MARK: Banners (38)

    @ViewBuilder
    private var banners: some View {
        if queues.unavailable, let issue = store.issue {
            ConnectionIssueBanner(issue: issue, onSignIn: { auth.signOut() })
                .padding(.horizontal, 16).padding(.bottom, 4)
        } else if queues.unavailable {
            QueueBanner(icon: queues.lastSync == nil ? "icloud.slash" : "arrow.triangle.2.circlepath",
                        text: queues.lastSync.map { L10n.text("ios.queue_stale", values: ["age": age($0)]) }
                            ?? L10n.text("desktop.board_unavailable"))
        }
    }

    private func age(_ date: Date) -> String {
        let minutes = Int(Date.now.timeIntervalSince(date) / 60)
        if minutes < 60 { return L10n.text("desktop.minutes", values: ["n": String(max(minutes, 1))]) }
        return L10n.text("desktop.hours", values: ["n": String(minutes / 60)])
    }
}

struct QueueBanner: View {
    var icon: String
    var text: String
    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: icon).regularIconWeight().foregroundStyle(Theme.fg3)
            Text(text).font(.system(size: 13.5)).foregroundStyle(Theme.fg2)
            Spacer(minLength: 0)
        }
        .padding(12)
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .padding(.horizontal, 16).padding(.bottom, 4)
    }
}

extension BoardRequest {
    /// The second line of a row: who said what last.
    var latestLine: String {
        if status_ == .failed, let failure, !failure.isEmpty {
            return "\(status_.label) · \(failure == "The agent stopped without reporting back" ? L10n.text("desktop.stalled") : failure)"
        }
        let text = (latest?.body ?? "").replacingOccurrences(of: "\n", with: " ")
        return status_.needsYou ? "\(status_.label) · \(text)" : text
    }
}

/// One request: agent icon (or a clock while queued; no status dot once done), title and time, then reference and latest line.
struct RequestRow: View {
    var request: BoardRequest
    var project: BoardProject
    var line: String

    var body: some View {
        HStack(spacing: 13) {
            RequestIcon(request: request, project: project)
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline, spacing: 7) {
                    Text(request.title).font(.rowTitle).lineLimit(1)
                    Spacer(minLength: 4)
                    Text(shortTime(request.updatedDate)).font(.system(size: 12.5)).foregroundStyle(Theme.fg3).monospacedDigit()
                }
                // The reference leads the second line so the title gets the full
                // width of the first one on a phone (FEN-54).
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(request.reference(project.key)).font(.system(size: 11.5, design: .monospaced)).foregroundStyle(Theme.fg3)
                        .fixedSize()
                    Text(line).font(.rowBody).foregroundStyle(Theme.fg2).lineLimit(1)
                }
            }
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 9)
        .contentShape(Rectangle())
        .accessibilityIdentifier("request-\(request.number)")
    }
}

struct RequestIcon: View {
    var request: BoardRequest
    var project: BoardProject
    var size: CGFloat = 30

    var body: some View {
        switch request.status_ {
        case .queued:
            tile(Image(systemName: "clock"), Theme.fg2)
        default:
            if let agent = request.agentKind ?? project.firstAgent {
                // Status dot at the bottom-right, like the session list's StatusDot (FEN-66).
                // A done request shows the plain agent icon: the Done page already
                // says it is done, so a checkmark on every row repeats it (FEN-116).
                AgentIcon(agent: agent, size: size)
                    .overlay(alignment: .bottomTrailing) {
                        if request.status_ != .resolved {
                            Circle().fill(request.status_.color).frame(width: 7, height: 7)
                                .background(Circle().stroke(Theme.bg, lineWidth: 4))
                        }
                    }
            } else if request.status_ == .resolved {
                tile(Image(systemName: "checkmark"), Theme.working)
            } else {
                tile(Image(systemName: "circle.fill"), request.status_.color)
            }
        }
    }

    private func tile(_ image: Image, _ color: Color) -> some View {
        image.font(.system(size: size * 0.45, weight: .medium)).regularIconWeight().foregroundStyle(color)
            .frame(width: size, height: size)
            .background(Theme.surface, in: RoundedRectangle(cornerRadius: size * 0.3, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: size * 0.3, style: .continuous).strokeBorder(Theme.line))
    }
}

/// Swipes and the long-press menu of a request row (21).
private struct RequestActions: ViewModifier {
    @Environment(QueueStore.self) private var queues
    var request: BoardRequest
    var project: BoardProject
    var link: () -> Void

    func body(content: Content) -> some View {
        content
            .swipeActions(edge: .trailing) {
                if request.status_.canMarkDone {
                    Button { Task { await queues.markDone(project.id, request.number) } } label: {
                        Label(L10n.text("desktop.resolve"), systemImage: "checkmark")
                    }.tint(Theme.working)
                } else if request.status_ == .resolved {
                    Button { Task { await queues.reopen(project.id, request.number) } } label: {
                        Label(L10n.text("desktop.reopen"), systemImage: "arrow.uturn.backward")
                    }.tint(Theme.accent)
                }
            }
            .swipeActions(edge: .leading) {
                if request.status_ == .queued {
                    Button { Task { await queues.move(project.id, request.number, before: 0) } } label: {
                        Label(L10n.text("desktop.run_next"), systemImage: "arrow.up")
                    }.tint(Theme.accent)
                }
            }
            .contextMenu {
                RequestMenuItems(request: request, project: project, link: link)
            }
    }
}

/// The actions a request offers, by status (21, 30).
struct RequestMenuItems: View {
    @Environment(QueueStore.self) private var queues
    var request: BoardRequest
    var project: BoardProject
    var link: () -> Void

    var body: some View {
        if request.status_ == .queued {
            Button(L10n.text("desktop.run_next"), systemImage: "arrow.up") {
                Task { await queues.move(project.id, request.number, before: 0) }
            }
        }
        Menu {
            ForEach(project.tags) { tag in
                Button { Task { await queues.setTag(project.id, request.number, tag.id) } } label: {
                    if tag.id == request.tag { Label(tag.label, systemImage: "checkmark") } else { Text(tag.label) }
                }
            }
        } label: { Label(L10n.text("ios.tag"), systemImage: "tag") }
        Button(L10n.text("ios.link_to_request"), systemImage: "link") { link() }
        Button(L10n.text("ios.copy_reference", values: ["ref": request.reference(project.key)]), systemImage: "doc.on.doc") {
            UIPasteboard.general.string = request.reference(project.key)
        }
        if request.status_.canMarkDone {
            Divider()
            Button(L10n.text("desktop.resolve"), systemImage: "checkmark") { Task { await queues.markDone(project.id, request.number) } }
        } else if request.status_ == .resolved {
            Divider()
            Button(L10n.text("desktop.reopen"), systemImage: "arrow.uturn.backward") { Task { await queues.reopen(project.id, request.number) } }
        }
    }
}

extension View {
    func requestActions(_ request: BoardRequest, project: BoardProject, link: @escaping () -> Void) -> some View {
        modifier(RequestActions(request: request, project: project, link: link))
    }

    /// Shows what the last queue change failed with.
    func queueActionAlert() -> some View { modifier(QueueActionAlert()) }
}

private struct QueueActionAlert: ViewModifier {
    @Environment(QueueStore.self) private var queues
    func body(content: Content) -> some View {
        content.alert(L10n.text("desktop.action_failed"), isPresented: Binding(
            get: { queues.actionError != nil }, set: { if !$0 { queues.actionError = nil } })) {
            Button(L10n.text("ui.ok"), role: .cancel) {}
        } message: { Text(queues.actionError ?? "") }
    }
}

// MARK: Reorder (22)

struct ReorderView: View {
    @Environment(QueueStore.self) private var queues
    @Environment(\.dismiss) private var dismiss
    var project: BoardProject
    @State private var order: [BoardRequest] = []

    var body: some View {
        NavigationStack {
            List {
                Section {
                    ForEach(order) { r in
                        RequestRow(request: r, project: project, line: QueueWait.of(currentList, number: r.number, limit: project.queue, paused: project.paused).text)
                            .listRowInsets(EdgeInsets()).listRowBackground(Theme.bg)
                    }
                    .onMove(perform: move)
                } header: {
                    Text(L10n.text("ios.reorder_hint")).font(.system(size: 12.5)).foregroundStyle(Theme.fg3).textCase(nil)
                }
            }
            .listStyle(.plain)
            .environment(\.editMode, .constant(.active))
            .scrollContentBackground(.hidden)
            .pageBackground()
            .navigationTitle(L10n.text("ios.reorder"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button(L10n.text("ui.done")) { dismiss() } }
            }
        }
        .onAppear { order = queues.list(project.id).filter { $0.status_ == .queued } }
    }

    /// The list with this screen's order, for the place-in-line text.
    private var currentList: [BoardRequest] {
        let others = queues.list(project.id).filter { $0.status_ != .queued }
        return others + order
    }

    private func move(_ from: IndexSet, _ to: Int) {
        guard let index = from.first else { return }
        let moved = order[index]
        let before = order
        order.move(fromOffsets: from, toOffset: to)
        guard let at = order.firstIndex(of: moved) else { return }
        Task {
            if at + 1 < order.count {
                // In front of the one that now follows it (or to the front).
                await queues.move(project.id, moved.number, before: at == 0 ? 0 : order[at + 1].number)
            } else {
                // To the end: the ones that were behind it move in front of it.
                let behind = before.drop { $0.number != moved.number }.dropFirst()
                for r in behind { await queues.move(project.id, r.number, before: moved.number) }
            }
        }
    }
}

// MARK: Done (23)

struct DoneView: View {
    @Environment(QueueStore.self) private var queues
    var projectID: String
    @Binding var path: [QueueRoute]
    @State private var tag: String?
    @State private var linking: BoardRequest?

    var body: some View {
        let project = queues.project(projectID)
        let done = queues.list(projectID).filter { $0.status_ == .resolved && (tag == nil || $0.tag == tag) }
            .sorted { $0.updatedDate > $1.updatedDate }
        List {
            if let project {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        chip(L10n.text("desktop.all"), count: queues.list(projectID).filter { $0.status_ == .resolved }.count, on: tag == nil) { tag = nil }
                        ForEach(project.tags) { t in chip(t.label, count: nil, on: tag == t.id) { tag = t.id } }
                    }
                    .padding(.horizontal, 16).padding(.vertical, 4)
                }
                .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Theme.bg)
                if done.isEmpty {
                    Text(L10n.text("desktop.nothing_resolved")).font(.rowBody).foregroundStyle(Theme.fg2)
                        .frame(maxWidth: .infinity).padding(.vertical, 40)
                        .listRowSeparator(.hidden).listRowBackground(Theme.bg)
                }
                ForEach(done) { r in
                    Button { path.append(.thread(projectID, r.number)) } label: {
                        RequestRow(request: r, project: project, line: (r.latest?.body ?? "").replacingOccurrences(of: "\n", with: " "))
                    }
                    .buttonStyle(.plain)
                    .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Theme.bg)
                    .requestActions(r, project: project, link: { linking = r })
                }
            }
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
        .pageBackground()
        .navigationTitle(L10n.text("desktop.done"))
        .navigationBarTitleDisplayMode(.inline)
        .sheet(item: $linking) { r in if let project { LinkSheet(project: project, request: r) } }
    }

    private func chip(_ title: String, count: Int?, on: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 6) {
                Text(title)
                if let count { Text("\(count)").foregroundStyle(Theme.accent).monospacedDigit() }
            }
            .font(.system(size: 13, weight: .medium))
            .foregroundStyle(on ? Theme.bg : Theme.fg2)
            .padding(.horizontal, 13).padding(.vertical, 7)
            .background(on ? Theme.fg : Theme.surface, in: Capsule())
        }
        .buttonStyle(.plain)
    }
}

// MARK: Search (24)

/// Requests and thread messages in every queue, from home's search bar.
struct RequestSearchSheet: View {
    var open: (QueueRoute) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    RequestSearchResults(query: query, open: open)
                }
            }
            .pageBackground()
            .navigationTitle(L10n.text("ios.search"))
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always),
                        prompt: L10n.text("ios.search_requests"))
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button(L10n.text("desktop.cancel")) { dismiss() } }
            }
        }
    }
}

struct RequestSearchResults: View {
    @Environment(QueueStore.self) private var queues
    var query: String
    var open: (QueueRoute) -> Void

    var body: some View {
        let q = query.trimmingCharacters(in: .whitespaces)
        let hits = results(q)
        if !hits.requests.isEmpty {
            GroupHeader(title: L10n.text("ios.requests"), count: hits.requests.count)
            ForEach(hits.requests, id: \.1.number) { project, r in
                Button { open(.thread(project.id, r.number)) } label: {
                    RequestRow(request: r, project: project, line: "\(project.name) · \(r.status_.label)")
                }
                .buttonStyle(.plain)
            }
        }
        if !hits.messages.isEmpty {
            GroupHeader(title: L10n.text("ios.in_threads"), count: hits.messages.count)
            ForEach(Array(hits.messages.enumerated()), id: \.offset) { _, hit in
                Button { open(.thread(hit.0.id, hit.1.number)) } label: {
                    HStack(alignment: .top, spacing: 13) {
                        Text(hit.1.reference(hit.0.key)).font(.system(size: 11.5, design: .monospaced)).foregroundStyle(Theme.fg3)
                            .frame(width: 58, alignment: .leading).padding(.top, 2)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(hit.2).font(.system(size: 14)).foregroundStyle(Theme.fg).lineLimit(3)
                            Text(hit.1.title).font(.system(size: 12)).foregroundStyle(Theme.fg3).lineLimit(1)
                        }
                        Spacer(minLength: 0)
                    }
                    .padding(.horizontal, 18).padding(.vertical, 8).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func results(_ q: String) -> (requests: [(BoardProject, BoardRequest)], messages: [(BoardProject, BoardRequest, String)]) {
        guard !q.isEmpty else { return ([], []) }
        let digits = q.uppercased().split(separator: "-").last.map(String.init) ?? q
        var requests: [(BoardProject, BoardRequest)] = []
        var messages: [(BoardProject, BoardRequest, String)] = []
        for project in queues.projects {
            for r in queues.list(project.id) {
                if r.title.localizedCaseInsensitiveContains(q) || String(r.number) == digits
                    || r.reference(project.key).localizedCaseInsensitiveContains(q) {
                    requests.append((project, r))
                }
                let thread = queues.thread(project.id, r.number)
                for message in thread?.messages ?? [r.latest].compactMap({ $0 }) where message.body.localizedCaseInsensitiveContains(q) {
                    messages.append((project, r, snippet(message.body, q)))
                }
            }
        }
        return (Array(requests.prefix(30)), Array(messages.prefix(30)))
    }

    private func snippet(_ body: String, _ q: String) -> String {
        let flat = body.replacingOccurrences(of: "\n", with: " ")
        guard let range = flat.range(of: q, options: .caseInsensitive) else { return flat }
        let start = flat.index(range.lowerBound, offsetBy: -40, limitedBy: flat.startIndex) ?? flat.startIndex
        return (start == flat.startIndex ? "" : "…") + flat[start...]
    }
}
