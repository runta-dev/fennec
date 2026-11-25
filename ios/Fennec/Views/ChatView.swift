import SwiftUI
import PhotosUI
import UniformTypeIdentifiers

/// 06 chat, 07 approval, 08 question, 09 done. Messages scroll under the
/// glass title pill and composer; requests from the agent rise as sheets.
struct ChatView: View {
    @Environment(SessionStore.self) private var store
    @Environment(AppModel.self) private var app
    @Environment(RuntaAuth.self) private var auth
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.dismiss) private var dismiss
    var sessionID: String
    @State private var draft = ""
    @State private var showMenu = false
    @State private var denyReason = ""
    @State private var followBottom = true
    /// Scroll facts read while pinning a turn's header; not observed, so
    /// scrolling doesn't redraw the conversation.
    @State private var scrollFacts = ScrollFacts()
    /// Space after the newest message, above the composer.
    static let tailSpace: CGFloat = 40
    @State private var showTerminal = false
    @State private var showDeliveryIssue = false
    @State private var composerFocused = false
    @State private var composerResetRevision = 0
    @State private var showFiles = false
    @State private var showPhotos = false
    @State private var selectedPhotos: [PhotosPickerItem] = []
    @State private var importingAttachments = false
    /// The question the person swiped away; the pill brings it back.
    @State private var dismissedPrompt: LivePrompt?
    @State private var promptFailed = false

    private var draftKey: String {
        "chatDraft.\(app.environment.rawValue).\(auth.profile?.email ?? "preview").\(sessionID)"
    }

    private var draftBinding: Binding<String> {
        Binding(get: { draft }, set: {
            draft = $0
            UserDefaults.standard.set($0, forKey: draftKey)
        })
    }

    var body: some View {
        if let session = store.session(sessionID) {
            content(session)
                .environment(\.workspaceSessionID, sessionID)
                .fileImporter(isPresented: $showFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
                    switch result {
                    case .success(let urls): importFiles(urls)
                    case .failure(let error): store.attachmentErrors[sessionID] = error.localizedDescription
                    }
                }
                .photosPicker(isPresented: $showPhotos, selection: $selectedPhotos, maxSelectionCount: 8, matching: .images)
                .onChange(of: selectedPhotos) { _, photos in
                    guard !photos.isEmpty else { return }
                    importingAttachments = true
                    Task {
                        defer { selectedPhotos = []; importingAttachments = false }
                        var attachments: [DraftAttachment] = []
                        do {
                            for photo in photos {
                                guard let data = try await photo.loadTransferable(type: Data.self) else { throw AttachmentError.unavailable }
                                attachments.append(try await Task.detached { try AttachmentImport.image(data) }.value)
                            }
                            try store.addAttachments(attachments, to: sessionID)
                        } catch {
                            AttachmentImport.discard(attachments)
                            store.attachmentErrors[sessionID] = error.localizedDescription
                        }
                    }
                }
                .alert(L10n.text("ios.attachments"), isPresented: Binding(get: { store.attachmentErrors[sessionID] != nil }, set: { if !$0 { store.attachmentErrors[sessionID] = nil } })) {
                    Button(L10n.text("ui.close"), role: .cancel) { store.attachmentErrors[sessionID] = nil }
                } message: { Text(store.attachmentErrors[sessionID] ?? "") }
                .onAppear {
                    composerFocused = false
                    draft = UserDefaults.standard.string(forKey: draftKey) ?? ""
                }
        } else {
            Text(L10n.text("ios.session_ended")).foregroundStyle(Theme.fg2)
                .frame(maxWidth: .infinity, maxHeight: .infinity).background(Theme.bg)
        }
    }

    @ViewBuilder
    private func content(_ session: Session) -> some View {
        if session.address != nil { liveContent(session) } else { demoContent(session) }
    }

    /// Live sessions show the conversation from the agent's own session log
    /// (internal/transcript); Herdr's state drives the working indicator.
    private func liveContent(_ session: Session) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                // Lazy: opening a long conversation builds only the turns on
                // screen, not every turn and its Markdown, before sliding in.
                LazyVStack(alignment: .leading, spacing: 14) {
                    if !session.transcriptLoaded {
                        ChatSkeleton()
                    } else if session.messages.isEmpty, let error = session.transcriptError {
                        VStack(spacing: 12) {
                            ConnectionIssueCard(issue: transcriptIssue(error),
                                                onSignIn: { auth.signOut() })
                            Button(L10n.text("ios.open_terminal")) { showTerminal = true }.buttonStyle(GlassCapsuleButtonStyle()).frame(width: 200)
                        }
                        .frame(maxWidth: .infinity).padding(.top, 80)
                    } else if session.messages.isEmpty && store.pendingSends[session.id] == nil {
                        emptyConversation(session)
                    }
                    let turns = liveTurns(session)
                    ForEach(turns) { turn in
                        TurnView(turn: turn, since: turnSince(turn, session), activity: session.activity)
                            .id(turn.id)
                    }
                    if let pending = store.pendingSends[session.id] {
                        VStack(alignment: .trailing, spacing: 6) {
                            MessageView(message: pending.message)
                            switch pending.delivery {
                            case .sent: EmptyView()
                            default: deliveryIndicator(pending)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .trailing)
                        .id(pending.message.id)
                        .transition(.asymmetric(insertion: .move(edge: .bottom).combined(with: .opacity), removal: .identity))
                        if pending.delivery == .sent {
                            // Delivered: the agent is starting; its turn header appears now.
                            StartingHeader().transition(.opacity)
                        }
                    }
                    if session.state == .working && turns.isEmpty {
                        WorkingIndicator(since: session.turnStarted ?? session.statusSince ?? session.updatedAt,
                                         activity: session.activity)
                            .id("working")
                    }
                    // Room between the newest message and the composer, so
                    // the end of a reply isn't read at the screen's edge. It
                    // is part of the content: scrolled up, it is gone.
                    Color.clear.frame(height: ChatView.tailSpace).id("bottom")
                }
                .padding(.horizontal, 16)
                // Never wider than the screen: wide content scrolls in its own block.
                .containerRelativeFrame(.horizontal, alignment: .leading)
                .padding(.vertical, 12)
            }
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            .defaultScrollAnchor(.top, for: .alignment)
            // Following the newest message keeps the bottom in place as content
            // grows; reading (or opening a turn) keeps what is on screen in
            // place, so a turn opening below its header doesn't move the header.
            .defaultScrollAnchor(followBottom ? .bottom : .top, for: .sizeChanges)
            .scrollDismissesKeyboard(.interactively)
            .environment(\.readingTurn, { followBottom = false })
            .environment(\.keepTurnHeader, { id, y, height in
                // Your finger wins: never pull the page while you scroll.
                guard scrollFacts.phase == .idle else { return false }
                // Put the header's top at `y` on screen: an anchor's point in
                // the header lines up with the same point of the visible area
                // (inside the title pill and composer insets).
                let insets = scrollFacts.insets
                let room = scrollFacts.containerHeight - insets.top - insets.bottom - height
                guard room > 1 else { return false }
                proxy.scrollTo(id, anchor: UnitPoint(x: 0, y: (y - insets.top) / room))
                return true
            })
            // Follow the newest message as the content grows (new lines,
            // tables and images settling their heights), unless you have
            // scrolled up to read; scrolling back down resumes following.
            .onScrollGeometryChange(for: EdgeInsets.self, of: { $0.contentInsets }) { _, insets in
                scrollFacts.insets = insets
            }
            .onScrollGeometryChange(for: ScrollGeometry.self, of: { $0 }) { old, new in
                guard new.contentSize.height != old.contentSize.height else { return }
                // Only from where it was before the content grew, and never
                // under your finger: a preview loading in the same moment you
                // start scrolling up must not pull you back to the bottom
                // (the phase change that stops following can come later).
                let wasAtBottom = old.visibleRect.maxY >= old.contentSize.height - 80
                if followBottom, wasAtBottom, scrollFacts.phase == .idle { proxy.scrollTo("bottom", anchor: .bottom) }
            }
            .onScrollGeometryChange(for: CGFloat.self, of: { $0.containerSize.height }) { old, height in
                scrollFacts.containerHeight = height
                // Dragging the keyboard away resizes the list; keep your scroll.
                guard scrollFacts.phase == .idle else { return }
                // The keyboard rising over the composer shows the newest
                // message; the keyboard going away leaves where you read.
                if followBottom || (composerFocused && height < old) { proxy.scrollTo("bottom", anchor: .bottom) }
            }
            .onChange(of: store.pendingSends[session.id]?.message.id) {
                followBottom = true
                withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) { proxy.scrollTo("bottom", anchor: .bottom) }
            }
            .onScrollPhaseChange { _, phase, context in
                scrollFacts.phase = phase
                // Your finger touching the list stops following at once, before
                // it moves, so content settling under it (a preview loading)
                // can't pull the page back to the newest message. Lifting it
                // near the bottom follows again.
                if phase == .tracking || phase == .interacting { followBottom = false }
                guard phase == .idle else { return }
                let geometry = context.geometry
                followBottom = geometry.visibleRect.maxY >= geometry.contentSize.height - 80
            }
            .task(id: session.id) {
                followBottom = true
                try? await Task.sleep(for: .milliseconds(250))
                proxy.scrollTo("bottom", anchor: .bottom)
            }
            .onChange(of: session.messages.count) {
                if followBottom, scrollFacts.phase == .idle { withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } }
            }
            .onChange(of: session.transcriptLoaded) {
                proxy.scrollTo("bottom", anchor: .bottom)
            }
        }
        .background(Theme.bg)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) { TitlePill(session: session) }
            ToolbarItem(placement: .topBarTrailing) {
                Button { showMenu = true } label: { Image(systemName: "ellipsis") }
                    .accessibilityLabel(L10n.text("ios.session_menu"))
            }
        }
        .safeAreaInset(edge: .top, spacing: 0) {
            ConnectionIssueBanner(issue: store.issue,
                                  onSignIn: { auth.signOut() })
                .padding(.horizontal, 16).padding(.top, 4)
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            VStack(spacing: 8) {
                if store.livePrompts[session.id] != nil && store.livePrompts[session.id] == dismissedPrompt {
                    PromptWaitingPill { dismissedPrompt = nil }
                }
                composer(session)
            }
        }
        .task(id: session.id) {
            while !Task.isCancelled {
                // The home list's refresh stops while this page is in front;
                // this keeps the chat's log and Herdr's state current.
                let wait = await store.pollChat(session.id)
                try? await Task.sleep(for: wait)
            }
        }
        .sheet(isPresented: Binding(
            get: { !showTerminal && !showMenu && store.livePrompts[session.id] != nil && store.livePrompts[session.id] != dismissedPrompt },
            set: { shown in if !shown { dismissedPrompt = store.livePrompts[session.id] } })) {
            if let prompt = store.livePrompts[session.id] {
                LivePromptSheet(agent: session.agent, prompt: prompt, answering: store.answeringPrompts.contains(session.id),
                                onAnswer: { key in Task { promptFailed = !(await store.answerPrompt(session.id, key: key)) } },
                                onOpenTerminal: { dismissedPrompt = prompt; showTerminal = true })
                    .alert(L10n.text("ios.answer_failed"), isPresented: $promptFailed) { Button(L10n.text("ui.ok")) {} }
            }
        }
        .navigationDestination(isPresented: $showTerminal) {
            if let address = session.address { TerminalPage(session: session, address: address) }
        }
        .sheet(isPresented: $showMenu) {
            SessionMenu(session: session, onStop: {
                store.stop(session.id)
                dismiss()
            }, onOpenTerminal: session.address == nil || store.core == nil ? nil : {
                showMenu = false
                showTerminal = true
            })
            .presentationDetents([.height(360)])
        }
    }

    /// Delivered and in the conversation, but not yet seen working: its turn
    /// shows "Working" (no time) at once. Before it is in the conversation,
    /// the header under the pending message does.
    private func starting(_ session: Session) -> Bool {
        session.state != .working && store.pendingSends[session.id] == nil && store.workingOrStarting(session)
    }

    private func liveTurns(_ session: Session) -> [ChatTurn] {
        let working = session.state == .working || starting(session)
        return ChatTurn.turns(session.messages, working: working, byPrompt: session.agent == .claudeCode)
    }

    /// When a turn's time counts from; until the agent is seen working there
    /// is no time yet (a start in the future reads as plain "Working").
    private func turnSince(_ turn: ChatTurn, _ session: Session) -> Date {
        if starting(session) { return .distantFuture }
        let started: Date? = turn.prompt?.time ?? session.turnStarted ?? session.statusSince
        return started ?? session.updatedAt
    }

    private func transcriptIssue(_ message: String) -> ConnectionIssue {
        var issue = ConnectionIssue(message: message)
        issue.otherTitle = L10n.text("ios.conversation_unavailable")
        return issue
    }

    private func demoContent(_ session: Session) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 14) {
                    if session.messages.isEmpty { emptyConversation(session) }
                    ForEach(session.messages) { message in
                        MessageView(message: message).id(message.id)
                    }
                    if session.state == .working {
                        WorkingIndicator(since: session.turnStarted ?? session.statusSince ?? session.updatedAt,
                                         activity: session.activity)
                            .id("working")
                    }
                    Color.clear.frame(height: ChatView.tailSpace).id("bottom")
                }
                .padding(.horizontal, 16)
                // Never wider than the screen: wide content scrolls in its own block.
                .containerRelativeFrame(.horizontal, alignment: .leading)
                .padding(.vertical, 12)
            }
            .defaultScrollAnchor(.bottom)
            .defaultScrollAnchor(.top, for: .alignment)
            .scrollDismissesKeyboard(.interactively)
            .onScrollGeometryChange(for: CGFloat.self, of: { $0.containerSize.height }) { _, _ in
                if composerFocused { proxy.scrollTo("bottom", anchor: .bottom) }
            }
            .onChange(of: session.messages.count) {
                withAnimation { proxy.scrollTo("bottom", anchor: .bottom) }
            }
        }
        .background(Theme.bg)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) { TitlePill(session: session) }
            ToolbarItem(placement: .topBarTrailing) {
                Button { showMenu = true } label: { Image(systemName: "ellipsis") }
                    .accessibilityLabel(L10n.text("ios.session_menu"))
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if session.approval == nil && session.question == nil { composer(session) }
        }
        .sheet(isPresented: Binding(get: { session.approval != nil }, set: { _ in })) {
            if let approval = session.approval { approvalSheet(session, approval) }
        }
        .sheet(isPresented: Binding(get: { session.question != nil }, set: { _ in })) {
            if let question = session.question { questionSheet(session, question) }
        }
        .sheet(isPresented: $showMenu) {
            SessionMenu(session: session, onStop: {
                store.stop(session.id)
                dismiss()
            })
            .presentationDetents([.height(360)])
        }
    }

    private func composer(_ session: Session) -> some View {
        VStack(spacing: 6) {
        if let attachments = store.attachmentDrafts[sessionID], !attachments.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(attachments) { attachment in
                        HStack(spacing: 6) {
                            Image(systemName: attachment.isImage ? "photo" : "doc")
                            Text(attachment.name).lineLimit(1).frame(maxWidth: 160)
                            Button { store.removeAttachment(attachment, from: sessionID) } label: { Image(systemName: "xmark.circle.fill") }
                                .accessibilityLabel(L10n.text("ios.remove_attachment"))
                                .disabled(store.pendingSends[sessionID] != nil)
                        }
                        .font(.system(size: 13)).padding(8)
                        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 10))
                    }
                }
            }
            .padding(.horizontal, 16)
        }
        HStack(spacing: 8) {
            Menu {
                Button(L10n.text("ios.choose_photos"), systemImage: "photo") { showPhotos = true }
                Button(L10n.text("ios.choose_files"), systemImage: "folder") { showFiles = true }
            } label: {
                if importingAttachments { ProgressView().frame(width: 36, height: 36) }
                else { Image(systemName: "plus")
                .font(.system(size: 17, weight: .medium))
                .frame(width: 36, height: 36)
                }
            }
                .disabled(importingAttachments || store.pendingSends[sessionID] != nil || session.address == nil)
                .accessibilityLabel(L10n.text("ios.attach"))
                .accessibilityIdentifier("composer-attach")
            ZStack(alignment: .leading) {
                if draft.isEmpty {
                    Text(L10n.text("ios.message_agent", values: ["agent": session.agent.displayName]))
                        .font(.system(size: 16)).foregroundStyle(Theme.fg3)
                        .allowsHitTesting(false).accessibilityHidden(true)
                }
                ComposerTextView(text: draftBinding,
                                 focused: Binding(get: { composerFocused }, set: { composerFocused = $0 }),
                                 placeholder: L10n.text("ios.message_agent", values: ["agent": session.agent.displayName]),
                                 canSend: canSend,
                                 onSend: { sendMessage(session) },
                                 onPasteImage: { data in importImage(data) }, resetRevision: composerResetRevision)
            }
            if draft.isEmpty && (store.attachmentDrafts[sessionID] ?? []).isEmpty && session.state == .working {
                Button { store.stopDemoTurn(session.id) } label: {
                    Image(systemName: "stop.fill").font(.system(size: 13))
                        .frame(width: 36, height: 36)
                        .foregroundStyle(Theme.onPrimary)
                        .background(Theme.primary, in: Circle())
                }
                .accessibilityLabel(L10n.text("ios.stop_turn"))
            } else {
                Button { sendMessage(session) } label: {
                    Image(systemName: "arrow.up").font(.system(size: 15, weight: .semibold))
                        .frame(width: 36, height: 36)
                        .foregroundStyle(Theme.onPrimary)
                        .background(Theme.primary.opacity(canSend ? 1 : 0.35), in: Circle())
                }
                .disabled(!canSend)
                .accessibilityLabel(L10n.text("ui.send"))
                .accessibilityIdentifier("composer-send")
            }
        }
        .padding(.leading, 8)
        .padding(.trailing, 6)
        .padding(.vertical, 6)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .padding(.horizontal, 12)
        .padding(.bottom, 6)
        }
    }

    private var canSend: Bool {
        (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !(store.attachmentDrafts[sessionID] ?? []).isEmpty)
            && store.pendingSends[sessionID] == nil && !importingAttachments
    }

    private func importFiles(_ urls: [URL]) {
        importingAttachments = true
        Task {
            defer { importingAttachments = false }
            do {
                let attachments = try await Task.detached { try AttachmentImport.files(urls) }.value
                try store.addAttachments(attachments, to: sessionID)
            } catch { store.attachmentErrors[sessionID] = error.localizedDescription }
        }
    }

    private func importImage(_ data: Data) {
        guard store.pendingSends[sessionID] == nil, !importingAttachments else { return }
        importingAttachments = true
        Task {
            defer { importingAttachments = false }
            do {
                let attachment = try await Task.detached { try AttachmentImport.image(data) }.value
                try store.addAttachments([attachment], to: sessionID)
            } catch { store.attachmentErrors[sessionID] = error.localizedDescription }
        }
    }

    private func sendMessage(_ session: Session) {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard canSend else { return }
        followBottom = true
        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.25)) {
            if !(store.attachmentDrafts[sessionID] ?? []).isEmpty {
                store.sendAttachments(text, to: session.id) {
                    if draft.trimmingCharacters(in: .whitespacesAndNewlines) == text { clearDraft() }
                }
            } else { clearDraft(); store.send(text, to: session.id) }
        }
    }

    private func clearDraft() {
        draftBinding.wrappedValue = ""
        composerResetRevision += 1
    }

    private func emptyConversation(_ session: Session) -> some View {
        VStack(spacing: 16) {
            AgentIcon(agent: session.agent, size: 48)
                .accessibilityHidden(true)
            VStack(spacing: 8) {
                Text(L10n.text("ios.empty_chat_title"))
                    .font(.system(size: 20, weight: .medium)).foregroundStyle(Theme.fg)
                Text(L10n.text("ios.empty_chat_detail"))
                    .font(.system(size: 14)).foregroundStyle(Theme.fg2)
                    .lineSpacing(4)
            }
            .multilineTextAlignment(.center)
            .frame(maxWidth: 280)
        }
        .frame(maxWidth: .infinity).padding(.top, 72).padding(.bottom, 40)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("empty-conversation")
    }

    @ViewBuilder
    private func deliveryIndicator(_ pending: SessionStore.PendingSend) -> some View {
        Group {
            switch pending.delivery {
            case .sending: SendingDash()
            case .sent:
                EmptyView()
            case .unconfirmed:
                Button { showDeliveryIssue = true } label: {
                    Image(systemName: "exclamationmark.circle").font(.system(size: 14))
                        .foregroundStyle(Theme.danger)
                        .frame(width: 28, height: 28)
                }
                .accessibilityLabel(L10n.text("ios.delivery_unconfirmed"))
                .alert(L10n.text("ios.delivery_unconfirmed"), isPresented: $showDeliveryIssue) {
                    Button(L10n.text("ios.return_draft")) {
                        guard store.pendingSends[sessionID]?.message.id == pending.message.id else { return }
                        if case .user(let text) = pending.message.kind {
                            draftBinding.wrappedValue = draft.isEmpty ? text : draft + "\n" + text
                        }
                        store.pendingSends[sessionID] = nil
                    }
                    Button(L10n.text("ui.close"), role: .cancel) {}
                } message: { Text(L10n.text("ios.delivery_explanation")) }
            }
        }
        .foregroundStyle(Theme.fg3)
        .frame(width: 28, height: 20)
    }

    private func approvalSheet(_ session: Session, _ approval: ApprovalRequest) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(L10n.text("ios.allow_command", values: ["agent": session.agent.displayName]))
                .font(.system(size: 17, weight: .medium))
            HStack(alignment: .top, spacing: 8) {
                Text("$").foregroundStyle(Theme.fg3)
                Text(approval.command).textSelection(.enabled)
            }
            .font(.system(size: 13, design: .monospaced))
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.bg, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            HStack(spacing: 6) {
                Image(systemName: approval.location == .mac ? "laptopcomputer" : "cloud")
                    .foregroundStyle(approval.location == .mac ? Theme.accent : Theme.fg3)
                Text(approval.directory).lineLimit(1).truncationMode(.middle)
            }
            .font(.system(size: 12, design: .monospaced))
            .foregroundStyle(Theme.fg3)
            Button(L10n.text("ios.approve")) { store.approve(session.id, forSession: false) }.buttonStyle(PrimaryButtonStyle())
            Button(L10n.text("ios.approve_session")) { store.approve(session.id, forSession: true) }.buttonStyle(GlassCapsuleButtonStyle())
            Divider().overlay(Theme.line)
            HStack {
                Image(systemName: "pencil").foregroundStyle(Theme.fg3)
                TextField(L10n.text("ios.tell_agent", values: ["agent": session.agent.displayName]), text: $denyReason)
                Button(L10n.text("permission.deny")) {
                    store.deny(session.id, reason: denyReason)
                    denyReason = ""
                }
                .font(.system(size: 14, weight: .medium))
                .padding(.horizontal, 14).frame(height: 34)
                .glassEffect(.regular.interactive(), in: Capsule())
            }
            .font(.system(size: 14.5))
        }
        .padding(20)
        .presentationDetents([.height(380)])
        .presentationBackgroundInteraction(.enabled(upThrough: .height(380)))
        .interactiveDismissDisabled()
    }

    private func questionSheet(_ session: Session, _ question: AgentQuestion) -> some View {
        QuestionSheet(question: question) { store.answer(session.id, with: $0) }
            .presentationDetents([.height(420)])
            .interactiveDismissDisabled()
    }
}

private struct QuestionSheet: View {
    var question: AgentQuestion
    var answer: (String) -> Void
    @State private var custom = ""
    @State private var typing = false

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            Text(question.prompt).font(.system(size: 17, weight: .medium)).padding(.bottom, 4)
            ForEach(question.options) { option in
                Button { answer(option.title) } label: {
                    HStack(spacing: 10) {
                        Text("\(option.id)").font(.system(size: 12, design: .monospaced)).foregroundStyle(Theme.fg3).frame(width: 12)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(option.title).font(.system(size: 15))
                            Text(option.detail).font(.system(size: 12.5)).foregroundStyle(Theme.fg2)
                        }
                        Spacer()
                    }
                    .padding(12)
                    .background(Theme.surface2, in: RoundedRectangle(cornerRadius: 13, style: .continuous))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
            HStack(spacing: 10) {
                Image(systemName: "pencil").foregroundStyle(Theme.fg3)
                TextField(L10n.text("ios.custom_answer"), text: $custom, onEditingChanged: { typing = $0 })
                    .onSubmit { if !custom.isEmpty { answer(custom) } }
            }
            .padding(12)
            .background(Theme.surface2, in: RoundedRectangle(cornerRadius: 13, style: .continuous))
            .selectionOutline(typing, radius: 13)
        }
        .padding(20)
    }
}

struct TitlePill: View {
    var session: Session
    var body: some View {
        HStack(spacing: 8) {
            AgentIcon(agent: session.agent, size: 24)
            VStack(alignment: .leading, spacing: 0) {
                Text(session.title).font(.system(size: 14, weight: .medium)).lineLimit(1)
                HStack(spacing: 4) {
                    Circle().fill(StatusDot(state: session.state).color).frame(width: 6, height: 6)
                    Text(session.state.label)
                }
                .font(.system(size: 11)).foregroundStyle(Theme.fg2)
            }
        }
        .frame(maxWidth: 220)
    }
}

struct MessageView: View {
    var message: ChatMessage
    var lineLimit: Int? = nil

    var body: some View {
        switch message.kind {
        case .macOnline:
            HStack(spacing: 10) {
                Image(systemName: "laptopcomputer").font(.system(size: 15))
                    .foregroundStyle(Theme.working)
                Text(L10n.text("ios.mac_online")).font(.system(size: 13, weight: .medium))
                Spacer(minLength: 8)
                if let time = message.time {
                    Text(time, format: .dateTime.month(.abbreviated).day().hour().minute())
                        .font(.system(size: 12)).foregroundStyle(Theme.fg3)
                }
            }
            .foregroundStyle(Theme.fg2)
            .padding(.horizontal, 12).padding(.vertical, 10)
            .background(Theme.surface, in: RoundedRectangle(cornerRadius: 12))
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("mac-online-notice")
        case .user(let text):
            VStack(alignment: .trailing, spacing: 6) {
                if !message.images.isEmpty { PastedImages(images: message.images) }
                ForEach(Array(PreviewPart.parse(text).enumerated()), id: \.offset) { _, part in
                if let ref = part.ref { WorkspaceFileCard(ref: ref) }
                else if let text = part.text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    HStack {
                        Spacer(minLength: 48)
                        UserText(text: text)
                            .font(.system(size: 15.5))
                            .padding(.horizontal, 14).padding(.vertical, 10)
                            .background(Theme.bubble, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                            .textSelection(.enabled)
                    }
                }
                }
                LinkCardList(text: text).padding(.leading, 48)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
        case .agent(let text):
            VStack(alignment: .leading, spacing: 10) {
                PreviewMessageText(text: text, lineLimit: lineLimit)
                // Folded notes stay one line; cards only on the full reply.
                if lineLimit == nil { LinkCardList(text: text) }
            }
        case .step(let text):
            HStack(spacing: 6) {
                Text(text)
                Image(systemName: "chevron.right").font(.system(size: 10, weight: .semibold))
            }
            .font(.system(size: 13.5))
            .foregroundStyle(Theme.fg2)
        case .changes(let files):
            ChangesCard(files: files)
        case .tools(let uses):
            ToolStep(uses: uses)
        case .end:
            EmptyView()
        case .compacted:
            HStack(spacing: 10) {
                Rectangle().fill(Theme.line).frame(height: 1)
                Text(L10n.text("ios.compacted")).font(.system(size: 12)).foregroundStyle(Theme.fg2).fixedSize()
                Rectangle().fill(Theme.line).frame(height: 1)
            }
            .padding(.vertical, 4)
            .accessibilityElement(children: .combine)
        }
    }
}

private struct ChangesCard: View {
    var files: [FileChange]
    @State private var expanded = true

    var body: some View {
        VStack(spacing: 0) {
            Button { withAnimation { expanded.toggle() } } label: {
                HStack {
                    Text(L10n.text("ios.files_changed", values: ["count": String(files.count)])).font(.system(size: 14, weight: .medium))
                    Text("+\(files.map(\.added).reduce(0, +))").foregroundStyle(Theme.working)
                    Text("−\(files.map(\.removed).reduce(0, +))").foregroundStyle(Theme.danger)
                    Spacer()
                    Image(systemName: expanded ? "chevron.up" : "chevron.down").foregroundStyle(Theme.fg3)
                }
                .font(.system(size: 12.5, design: .monospaced))
                .padding(.horizontal, 14).padding(.vertical, 11)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if expanded {
                ForEach(files, id: \.path) { file in
                    Divider().overlay(Theme.line)
                    HStack {
                        Text(file.path).lineLimit(1).truncationMode(.head).foregroundStyle(Theme.fg2)
                        Spacer()
                        Text("+\(file.added)").foregroundStyle(Theme.working)
                        if file.removed > 0 { Text("−\(file.removed)").foregroundStyle(Theme.danger) }
                    }
                    .font(.system(size: 12, design: .monospaced))
                    .padding(.horizontal, 14).padding(.vertical, 9)
                }
            }
        }
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).strokeBorder(Theme.line))
    }
}

/// 11 session menu: identity, actions, then the destructive action alone.
struct SessionMenu: View {
    @Environment(SessionStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    var session: Session
    var onStop: () -> Void
    var onOpenTerminal: (() -> Void)? = nil
    @State private var confirmStop = false
    @State private var renaming = false
    @State private var newTitle = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 12) {
                AgentIcon(agent: session.agent, size: 36)
                VStack(alignment: .leading, spacing: 1) {
                    Text(session.title).font(.system(size: 16, weight: .medium)).lineLimit(1)
                    Text("\(session.agent.displayName) · \(session.model) · \(session.location == .cloud ? L10n.text("ios.cloud") : "Mac")")
                        .font(.system(size: 13)).foregroundStyle(Theme.fg2)
                }
            }
            .padding(.horizontal, 4)
            ListCard {
                Button { onOpenTerminal?() } label: { ListRow(icon: "apple.terminal", iconColor: Theme.fg, title: L10n.text("ios.open_terminal")) }
                    .disabled(onOpenTerminal == nil)
                Button { newTitle = session.title; renaming = true } label: { ListRow(icon: "pencil", iconColor: Theme.fg, title: L10n.text("ios.rename")) }
                Button { store.togglePin(session.id); dismiss() } label: {
                    ListRow(icon: session.pinned ? "pin.slash" : "pin", iconColor: Theme.fg, title: session.pinned ? L10n.text("ios.unpin") : L10n.text("ios.pin_top"), showDivider: false)
                }
            }
            .buttonStyle(.plain)
            ListCard {
                Button { confirmStop = true } label: {
                    ListRow(icon: "stop.fill", iconColor: Theme.danger, title: L10n.text("ui.stop_session"), titleColor: Theme.danger, showDivider: false)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 20)
        .confirmationDialog(L10n.text("ios.stop_confirm"), isPresented: $confirmStop, titleVisibility: .visible) {
            Button(L10n.text("ui.stop_session"), role: .destructive) { dismiss(); onStop() }
        } message: {
            Text(L10n.text("ios.stop_explanation"))
        }
        .alert(L10n.text("ios.rename_session"), isPresented: $renaming) {
            TextField(L10n.text("ios.title"), text: $newTitle)
            Button(L10n.text("ios.save")) { store.rename(session.id, to: newTitle) }
            Button(L10n.text("ui.cloud_cancel"), role: .cancel) {}
        }
    }
}

/// A run of steps, Codex style: a headline ("Explored 3 files, ran 2
/// commands"); tap to list each step as a verb and what it acted on.
private struct ToolStep: View {
    var uses: [ToolUse]
    @State private var expanded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Button { withAnimation(.snappy) { expanded.toggle() } } label: {
                HStack(spacing: 6) {
                    Text(ToolUse.headline(uses)).lineLimit(1).truncationMode(.middle)
                    Image(systemName: "chevron.right").font(.system(size: 10, weight: .semibold))
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                }
                .font(.system(size: 14)).foregroundStyle(Theme.fg2)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if expanded {
                // The steps as a list card, like the app's other lists: an
                // icon for what was done, then what it was done to.
                VStack(spacing: 0) {
                    ForEach(Array(uses.enumerated()), id: \.offset) { index, use in
                        if index > 0 { Divider().overlay(Theme.line).padding(.leading, 40) }
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Image(systemName: use.symbol)
                                .font(.system(size: 12.5)).foregroundStyle(Theme.fg2)
                                .frame(width: 18)
                            Text(use.summary.isEmpty ? use.verb : use.summary)
                                .font(.system(size: 12.5, design: .monospaced)).foregroundStyle(Theme.fg)
                                .lineLimit(2).truncationMode(.tail)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .padding(.horizontal, 12).padding(.vertical, 9)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel("\(use.verb) \(use.summary)")
                    }
                }
                .background(Theme.surface, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Theme.line))
            }
        }
    }
}

/// While the agent works and nothing is in the log yet: the elapsed time
/// (shimmering) and Herdr's line about what it is doing.
/// A turn's header while the agent starts on a just-delivered message:
/// "Working" and the divider, before there is a time to show.
private struct StartingHeader: View {
    var body: some View {
        HStack {
            Text(L10n.text("ui.working")).textShimmer()
            Spacer()
        }
        .font(.system(size: 14)).foregroundStyle(Theme.fg2)
        .padding(.bottom, 10)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 1) }
        .accessibilityIdentifier("starting-header")
    }
}

private struct ReadingTurnKey: EnvironmentKey { static let defaultValue: () -> Void = {} }
private struct KeepTurnHeaderKey: EnvironmentKey {
    static let defaultValue: (AnyHashable, CGFloat, CGFloat) -> Bool = { _, _, _ in false }
}
extension EnvironmentValues {
    /// Called when a turn is opened or closed, so the list stops following.
    var readingTurn: () -> Void {
        get { self[ReadingTurnKey.self] }
        set { self[ReadingTurnKey.self] = newValue }
    }
    /// Scrolls so the header with this id has its top at the given y on
    /// screen (header height last). False when it can't: you are scrolling.
    var keepTurnHeader: (AnyHashable, CGFloat, CGFloat) -> Bool {
        get { self[KeepTurnHeaderKey.self] }
        set { self[KeepTurnHeaderKey.self] = newValue }
    }
}

/// What the conversation's scroll view last reported. A plain class: written
/// on every scroll, read only when a turn opens, never redraws anything.
private final class ScrollFacts {
    var phase: ScrollPhase = .idle
    var containerHeight: CGFloat = 0
    var insets = EdgeInsets()
}

/// Where a turn's header was on screen when it was tapped, kept for a moment
/// while the opened (or closed) turn settles. Not observed, like ScrollFacts.
private final class HeaderPin {
    var frame: CGRect = .zero
    var target: CGFloat?
    /// The y last asked of the scroll view. Wherever the header lands
    /// instead, the next ask is off by that much the other way, so a header
    /// pushed by settling content, or a scroll that lands a little off,
    /// comes back to its place in a step or two.
    var asked: CGFloat = 0
    var corrections = 0
    var until: Date = .distantPast

    func start() {
        target = frame.minY
        asked = frame.minY
        corrections = 0
        until = .now.addingTimeInterval(0.8)
    }
}

struct WorkingIndicator: View {
    var since: Date
    var activity: String?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    Text(L10n.text("ios.working_for", values: ["duration": Self.elapsed(from: since, to: context.date)]))
                        .font(.system(size: 14, weight: .medium))
                        .monospacedDigit()
                }
                .textShimmer()
                if let activity, !activity.isEmpty {
                    Text(activity).font(.system(size: 13)).foregroundStyle(Theme.fg3).lineLimit(2)
                }
            }
        }
        .padding(.top, 4)
        .accessibilityElement(children: .combine)
    }

    static func elapsed(from start: Date, to now: Date) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(start)))
        if seconds < 60 { return "\(seconds)s" }
        if seconds < 3600 { return "\(seconds / 60)m \(seconds % 60)s" }
        return "\(seconds / 3600)h \(seconds % 3600 / 60)m"
    }
}

struct TypingDots: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        TimelineView(.animation(minimumInterval: 1.0 / 30, paused: reduceMotion)) { context in
            let t = context.date.timeIntervalSinceReferenceDate
            HStack(spacing: 4) {
                ForEach(0..<3) { index in
                    let phase = reduceMotion ? 0.6 : (sin((t * 2 * .pi / 1.2) - Double(index) * 0.8) + 1) / 2
                    Circle().fill(Theme.fg3).frame(width: 6, height: 6)
                        .opacity(0.35 + 0.65 * phase)
                        .offset(y: reduceMotion ? 0 : -2 * phase)
                }
            }
        }
        .frame(height: 10)
        .accessibilityHidden(true)
    }
}

/// Placeholder bubbles while the conversation log loads.
private struct ChatSkeleton: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack { Spacer(); SkeletonBar(width: 200, height: 40) }
            VStack(alignment: .leading, spacing: 8) {
                SkeletonBar(width: 290, height: 12); SkeletonBar(width: 250, height: 12); SkeletonBar(width: 180, height: 12)
            }
            SkeletonBar(width: 120, height: 11)
            VStack(alignment: .leading, spacing: 8) {
                SkeletonBar(width: 270, height: 12); SkeletonBar(width: 220, height: 12)
            }
        }
        .shimmer()
        .padding(.top, 8)
        .accessibilityLabel(L10n.text("ios.loading_conversation"))
    }
}

/// One turn, following the activity-label rule of Codex in ChatGPT.app: a
/// single row between the agent's activity and its final reply carries the
/// turn's state. While working it reads "Working for 1m 5s" (shimmering) with
/// the steps open beneath it; once finished it reads "Worked for 1m 5s", the
/// steps fold behind it, and only the final reply stays.
private struct TurnView: View {
    var turn: ChatTurn
    var since: Date
    var activity: String?
    @State private var expanded = false
    @State private var pin = HeaderPin()
    @Environment(\.readingTurn) private var readingTurn
    @Environment(\.keepTurnHeader) private var keepTurnHeader

    private var headerID: String { "turn-header-\(turn.id.uuidString)" }

    /// While working, only the latest steps show, so a turn stays within one
    /// screen and the newest work is in view; earlier steps fold into the header.
    static let liveSteps = 3

    /// The steps shown: all when expanded; the latest few while working;
    /// when finished only your own mid-turn messages (the summary follows).
    private var visibleWork: [ChatMessage] {
        if expanded { return turn.work }
        if turn.finished {
            // No closing reply (stopped, or ended on a tool): keep the latest
            // agent note, else the last step, so a folded turn still says something.
            let latest = turn.summary == nil ? (turn.work.last(where: \.isReply) ?? turn.work.last(where: { !$0.isUser })) : nil
            // Replies the agent stopped on stay too: they answer a message.
            return turn.work.filter { $0.isUser || $0.isCompaction || turn.closingReplies.contains($0.id) || $0.id == latest?.id }
        }
        // Working: only the latest few items, yours included, so the turn's
        // header with "Working for…" stays in view; earlier ones fold into it.
        return Array(turn.work.suffix(Self.liveSteps))
    }

    private var foldable: Bool {
        turn.finished ? !turn.work.isEmpty : turn.work.count > Self.liveSteps
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let prompt = turn.prompt { MessageView(message: prompt) }
            if !turn.finished || !turn.work.isEmpty { header }
            let latestNote = visibleWork.last(where: \.isReply)?.id
            ForEach(visibleWork) { message in
                // Earlier notes stay short unless you open the turn; the latest
                // one shows in full, with its tables and lists.
                MessageView(message: message, lineLimit: expanded || message.id == latestNote ? nil : 6)
            }
            if !turn.finished, let activity, !activity.isEmpty {
                // What the agent says it is doing now (Herdr status.msg).
                Text(activity).font(.system(size: 13.5)).foregroundStyle(Theme.fg2).lineLimit(2)
            }
            if let summary = turn.summary { MessageView(message: summary) }
        }
    }

    private var header: some View {
        Button {
            guard foldable else { return }
            // Opening a turn is reading it: keep it where it is on screen.
            readingTurn()
            // The header stays where you tapped it while the turn opens or
            // closes and its messages settle their heights. No layout
            // animation: a lazy list animating a large height change keeps
            // re-estimating the turns around it, and the whole page jumps.
            pin.start()
            expanded.toggle()
        } label: {
            HStack(spacing: 6) {
                if turn.finished {
                    Text(turn.stopped
                         ? turn.duration.map { L10n.text("ios.stopped_after", values: ["duration": Self.format($0)]) } ?? L10n.text("ios.stopped")
                         : turn.duration.map { L10n.text("ios.worked_for", values: ["duration": Self.format($0)]) } ?? L10n.text("ios.worked"))
                } else {
                    TimelineView(.periodic(from: .now, by: 1)) { context in
                        let elapsed = context.date.timeIntervalSince(since)
                        Text(elapsed < 2 ? L10n.text("ui.working") : L10n.text("ios.working_for", values: ["duration": Self.format(elapsed)])).monospacedDigit()
                    }
                    .textShimmer()
                }
                Spacer(minLength: 8)
                if foldable {
                    Image(systemName: "chevron.right").font(.system(size: 11, weight: .semibold))
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                        .animation(.snappy, value: expanded)
                }
            }
            .font(.system(size: 14)).foregroundStyle(Theme.fg2)
            .padding(.bottom, 10)
            .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 1) }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .id(headerID)
        .onGeometryChange(for: CGRect.self, of: { $0.frame(in: .scrollView) }) { frame in
            pin.frame = frame
            guard let target = pin.target else { return }
            guard Date.now < pin.until, pin.corrections < 6 else { pin.target = nil; return }
            let off = frame.minY - target
            guard abs(off) > 0.5 else { return }
            pin.asked -= off
            pin.corrections += 1
            if !keepTurnHeader(headerID, pin.asked, frame.height) { pin.target = nil }
        }
        .accessibilityHint(foldable ? (expanded ? L10n.text("ios.hide_steps") : L10n.text("ios.show_steps")) : "")
    }

    static func format(_ seconds: TimeInterval) -> String {
        WorkingIndicator.elapsed(from: .init(timeIntervalSince1970: 0), to: .init(timeIntervalSince1970: seconds))
    }
}

/// Images pasted into a user message: right-aligned thumbnails; tap for full screen.
private struct PastedImages: View {
    var images: [Data]
    @State private var shown: Int?

    var body: some View {
        let thumbnails = images.compactMap(UIImage.init(data:))
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(Array(thumbnails.enumerated()), id: \.offset) { index, image in
                    Button { shown = index } label: {
                        Image(uiImage: image).resizable().scaledToFill()
                            .frame(width: thumbnails.count == 1 ? 180 : 96, height: thumbnails.count == 1 ? 180 : 96)
                            .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
                            .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Theme.line))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(L10n.text("ios.image_number", values: ["number": String(index + 1)]))
                }
            }
        }
        .defaultScrollAnchor(.trailing)
        .frame(height: thumbnails.count == 1 ? 180 : 96)
        .fullScreenCover(isPresented: Binding(get: { shown != nil }, set: { if !$0 { shown = nil } })) {
            ZStack(alignment: .topTrailing) {
                Color.black.ignoresSafeArea()
                TabView(selection: Binding(get: { shown ?? 0 }, set: { shown = $0 })) {
                    ForEach(Array(thumbnails.enumerated()), id: \.offset) { index, image in
                        ZoomableImage(image: image).tag(index)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: thumbnails.count > 1 ? .automatic : .never))
                Button { shown = nil } label: {
                    Image(systemName: "xmark").font(.system(size: 15, weight: .medium)).frame(width: 40, height: 40)
                }
                .glassEffect(.regular.interactive(), in: Circle())
                .padding(16)
                .accessibilityLabel(L10n.text("ui.close"))
            }
            .environment(\.colorScheme, .dark)
        }
    }
}

/// A full-screen image: pinch or double-tap to zoom, drag to look around.
struct ZoomableImage: View {
    var image: UIImage
    @State private var scale: CGFloat = 1
    @State private var settled: CGFloat = 1
    @State private var offset: CGSize = .zero
    @State private var dragged: CGSize = .zero

    var body: some View {
        Image(uiImage: image).resizable().scaledToFit()
            .scaleEffect(scale)
            .offset(offset)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .contentShape(Rectangle())
            .gesture(MagnifyGesture()
                .onChanged { scale = min(max(settled * $0.magnification, 1), 5) }
                .onEnded { _ in settled = scale; if scale == 1 { reset() } })
            // Panning only while zoomed, so swiping between images still works.
            .simultaneousGesture(scale > 1 ? DragGesture()
                .onChanged { offset = CGSize(width: dragged.width + $0.translation.width, height: dragged.height + $0.translation.height) }
                .onEnded { _ in dragged = offset } : nil)
            .onTapGesture(count: 2) {
                withAnimation(.snappy) {
                    if scale > 1 { reset() } else { scale = 2.5; settled = 2.5 }
                }
            }
            .accessibilityAddTraits(.isImage)
    }

    private func reset() {
        scale = 1; settled = 1; offset = .zero; dragged = .zero
    }
}

/// Your message in its bubble. A long paste shows its first lines; tap to read it all.
private struct UserText: View {
    var text: String
    @State private var expanded = false

    private var long: Bool { text.count > 600 || text.split(separator: "\n", omittingEmptySubsequences: false).count > 12 }

    var body: some View {
        Text(text)
            .lineLimit(long && !expanded ? 12 : nil)
            .onTapGesture { if long { withAnimation(.snappy) { expanded.toggle() } } }
            .accessibilityHint(long ? (expanded ? L10n.text("ios.show_less") : L10n.text("ios.show_message")) : "")
    }
}
