import SwiftUI

/// 05 search: Sessions match titles, Messages match agent replies.
struct SearchView: View {
    @Environment(SessionStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    var open: (String) -> Void
    @State private var query = ""
    @FocusState private var focused: Bool

    var body: some View {
        let results = store.search(query)
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                HStack(spacing: 10) {
                    Image(systemName: "magnifyingglass").foregroundStyle(Theme.fg2)
                    TextField(L10n.text("ios.search"), text: $query)
                        .focused($focused)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .submitLabel(.search)
                }
                .padding(.horizontal, 16)
                .frame(height: 46)
                .glassEffect(.regular, in: Capsule())
                GlassCircleButton(action: { dismiss() }) { Image(systemName: "xmark") }
                    .accessibilityLabel(L10n.text("ui.close"))
            }
            .padding(.horizontal, 16)
            .padding(.top, 14)

            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    if !results.sessions.isEmpty {
                        GroupHeader(title: L10n.text("ui.sessions"), count: results.sessions.count)
                        ForEach(results.sessions) { session in
                            Button { open(session.id) } label: { SessionRow(session: session, subtitle: session.project) }
                                .buttonStyle(.plain)
                        }
                    }
                    if !results.messages.isEmpty {
                        GroupHeader(title: L10n.text("ios.messages"), count: results.messages.count)
                        ForEach(Array(results.messages.enumerated()), id: \.offset) { _, hit in
                            Button { open(hit.0.id) } label: { MessageHit(session: hit.0, text: hit.1) }
                                .buttonStyle(.plain)
                        }
                    }
                    if !query.isEmpty && results.sessions.isEmpty && results.messages.isEmpty {
                        Text(L10n.text("ios.no_results", values: ["query": query]))
                            .font(.rowBody).foregroundStyle(Theme.fg2)
                            .frame(maxWidth: .infinity).padding(.top, 60)
                    }
                }
            }
        }
        .background(Theme.bg)
        .onAppear { focused = true }
    }
}

private struct MessageHit: View {
    var session: Session
    var text: String
    var body: some View {
        HStack(alignment: .top, spacing: 13) {
            AgentIcon(agent: session.agent, size: 20).padding(.top, 2).frame(width: 30)
            VStack(alignment: .leading, spacing: 4) {
                Text(text).font(.system(size: 14)).foregroundStyle(Theme.fg2).lineLimit(3)
                Text("\(session.title) · \(shortTime(session.updatedAt))").font(.system(size: 12)).foregroundStyle(Theme.fg3)
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 18)
        .padding(.vertical, 8)
        .contentShape(Rectangle())
    }
}
