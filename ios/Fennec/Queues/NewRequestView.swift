import SwiftUI

/// A new request, written the way a new chat used to start (FEN-110): + on
/// home picks the agent, and this page is that chat's blank page and
/// composer. The request goes to the open queue (home's queue switcher picks
/// it) for the chosen agent. Send opens its thread at once; the board answers
/// in the background (FEN-58).
struct NewRequestView: View {
    @Environment(QueueStore.self) private var queues
    var projectID: String
    var agent: AgentKind
    @Binding var path: [QueueRoute]
    @State private var draft = ""
    @State private var images = ImageDraft()
    /// Why the last send didn't reach the board (FEN-58).
    @State private var error: String?
    @State private var composerFocused = false
    @State private var composerResetRevision = 0

    var body: some View {
        ScrollView {
            VStack(spacing: 12) {
                EmptyConversation(agent: agent)
                if let error {
                    Label { Text(error) } icon: { Image(systemName: "exclamationmark.triangle").regularIconWeight() }
                        .font(.system(size: 12.5)).foregroundStyle(Theme.danger)
                        .accessibilityIdentifier("new-request-error")
                }
            }
            .padding(.horizontal, 16)
        }
        .scrollDismissesKeyboard(.interactively)
        .background(Theme.bg)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .principal) { title } }
        .safeAreaInset(edge: .bottom, spacing: 0) { composer }
        .onAppear {
            if let unsent = queues.unsent, unsent.project == projectID {
                draft = unsent.text
                images = ImageDraft(done: unsent.images)
                error = unsent.error
                composerResetRevision += 1
                queues.unsent = nil
            }
            composerFocused = true
        }
    }

    /// The chat's title pill: the agent, and where the request goes.
    private var title: some View {
        HStack(spacing: 8) {
            AgentIcon(agent: agent, size: 24)
            VStack(alignment: .leading, spacing: 0) {
                Text(agent.displayName).font(.system(size: 14, weight: .medium)).lineLimit(1)
                Text(queues.project(projectID)?.name ?? "").font(.system(size: 11)).foregroundStyle(Theme.fg2).lineLimit(1)
            }
        }
        .frame(maxWidth: 220)
        .accessibilityElement(children: .combine)
    }

    /// The chat composer: images (as in a thread reply), the prompt, Send.
    private var composer: some View {
        VStack(spacing: 6) {
            ImageDraftStrip(draft: $images).padding(.horizontal, 16)
            HStack(alignment: .bottom, spacing: 8) {
                ImagePickerButton(draft: $images)
                    .accessibilityLabel(L10n.text("ios.attach"))
                    .accessibilityIdentifier("new-request-image")
                ZStack(alignment: .leading) {
                    if draft.isEmpty {
                        Text(placeholder)
                            .font(.system(size: 16)).foregroundStyle(Theme.fg3)
                            .allowsHitTesting(false).accessibilityHidden(true)
                    }
                    ComposerTextView(text: $draft, focused: $composerFocused,
                                     placeholder: placeholder, canSend: canSend,
                                     onSend: send, resetRevision: composerResetRevision,
                                     sendsOnReturn: false, identifier: "new-request-text")
                }
                Button(action: send) {
                    Image(systemName: "arrow.up").regularIconWeight().font(.system(size: 15, weight: .semibold))
                        .frame(width: 36, height: 36)
                        .foregroundStyle(Theme.onPrimary)
                        .background(Theme.primary.opacity(canSend ? 1 : 0.35), in: Circle())
                }
                .disabled(!canSend)
                .accessibilityLabel(L10n.text("ui.send"))
                .accessibilityIdentifier("send-request")
            }
            .padding(.leading, 8).padding(.trailing, 6).padding(.vertical, 6)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            .padding(.horizontal, 12)
            .padding(.bottom, 6)
        }
        .keepsKeyboard()
    }

    private var placeholder: String { L10n.text("ios.message_agent", values: ["agent": agent.displayName]) }

    private var canSend: Bool {
        !images.uploading && (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !images.done.isEmpty)
    }

    /// Replaces this page with the new thread; a refusal brings it back
    /// with the text and images (QueuesView watches `queues.unsent`).
    private func send() {
        guard canSend, queues.project(projectID) != nil else { return }
        let number = queues.send(projectID, text: draft, images: images.done, agent: agent)
        queues.selectedID = projectID
        composerFocused = false
        if let index = path.lastIndex(of: .newRequest(projectID, agent)) { path.remove(at: index) }
        path.append(.thread(projectID, number))
    }
}
