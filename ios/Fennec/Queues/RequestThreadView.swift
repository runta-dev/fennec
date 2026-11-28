import PhotosUI
import SwiftUI

/// 25–32: one request's thread. Messages, then a card saying where it
/// stands: waiting in line, starting, or the agent's live output.
struct RequestThreadView: View {
    @Environment(QueueStore.self) private var queues
    @Environment(SessionStore.self) private var sessions
    var projectID: String
    var number: Int
    @Binding var path: [QueueRoute]
    @State private var draft = ""
    @State private var images = ImageDraft()
    @State private var showMenu = false
    @State private var linking = false
    /// Bumped when the Marked done banner appears, to bounce its check.
    @State private var bannerBounce = 0
    @State private var composerFocused = false
    @State private var composerResetRevision = 0

    var body: some View {
        let project = queues.project(projectID)
        let request = queues.thread(projectID, number)
        Group {
            if let project, let request {
                ScrollViewReader { proxy in
                    ScrollView {
                        // Not lazy (FEN-70): a lazy stack anchored at the bottom
                        // guessed the height of rows it hadn't built, so opening a
                        // thread (and the full messages replacing the latest one)
                        // scrolled past the real content: blank until you scrolled.
                        // Threads are short enough to build all at once.
                        VStack(alignment: .leading, spacing: 14) {
                            if let related = request.related, !related.isEmpty {
                                LinkedRequestsCard(related: related, project: project) { n in path.append(.thread(projectID, n)) }
                            }
                            let messages = request.messages ?? [request.latest].compactMap { $0 }
                            let hasFeedback = messages.contains { $0.fromOutside }
                            ForEach(messages) { message in
                                ThreadMessage(message: message, project: project, showOwner: hasFeedback,
                                              openRequest: { n in path.append(.thread(projectID, n)) })
                            }
                            if request.status_ == .resolved, let closed = request.closed { ClosureCard(closure: closed) }
                            if request.status_ == .failed { FailureCard(request: request) }
                            statusCard(request, project)
                            actions(request)
                            // Scroll target under everything (FEN-114). It was the
                            // status card, so opening a thread stopped above Mark
                            // done, and in review (no card) it had nothing to hit.
                            Color.clear.frame(height: 1).id("bottom")
                        }
                        .padding(.horizontal, 16)
                        .padding(.vertical, 12)
                        .animation(.smooth(duration: 0.3), value: request.status)
                    }
                    .defaultScrollAnchor(.bottom)
                    .scrollDismissesKeyboard(.interactively)
                    .onChange(of: request.messages?.count) { old, _ in
                        // The first full load jumps without animation; only new
                        // messages in an open thread slide in.
                        if old == nil { proxy.scrollTo("bottom", anchor: .bottom) }
                        else { withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } }
                    }
                    .task(id: number) {
                        // Markdown, images and the composer inset settle after the
                        // first layout; land on the real bottom once they have,
                        // like ChatView does.
                        try? await Task.sleep(for: .milliseconds(250))
                        proxy.scrollTo("bottom", anchor: .bottom)
                    }
                }
                .safeAreaInset(edge: .bottom, spacing: 0) { bottom(request, project) }
                .toolbar {
                    ToolbarItem(placement: .principal) { titlePill(request, project) }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { showMenu = true } label: { Image(systemName: "ellipsis").regularIconWeight() }
                            .accessibilityLabel(L10n.text("ios.request_menu"))
                            .accessibilityIdentifier("request-menu")
                    }
                }
                .sheet(isPresented: $showMenu) {
                    RequestMenuSheet(request: request, project: project, liveSession: liveSession(request),
                                     link: { showMenu = false; linking = true },
                                     openSession: { id in showMenu = false; path.append(.chat(id)) })
                        .presentationDetents([.medium])
                }
                .sheet(isPresented: $linking) { LinkSheet(project: project, request: request) }
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(Theme.bg)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
        .queueActionAlert()
        .task {
            // Pushed while it is open (FEN-60); read here only without a stream.
            queues.follow(thread: number, in: projectID)
            while !Task.isCancelled {
                if !queues.pushes(thread: number, in: projectID) || queues.thread(projectID, number) == nil { await queues.loadThread(projectID, number) }
                if let r = queues.thread(projectID, number), r.status_.running {
                    await sessions.refresh(transcripts: false)
                    // The live card's preview comes from the log, which no
                    // session list reads any more (FEN-108).
                    if let live = liveSession(r) { await sessions.loadTranscript(live.id) }
                }
                try? await Task.sleep(for: .seconds(3))
            }
        }
        .onDisappear { queues.unfollow(thread: number, in: projectID) }
    }

    private func titlePill(_ request: BoardRequest, _ project: BoardProject) -> some View {
        VStack(spacing: 1) {
            Text(request.title).font(.system(size: 14, weight: .semibold)).lineLimit(1)
            HStack(spacing: 4) {
                Text(request.reference(project.key)).font(.system(size: 10.5, design: .monospaced))
                Text("·")
                Circle().fill(request.status_.color).frame(width: 6, height: 6)
                Text(request.status_.label)
            }
            .font(.system(size: 11)).foregroundStyle(Theme.fg2)
        }
        .frame(maxWidth: 230)
        .animation(.smooth(duration: 0.3), value: request.status)
        .accessibilityElement(children: .combine)
    }

    // MARK: The card at the end (25, 25b, 26, 27)

    /// The session showing this request's live output, if the phone sees it.
    private func liveSession(_ request: BoardRequest) -> Session? {
        for id in request.runSessions { if let s = sessions.session(id) { return s } }
        return nil
    }

    @ViewBuilder
    private func statusCard(_ request: BoardRequest, _ project: BoardProject) -> some View {
        let live = liveSession(request)
        if request.status_.running, let live {
            LiveCard(session: live) { path.append(.chat(live.id)) }
        } else if request.status_.running {
            // Dispatched but no session yet says Starting…; a run the phone
            // can't see (on the Mac) shows the agent and its status.
            let starting = request.runs?.last?.state == "starting"
            WaitCard(icon: AnyView(AgentIcon(agent: request.agentKind ?? project.firstAgent ?? .claudeCode, size: 20)),
                     title: (request.agentKind ?? project.firstAgent)?.displayName ?? "",
                     dot: Theme.working, detail: starting ? L10n.text("desktop.queued_starting") : request.status_.label,
                     hint: nil, hintAgent: nil, inline: true)
        } else if request.status_ == .queued {
            let wait = QueueWait.of(queues.list(projectID), number: request.number, limit: project.queue, paused: project.paused)
            WaitCard(icon: AnyView(Image(systemName: "clock").regularIconWeight().font(.system(size: 16)).foregroundStyle(Theme.fg2)),
                     title: L10n.text("desktop.queued_wait"), dot: Theme.fg3, detail: wait.text,
                     hint: L10n.text(wait == .paused ? "desktop.queued_hint_paused" : "desktop.queued_hint"),
                     hintAgent: project.firstAgent, inline: false)
        }
    }

    // MARK: Composer and actions (28, 29, 32)

    /// Retry and Mark done sit at the end of the thread, under what the
    /// agent said, not right above the composer where they were easy to hit
    /// by mistake (FEN-22). A done request's Reopen is in the composer, where
    /// Send goes while it is empty (FEN-220); sending a message reopens it
    /// too (FEN-118).
    @ViewBuilder
    private func actions(_ request: BoardRequest) -> some View {
        if request.status_.canMarkDone {
            HStack(spacing: 8) {
                Spacer(minLength: 0)
                if request.status_ == .failed {
                    glassButton(L10n.text("desktop.retry_request"), "arrow.clockwise") {
                        Task { await queues.reply(projectID, number, text: "") }
                    }
                }
                glassButton(L10n.text("desktop.resolve"), "checkmark") { Task { await queues.markDone(projectID, number) } }
            }
            .padding(.top, 4)
            .transition(.opacity)
        }
    }

    /// Shown for a few seconds after Mark done: the thread is closed, with
    /// a way back if that was a mistake (FEN-22).
    private var resolvedBanner: some View {
        HStack(spacing: 10) {
            Image(systemName: "checkmark.circle.fill").regularIconWeight()
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(Theme.working)
                .symbolEffect(.bounce, options: .nonRepeating, value: bannerBounce)
                .onAppear { bannerBounce += 1 }
            Text(L10n.text("ios.marked_done")).font(.system(size: 15, weight: .medium))
            Spacer(minLength: 0)
            Button(L10n.text("ios.undo")) {
                queues.justResolved = nil
                Task { await queues.reopen(projectID, number) }
            }
            .font(.system(size: 15, weight: .semibold))
            .foregroundStyle(Theme.accent)
            .accessibilityIdentifier("undo-mark-done")
        }
        .padding(.horizontal, 16).frame(height: 48)
        .glassEffect(.regular, in: Capsule())
        .padding(.horizontal, 16)
        .transition(.move(edge: .bottom).combined(with: .opacity).combined(with: .scale(scale: 0.9)))
        .accessibilityElement(children: .contain)
    }

    private func bottom(_ request: BoardRequest, _ project: BoardProject) -> some View {
        VStack(spacing: 8) {
            if queues.justResolved == "\(projectID)/\(queues.boardNumber(projectID, number))" { resolvedBanner }
            ImageDraftStrip(draft: $images).padding(.horizontal, 16)
            HStack(alignment: .bottom, spacing: 8) {
                ImagePickerButton(draft: $images)
                ZStack(alignment: .leading) {
                    if draft.isEmpty {
                        Text(placeholder(request))
                            .font(.system(size: 16)).foregroundStyle(Theme.fg3)
                            .allowsHitTesting(false).accessibilityHidden(true)
                    }
                    ComposerTextView(text: $draft, focused: $composerFocused,
                                     placeholder: placeholder(request), canSend: canSend,
                                     onSend: { send(request) }, resetRevision: composerResetRevision,
                                     sendsOnReturn: false, identifier: "thread-composer")
                }
                if request.status_ == .resolved && !canSend && !images.uploading {
                    // FEN-220: a done request offers Reopen where Send goes;
                    // typing brings Send back (a message reopens it too).
                    Button(L10n.text("desktop.reopen")) {
                        Task { await queues.reopen(projectID, number) }
                    }
                    .font(.system(size: 14, weight: .medium))
                    .foregroundStyle(Theme.fg)
                    .padding(.horizontal, 14).frame(height: 36)
                    .background(Theme.fg.opacity(0.08), in: Capsule())
                    .accessibilityIdentifier("thread-reopen")
                } else {
                Button { send(request) } label: {
                    Group {
                        Image(systemName: "arrow.up").regularIconWeight().font(.system(size: 15, weight: .semibold))
                    }
                    .frame(width: 36, height: 36)
                    .foregroundStyle(Theme.onPrimary)
                    .background(Theme.primary.opacity(canSend ? 1 : 0.35), in: Circle())
                }
                .disabled(!canSend)
                .accessibilityLabel(L10n.text("ui.send"))
                .accessibilityIdentifier("thread-send")
                }
            }
            .padding(.leading, 8).padding(.trailing, 6).padding(.vertical, 6)
            .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
            .padding(.horizontal, 12)
            .padding(.bottom, 6)
        }
        .keepsKeyboard()
        .animation(.spring(duration: 0.4, bounce: 0.3), value: queues.justResolved)
    }

    private func placeholder(_ request: BoardRequest) -> String {
        L10n.text("desktop.reply")
    }

    private var canSend: Bool {
        !images.uploading && (!draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !images.done.isEmpty)
    }

    /// The composer empties as soon as Send is tapped, like a chat, not
    /// after the board answers and the thread reloads; the reply showed up
    /// in the thread while its text stayed in the box, and a pinyin commit
    /// after the tap kept it there for good (FEN-24). A failed send puts
    /// the text and images back unless something new was typed meanwhile.
    private func send(_ request: BoardRequest) {
        guard canSend else { return }
        let text = draft
        let sent = images
        draft = ""
        composerResetRevision += 1
        images = ImageDraft()
        Task {
            if await queues.reply(projectID, number, text: text, images: sent.done) { return }
            if draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                draft = text
                composerResetRevision += 1
            }
            if images.done.isEmpty && !images.uploading { images = sent }
        }
    }

    private func glassButton(_ title: String, _ icon: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Label { Text(title) } icon: { Image(systemName: icon).regularIconWeight() }.font(.system(size: 14, weight: .medium))
                .padding(.horizontal, 16).frame(height: 36)
        }
        .buttonStyle(.plain)
        .glassEffect(.regular.interactive(), in: Capsule())
    }
}

/// The desktop's thread_wait card (THI-95): a tile, a title with a dot, a
/// detail line, and a hint with the agent that will take it.
struct WaitCard: View {
    var icon: AnyView
    var title: String
    var dot: Color
    var detail: String
    var hint: String?
    var hintAgent: AgentKind?
    /// Starting: the detail sits on the title line.
    var inline: Bool

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            icon
                .frame(width: 34, height: 34)
                .background(Theme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Theme.line))
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(title).font(.system(size: 14.5, weight: .medium))
                    Circle().fill(dot).frame(width: 6, height: 6)
                    if inline { Text(detail).font(.system(size: 13)).foregroundStyle(Theme.fg2) }
                }
                if !inline { Text(detail).font(.system(size: 13)).foregroundStyle(Theme.fg2).monospacedDigit() }
                if let hint {
                    HStack(alignment: .top, spacing: 7) {
                        if let hintAgent { AgentIcon(agent: hintAgent, size: 14).padding(.top, 2) }
                        Text(hint).font(.system(size: 13)).foregroundStyle(Theme.fg3)
                    }
                    .padding(.top, 4)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(.top, 4)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("thread-wait")
    }
}

/// The agent's live output (the desktop's peek card); opens the session.
struct LiveCard: View {
    var session: Session
    var open: () -> Void

    var body: some View {
        Button(action: open) {
            VStack(alignment: .leading, spacing: 7) {
                HStack(spacing: 7) {
                    AgentIcon(agent: session.agent, size: 16)
                    Text(session.agent.displayName).font(.system(size: 13.5, weight: .medium))
                    Spacer()
                    Text(L10n.text("desktop.view_session")).font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                    Image(systemName: "chevron.right").regularIconWeight().font(.system(size: 11)).foregroundStyle(Theme.fg3)
                }
                Text(lines).font(.system(size: 11.5, design: .monospaced)).foregroundStyle(Theme.fg2)
                    .lineLimit(4).frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(12)
            .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).strokeBorder(Theme.line))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("thread-live")
    }

    private var lines: String {
        if let activity = session.activity, !activity.isEmpty { return activity }
        let p = session.preview
        return p.isEmpty ? L10n.text("ui.working") : p
    }
}

struct FailureCard: View {
    var request: BoardRequest
    var body: some View {
        let failure = request.failure ?? ""
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "exclamationmark.circle").regularIconWeight().foregroundStyle(Theme.danger)
            Text(failure.isEmpty || failure == "The agent stopped without reporting back" ? L10n.text("desktop.stalled") : failure)
                .font(.system(size: 13.5)).foregroundStyle(Theme.fg)
            Spacer(minLength: 0)
        }
        .padding(12)
        .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Theme.danger))
    }
}

/// FEN-220: the agent closed the request itself. "✓ claude closed this
/// request · 10:42" over its reason, as the thread's last word.
struct ClosureCard: View {
    var closure: BoardClosure
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Divider()
            HStack(spacing: 7) {
                Image(systemName: "checkmark.circle.fill").regularIconWeight().foregroundStyle(Theme.working)
                Text(AgentKind(launcherID: closure.by)?.displayName ?? closure.by)
                    .fontWeight(.semibold).foregroundStyle(Theme.fg)
                Text(L10n.text("desktop.closed_this_request"))
                if let date = closure.date { Text("· \(shortTime(date))").foregroundStyle(Theme.fg3) }
            }
            .font(.system(size: 13)).foregroundStyle(Theme.fg2)
            (Text(L10n.text("desktop.close_reason")).foregroundStyle(Theme.fg2) + Text(closure.reason).foregroundStyle(Theme.fg))
                .font(.system(size: 14.5)).textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .background(Theme.fg.opacity(0.05), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("thread-closure")
    }
}

/// One message: yours in a bubble, an agent's as a reply with its name.
struct ThreadMessage: View {
    var message: BoardMessage
    var project: BoardProject
    /// The thread has Feedback (FEN-222): the project's people get an Owner badge.
    var showOwner = false
    var openRequest: (Int) -> Void

    var body: some View {
        if let external = message.external, !message.fromAgent {
            // Feedback from outside the project: name, channel, then the setup it came from.
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 7) {
                    Text(external.name).foregroundStyle(Theme.fg)
                    MessageChip(text: external.channelLabel, color: Theme.fg2)
                    if let date = message.date { Text(shortTime(date)) }
                }
                .font(.system(size: 12.5)).foregroundStyle(Theme.fg2)
                MarkdownText(text: message.body)
                    .font(.system(size: 15.5)).textSelection(.enabled)
                AttachmentThumbnails(attachments: message.attachments ?? [], trailing: false)
                if !external.details.isEmpty {
                    Text(external.details).font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } else if message.fromAgent {
            VStack(alignment: .leading, spacing: 6) {
                HStack(spacing: 7) {
                    if let agent = message.agent.flatMap(AgentKind.init(launcherID:)) {
                        AgentIcon(agent: agent, size: 16)
                        Text(agent.displayName)
                    } else { Text(message.agent ?? "") }
                    if let date = message.date { Text("· \(shortTime(date))") }
                }
                .font(.system(size: 12.5)).foregroundStyle(Theme.fg2)
                // Same size as agent replies in a session (PreviewMessageText) and
                // the bubbles here; unsized it fell back to 17pt .body (FEN-56).
                MarkdownText(text: message.body)
                    .font(.system(size: 15.5)).textSelection(.enabled)
                    .environment(\.openURL, OpenURLAction { url in route(url) })
                AttachmentThumbnails(attachments: message.attachments ?? [], trailing: false)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            VStack(alignment: .trailing, spacing: 6) {
                if showOwner { MessageChip(text: L10n.text("desktop.owner_badge"), color: Theme.accent) }
                AttachmentThumbnails(attachments: message.attachments ?? [], trailing: true)
                if !message.body.isEmpty {
                    Text(message.body)
                        .font(.system(size: 15.5))
                        .textSelection(.enabled)
                        .padding(.horizontal, 14).padding(.vertical, 10)
                        .background(Theme.bubble, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                }
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .padding(.leading, 40)
            // On its way to the board (FEN-58): a stand-in with a negative ID.
            .opacity(message.id < 0 ? 0.6 : 1)
        }
    }

    /// THI-<n> links open that thread here; others open as usual.
    private func route(_ url: URL) -> OpenURLAction.Result {
        let text = url.absoluteString
        if let range = text.range(of: "\(project.key)-", options: .backwards),
           let n = Int(text[range.upperBound...].prefix { $0.isNumber }), !text.hasPrefix("http") {
            openRequest(n)
            return .handled
        }
        return .systemAction
    }
}

/// A small label next to a message author: the Feedback channel, or Owner.
struct MessageChip: View {
    var text: String
    var color: Color

    var body: some View {
        Text(text)
            .font(.system(size: 11, weight: .medium))
            .foregroundStyle(color)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .background(color.opacity(0.12), in: RoundedRectangle(cornerRadius: 5, style: .continuous))
    }
}

/// Images in a message: the board's thumbnails; tap for the full image.
struct AttachmentThumbnails: View {
    @Environment(QueueStore.self) private var queues
    var attachments: [BoardAttachment]
    var trailing: Bool
    @State private var shown: BoardAttachment?
    @State private var full: UIImage?
    @State private var failed = false

    var body: some View {
        if !attachments.isEmpty {
            HStack(spacing: 6) {
                ForEach(attachments) { a in
                    Button { open(a) } label: {
                        Group {
                            if let image = a.thumbnailImage { Image(uiImage: image).resizable().scaledToFill() }
                            else { Theme.surface2 }
                        }
                        .frame(width: attachments.count == 1 ? 150 : 96, height: attachments.count == 1 ? 150 : 96)
                        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Theme.line))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(L10n.text("desktop.request_image"))
                }
            }
            .frame(maxWidth: .infinity, alignment: trailing ? .trailing : .leading)
            .fullScreenCover(item: $shown) { a in
                ZStack(alignment: .topTrailing) {
                    Color.black.ignoresSafeArea()
                    if let full { ZoomableImage(image: full) }
                    else if failed { Text(L10n.text("desktop.request_image_not_ready")).foregroundStyle(.white.opacity(0.8)) .frame(maxWidth: .infinity, maxHeight: .infinity) }
                    else {
                        ZStack {
                            if let thumb = a.thumbnailImage { Image(uiImage: thumb).resizable().scaledToFit().blur(radius: 6) }
                            ProgressView().tint(.white)
                        }
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                    }
                    Button { shown = nil } label: {
                        Image(systemName: "xmark").regularIconWeight().font(.system(size: 15, weight: .medium)).frame(width: 40, height: 40)
                    }
                    .glassEffect(.regular.interactive(), in: Circle())
                    .padding(16)
                    .accessibilityLabel(L10n.text("ui.close"))
                }
                .environment(\.colorScheme, .dark)
            }
        }
    }

    private func open(_ a: BoardAttachment) {
        full = nil; failed = false; shown = a
        Task {
            do { full = UIImage(data: try await queues.imageData(a)) ; failed = full == nil }
            catch { failed = true }
        }
    }
}

/// Linked requests at the top of a thread (31), like the desktop.
struct LinkedRequestsCard: View {
    var related: [BoardRelated]
    var project: BoardProject
    var open: (Int) -> Void
    @State private var expanded = false

    var body: some View {
        let shown = expanded ? related : Array(related.prefix(4))
        VStack(alignment: .leading, spacing: 2) {
            Text(L10n.text("desktop.linked_requests")).font(.system(size: 12.5, weight: .medium)).foregroundStyle(Theme.fg3)
                .padding(.bottom, 4)
            ForEach(shown) { r in
                Button { open(r.number) } label: {
                    HStack(spacing: 8) {
                        Text(r.label).font(.system(size: 11.5)).foregroundStyle(Theme.fg3).frame(width: 82, alignment: .leading).lineLimit(1)
                        Text("\(project.key)-\(r.number)").font(.system(size: 11.5, design: .monospaced)).foregroundStyle(Theme.fg3)
                        Text(r.title).font(.system(size: 13.5)).lineLimit(1)
                        Spacer(minLength: 4)
                        let status = RequestStatus(rawValue: r.status) ?? .unknown
                        Text(status.label).font(.system(size: 11)).foregroundStyle(status.color)
                            .padding(.horizontal, 7).padding(.vertical, 1)
                            .background(Theme.surface2, in: Capsule())
                    }
                    .padding(.vertical, 4)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
            if related.count > 4 && !expanded {
                Button { expanded = true } label: {
                    Text(L10n.text("desktop.link_more_mentions", values: ["n": String(related.count - 4)]))
                        .font(.system(size: 12.5)).foregroundStyle(Theme.fg2)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(12)
        .background(Theme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).strokeBorder(Theme.line))
    }
}

// MARK: Thread menu (30)

struct RequestMenuSheet: View {
    @Environment(QueueStore.self) private var queues
    @Environment(\.dismiss) private var dismiss
    var request: BoardRequest
    var project: BoardProject
    var liveSession: Session?
    var link: () -> Void
    var openSession: (String) -> Void

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack(spacing: 12) {
                    RequestIcon(request: request, project: project, size: 36)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(request.title).font(.system(size: 16, weight: .medium)).lineLimit(2)
                        Text([request.reference(project.key), project.name, request.status_.label, request.author ?? ""]
                            .filter { !$0.isEmpty }.joined(separator: " · "))
                            .font(.system(size: 13)).foregroundStyle(Theme.fg2)
                    }
                }
                ListCard {
                    Menu {
                        ForEach(project.tags) { tag in
                            Button { Task { await queues.setTag(project.id, request.number, tag.id) } } label: {
                                if tag.id == request.tag { Label(tag.label, systemImage: "checkmark") } else { Text(tag.label) }
                            }
                        }
                    } label: {
                        ListRow(icon: "tag", title: L10n.text("ios.tag")) {
                            Text(request.tag.map(project.tagLabel) ?? "").foregroundStyle(Theme.fg3)
                        }
                    }
                    .buttonStyle(.plain)
                    if request.status_ == .queued {
                        row("arrow.up", L10n.text("desktop.run_next")) {
                            Task { await queues.move(project.id, request.number, before: 0) }; dismiss()
                        }
                    }
                    row("link", L10n.text("ios.link_to_request")) { link() }
                    row("doc.on.doc", L10n.text("ios.copy_reference", values: ["ref": request.reference(project.key)])) {
                        UIPasteboard.general.string = request.reference(project.key); dismiss()
                    }
                    if let liveSession {
                        row("terminal", L10n.text("desktop.view_session"), last: true) { openSession(liveSession.id) }
                    } else {
                        ListRow(icon: "terminal", iconColor: Theme.fg3, title: L10n.text("desktop.view_session"), titleColor: Theme.fg3,
                                showDivider: false) { Text(L10n.text("ios.after_it_starts")).font(.system(size: 13)).foregroundStyle(Theme.fg3) }
                    }
                }
                if request.status_.canMarkDone {
                    ListCard { row("checkmark", L10n.text("desktop.resolve"), last: true) { Task { await queues.markDone(project.id, request.number) }; dismiss() } }
                }
            }
            .padding(16)
        }
        .background(Theme.bg)
    }

    private func row(_ icon: String, _ title: String, last: Bool = false, action: @escaping () -> Void) -> some View {
        Button(action: action) { ListRow(icon: icon, title: title, showDivider: !last) }.buttonStyle(.plain)
    }
}

// MARK: Link sheet (31b)

struct LinkSheet: View {
    @Environment(QueueStore.self) private var queues
    @Environment(\.dismiss) private var dismiss
    var project: BoardProject
    var request: BoardRequest
    @State private var kind = "related"
    @State private var query = ""

    var body: some View {
        let linked = Set((queues.thread(project.id, request.number)?.related ?? []).filter { $0.incoming != true }.map(\.number))
        let q = query.trimmingCharacters(in: .whitespaces)
        let digits = q.uppercased().split(separator: "-").last.map(String.init) ?? q
        let candidates = queues.list(project.id)
            .filter { $0.number != request.number && !linked.contains($0.number) }
            .filter { q.isEmpty || $0.title.localizedCaseInsensitiveContains(q) || String($0.number).hasPrefix(digits) }
            .sorted { $0.updatedDate > $1.updatedDate }
        NavigationStack {
            VStack(spacing: 12) {
                Picker("", selection: $kind) {
                    Text(L10n.text("desktop.link_related")).tag("related")
                    Text(L10n.text("desktop.link_duplicate_of")).tag("duplicate")
                }
                .pickerStyle(.segmented)
                HStack(spacing: 10) {
                    Image(systemName: "magnifyingglass").regularIconWeight().foregroundStyle(Theme.fg2)
                    TextField(L10n.text("desktop.link_search"), text: $query)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                }
                .padding(.horizontal, 14).frame(height: 42)
                .background(Theme.surface, in: Capsule())
                ScrollView {
                    if candidates.isEmpty {
                        Text(L10n.text("desktop.link_none")).font(.rowBody).foregroundStyle(Theme.fg2).padding(.top, 30)
                    }
                    LazyVStack(spacing: 0) {
                        ForEach(candidates.prefix(40)) { r in
                            Button {
                                Task { await queues.link(project.id, request.number, to: r.number, kind: kind); dismiss() }
                            } label: { RequestRow(request: r, project: project, line: r.status_.label) }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .padding(16)
            .background(Theme.bg)
            .navigationTitle(L10n.text("ios.link_title", values: ["ref": request.reference(project.key)]))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button(L10n.text("desktop.cancel")) { dismiss() } } }
        }
    }
}

// MARK: Images in the composer (32, 33)

/// Images picked for a request or reply, uploaded to the board as soon as
/// they are picked (the board keeps them by content, so this is safe).
struct ImageDraft: Equatable {
    var done: [BoardAttachment] = []
    var previews: [String: UIImage] = [:]
    var uploading = false
    var error: String?
}

struct ImagePickerButton<Content: View>: View {
    @Environment(QueueStore.self) private var queues
    @Binding var draft: ImageDraft
    /// Draws the button; gets whether an upload is running.
    var content: (Bool) -> Content
    @State private var picking = false
    @State private var picked: [PhotosPickerItem] = []

    init(draft: Binding<ImageDraft>, @ViewBuilder content: @escaping (Bool) -> Content) {
        _draft = draft
        self.content = content
    }

    var body: some View {
        Button { picking = true } label: { content(draft.uploading) }
        .disabled(draft.uploading || draft.done.count >= 8)
        .accessibilityLabel(L10n.text("ios.attach"))
        .photosPicker(isPresented: $picking, selection: $picked, maxSelectionCount: max(1, 8 - draft.done.count), matching: .images)
        .onChange(of: picked) { _, items in
            guard !items.isEmpty else { return }
            picked = []
            Task { await add(items) }
        }
    }

    private func add(_ items: [PhotosPickerItem]) async {
        draft.uploading = true
        draft.error = nil
        defer { draft.uploading = false }
        for item in items {
            guard draft.done.count < 8 else { draft.error = L10n.text("desktop.request_image_limit"); break }
            guard let data = try? await item.loadTransferable(type: Data.self), let image = UIImage(data: data) else {
                draft.error = L10n.text("desktop.request_image_error"); continue
            }
            // PNG and JPEG go as they are; anything else (HEIC) as JPEG.
            let bytes = ImagePicking.isPNGOrJPEG(data) ? data : (image.jpegData(compressionQuality: 0.9) ?? data)
            guard bytes.count <= 8 << 20 else { draft.error = L10n.text("desktop.request_image_error"); continue }
            do {
                let attachment = try await queues.upload(bytes)
                if !draft.done.contains(attachment) { draft.done.append(attachment) }
                draft.previews[attachment.id] = image
            } catch {
                draft.error = L10n.text("desktop.request_image_error")
            }
        }
    }

}

/// The round "+" used in composer bars.
extension ImagePickerButton where Content == AnyView {
    init(draft: Binding<ImageDraft>) {
        self.init(draft: draft) { uploading in
            AnyView(Group {
                if uploading { ProgressView() } else { Image(systemName: "plus").regularIconWeight().font(.system(size: 17, weight: .medium)) }
            }
            .frame(width: 36, height: 36))
        }
    }
}

enum ImagePicking {
    static func isPNGOrJPEG(_ data: Data) -> Bool {
        let head = [UInt8](data.prefix(4))
        return head.starts(with: [0x89, 0x50, 0x4E, 0x47]) || head.starts(with: [0xFF, 0xD8, 0xFF])
    }
}

struct ImageDraftStrip: View {
    @Binding var draft: ImageDraft

    var body: some View {
        if !draft.done.isEmpty || draft.uploading || draft.error != nil {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(draft.done) { a in
                        Group {
                            if let image = draft.previews[a.id] ?? a.thumbnailImage { Image(uiImage: image).resizable().scaledToFill() }
                            else { Theme.surface2 }
                        }
                        .frame(width: 44, height: 56)
                        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
                        .overlay(alignment: .topTrailing) {
                            Button { draft.done.removeAll { $0.id == a.id } } label: {
                                Image(systemName: "xmark").regularIconWeight().font(.system(size: 9, weight: .semibold))
                                    .frame(width: 18, height: 18)
                                    .foregroundStyle(Theme.bg).background(Theme.fg, in: Circle())
                            }
                            .offset(x: 6, y: -6)
                            .accessibilityLabel(L10n.text("ios.remove_attachment"))
                        }
                    }
                    if draft.uploading {
                        Text(L10n.text("desktop.request_image_adding")).font(.system(size: 12.5)).foregroundStyle(Theme.fg3)
                    } else if let error = draft.error {
                        Text(error).font(.system(size: 12.5)).foregroundStyle(Theme.danger)
                    }
                }
                .padding(.top, 6)
            }
        }
    }
}
