import SwiftUI

/// Home. With Show Queues off (the default) it is the session list as
/// before; with it on, a bottom tab bar: Sessions, Queues (badged with the
/// requests that need you), and Search on its own (DESIGN.html 18, 18b).
struct MainView: View {
    @AppStorage(QueueSettings.enabledKey) private var queuesEnabled = false
    @Environment(AppModel.self) private var app

    var body: some View {
        if queuesEnabled { MainTabs().environment(app.queues) }
        else { HomeView() }
    }
}

enum QueueSettings {
    static let enabledKey = "queuesEnabled"
    static let notifyKey = "notifyRequestNeedsYou"
}

struct MainTabs: View {
    enum Section: Hashable { case sessions, queues, search }
    @Environment(QueueStore.self) private var queues
    @State private var selection: Section = .sessions
    /// Search searches the tab you came from.
    @State private var searchScope: Section = .sessions

    var body: some View {
        TabView(selection: $selection) {
            Tab(L10n.text("ui.sessions"), systemImage: "bubble.left", value: Section.sessions) {
                HomeView(tabbed: true)
            }
            Tab(L10n.text("ios.queues"), systemImage: "square.stack", value: Section.queues) {
                QueuesView()
            }
            .badge(queues.needsYouCount)
            Tab(value: Section.search, role: .search) {
                SearchTab(scope: searchScope)
            }
        }
        .tint(Theme.accent)
        .onChange(of: selection) { _, tab in if tab != .search { searchScope = tab } }
        .task {
            // Keep the badge current while the Sessions tab is open.
            while !Task.isCancelled {
                if selection != .queues { await queues.refresh() }
                try? await Task.sleep(for: .seconds(20))
            }
        }
    }
}

/// The search tab: sessions or requests, by the tab it was opened from.
struct SearchTab: View {
    var scope: MainTabs.Section
    @Environment(SessionStore.self) private var store
    @State private var query = ""
    @State private var path: [QueueRoute] = []

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if scope == .queues {
                        RequestSearchResults(query: query) { path.append($0) }
                    } else {
                        let results = store.search(query)
                        if !results.sessions.isEmpty {
                            GroupHeader(title: L10n.text("ui.sessions"), count: results.sessions.count)
                            ForEach(results.sessions) { session in
                                Button { path.append(.chat(session.id)) } label: { SessionRow(session: session, subtitle: session.project) }
                                    .buttonStyle(.plain)
                            }
                        }
                    }
                }
            }
            .background(Theme.bg)
            .navigationTitle(L10n.text("ios.search"))
            .navigationBarTitleDisplayMode(.inline)
            .searchable(text: $query, prompt: scope == .queues ? L10n.text("ios.search_requests") : L10n.text("ios.search"))
            .modifier(QueueDestinations(path: $path))
        }
    }
}
