import SwiftUI

/// 06 chat, 07 approval, 08 question, 09 done. Messages scroll under the
/// glass title pill and composer; requests from the agent rise as sheets.
struct ChatView: View {
    @Environment(SessionStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    var sessionID: String
    @State private var draft = ""
    @State private var showMenu = false
    @State private var denyReason = ""
    @State private var followBottom = true
    @State private var showTerminal = false
    @FocusState private var composerFocused: Bool

    var body: some View {
        if let session = store.session(sessionID) {
            content(session)
        } else {
            Text("This session has ended.").foregroundStyle(Theme.fg2)
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
                VStack(alignment: .leading, spacing: 14) {
                    if !session.transcriptLoaded {
                        ChatSkeleton()
                    } else if session.messages.isEmpty, let error = session.transcriptError {
                        VStack(spacing: 12) {
                            Text(error).font(.system(size: 14)).foregroundStyle(Theme.fg2).multilineTextAlignment(.center)
                            Button("Open terminal") { showTerminal = true }.buttonStyle(GlassCapsuleButtonStyle()).frame(width: 200)
                        }
                        .frame(maxWidth: .infinity).padding(.top, 80)
                    }
                    let turns = ChatTurn.turns(session.messages, working: session.state == .working, byPrompt: session.agent == .claudeCode)
                    ForEach(turns) { turn in
                        TurnView(turn: turn,
                                 since: turn.prompt?.time ?? session.turnStarted ?? session.statusSince ?? session.updatedAt,
                                 activity: session.activity)
                    }
                    if session.state == .working && turns.isEmpty {
                        WorkingIndicator(since: session.turnStarted ?? session.statusSince ?? session.updatedAt,
                                         activity: session.activity)
                            .id("working")
                    }
                    Color.clear.frame(height: 1).id("bottom")
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
            }
            .defaultScrollAnchor(.bottom)
            .scrollDismissesKeyboard(.interactively)
            // Follow the newest message as the content grows (new lines,
            // tables and images settling their heights), unless you have
            // scrolled up to read; scrolling back down resumes following.
            .onScrollGeometryChange(for: CGFloat.self, of: { $0.contentSize.height }) { _, _ in
                if followBottom { proxy.scrollTo("bottom", anchor: .bottom) }
            }
            .onScrollPhaseChange { _, phase, context in
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
                withAnimation { proxy.scrollTo("bottom", anchor: .bottom) }
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
                    .accessibilityLabel("Session menu")
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) { composer(session) }
        .task(id: session.id) {
            while !Task.isCancelled {
                // The home list's refresh stops while this page is in front, so
                // keep Herdr's state current here: it is what ends "Working".
                await store.refresh()
                await store.loadTranscript(session.id)
                // New lines arrive fast while the agent works; idle logs rarely change.
                try? await Task.sleep(for: .seconds(store.session(session.id)?.state == .working ? 1.5 : 4))
            }
        }
        .navigationDestination(isPresented: $showTerminal) {
            if let address = session.address { TerminalPage(session: session, address: address) }
        }
        .sheet(isPresented: $showMenu) {
            SessionMenu(session: session, onStop: {
                store.stop(session.id)
                dismiss()
            }, onOpenTerminal: session.address == nil ? nil : {
                showMenu = false
                showTerminal = true
            })
            .presentationDetents([.height(360)])
        }
    }

    private func demoContent(_ session: Session) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 14) {
                    ForEach(session.messages) { message in
                        MessageView(message: message).id(message.id)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
            }
            .defaultScrollAnchor(.bottom)
            .scrollDismissesKeyboard(.interactively)
            .onChange(of: session.messages.count) {
                if let last = session.messages.last { withAnimation { proxy.scrollTo(last.id, anchor: .bottom) } }
            }
        }
        .background(Theme.bg)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) { TitlePill(session: session) }
            ToolbarItem(placement: .topBarTrailing) {
                Button { showMenu = true } label: { Image(systemName: "ellipsis") }
                    .accessibilityLabel("Session menu")
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
        HStack(spacing: 8) {
            Image(systemName: "plus")
                .font(.system(size: 17, weight: .medium))
                .frame(width: 36, height: 36)
                .accessibilityLabel("Attach")
            TextField("Message \(session.agent.displayName)", text: $draft, axis: .vertical)
                .lineLimit(1...5)
                .focused($composerFocused)
                .font(.system(size: 16))
            if draft.isEmpty && session.state == .working {
                Button { } label: {
                    Image(systemName: "stop.fill").font(.system(size: 13))
                        .frame(width: 36, height: 36)
                        .foregroundStyle(Theme.onPrimary)
                        .background(Theme.primary, in: Circle())
                }
                .accessibilityLabel("Stop turn")
            } else {
                Button {
                    let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !text.isEmpty else { return }
                    store.send(text, to: session.id)
                    draft = ""
                } label: {
                    Image(systemName: "arrow.up").font(.system(size: 15, weight: .semibold))
                        .frame(width: 36, height: 36)
                        .foregroundStyle(Theme.onPrimary)
                        .background(Theme.primary.opacity(draft.isEmpty ? 0.35 : 1), in: Circle())
                }
                .disabled(draft.isEmpty)
                .accessibilityLabel("Send")
            }
        }
        .padding(.leading, 8)
        .padding(.trailing, 6)
        .padding(.vertical, 6)
        .glassEffect(.regular, in: RoundedRectangle(cornerRadius: 26, style: .continuous))
        .padding(.horizontal, 12)
        .padding(.bottom, 6)
    }

    private func approvalSheet(_ session: Session, _ approval: ApprovalRequest) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Allow \(session.agent.displayName) to run this command?")
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
            Button("Approve") { store.approve(session.id, forSession: false) }.buttonStyle(PrimaryButtonStyle())
            Button("Approve for this session") { store.approve(session.id, forSession: true) }.buttonStyle(GlassCapsuleButtonStyle())
            Divider().overlay(Theme.line)
            HStack {
                Image(systemName: "pencil").foregroundStyle(Theme.fg3)
                TextField("Tell \(session.agent.displayName) what to do", text: $denyReason)
                Button("Deny") {
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
                TextField("Type something else", text: $custom, onEditingChanged: { typing = $0 })
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
        case .user(let text):
            VStack(alignment: .trailing, spacing: 6) {
                if !message.images.isEmpty { PastedImages(images: message.images) }
                if !text.isEmpty {
                    HStack {
                        Spacer(minLength: 48)
                        UserText(text: text)
                            .font(.system(size: 15.5))
                            .padding(.horizontal, 14).padding(.vertical, 10)
                            .background(Theme.bubble, in: UnevenRoundedRectangle(topLeadingRadius: 20, bottomLeadingRadius: 20, bottomTrailingRadius: 6, topTrailingRadius: 20, style: .continuous))
                            .textSelection(.enabled)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
        case .agent(let text):
            MarkdownText(text: text, lineLimit: lineLimit)
                .font(.system(size: 15.5))
                .textSelection(.enabled)
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
                Text("Conversation compacted").font(.system(size: 12)).foregroundStyle(Theme.fg2).fixedSize()
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
                    Text("\(files.count) files changed").font(.system(size: 14, weight: .medium))
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
                    Text("\(session.agent.displayName) · \(session.model) · \(session.location == .cloud ? "Cloud" : "Mac")")
                        .font(.system(size: 13)).foregroundStyle(Theme.fg2)
                }
            }
            .padding(.horizontal, 4)
            ListCard {
                Button { onOpenTerminal?() } label: { ListRow(icon: "apple.terminal", iconColor: Theme.fg, title: "Open terminal") }
                    .disabled(onOpenTerminal == nil)
                Button { newTitle = session.title; renaming = true } label: { ListRow(icon: "pencil", iconColor: Theme.fg, title: "Rename") }
                Button { store.togglePin(session.id); dismiss() } label: {
                    ListRow(icon: session.pinned ? "pin.slash" : "pin", iconColor: Theme.fg, title: session.pinned ? "Unpin" : "Pin to top", showDivider: false)
                }
            }
            .buttonStyle(.plain)
            ListCard {
                Button { confirmStop = true } label: {
                    ListRow(icon: "stop.fill", iconColor: Theme.danger, title: "Stop session", titleColor: Theme.danger, showDivider: false)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 20)
        .confirmationDialog("Stop this session?", isPresented: $confirmStop, titleVisibility: .visible) {
            Button("Stop session", role: .destructive) { dismiss(); onStop() }
        } message: {
            Text("This ends the cloud process. Closing the app doesn't stop sessions.")
        }
        .alert("Rename session", isPresented: $renaming) {
            TextField("Title", text: $newTitle)
            Button("Save") { store.rename(session.id, to: newTitle) }
            Button("Cancel", role: .cancel) {}
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
struct WorkingIndicator: View {
    var since: Date
    var activity: String?

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    Text("Working for \(Self.elapsed(from: since, to: context.date))")
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
        .accessibilityLabel("Loading conversation")
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
        Button { if foldable { withAnimation(.snappy) { expanded.toggle() } } } label: {
            HStack(spacing: 6) {
                if turn.finished {
                    Text(turn.stopped
                         ? turn.duration.map { "Stopped after \(Self.format($0))" } ?? "Stopped"
                         : turn.duration.map { "Worked for \(Self.format($0))" } ?? "Worked")
                } else {
                    TimelineView(.periodic(from: .now, by: 1)) { context in
                        let elapsed = context.date.timeIntervalSince(since)
                        Text(elapsed < 2 ? "Working" : "Working for \(Self.format(elapsed))").monospacedDigit()
                    }
                    .textShimmer()
                }
                Spacer(minLength: 8)
                if foldable {
                    Image(systemName: "chevron.right").font(.system(size: 11, weight: .semibold))
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                }
            }
            .font(.system(size: 14)).foregroundStyle(Theme.fg2)
            .padding(.bottom, 10)
            .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 1) }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityHint(foldable ? (expanded ? "Hides the steps" : "Shows the steps") : "")
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
                    .accessibilityLabel("Image \(index + 1)")
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
                        Image(uiImage: image).resizable().scaledToFit().tag(index)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: thumbnails.count > 1 ? .automatic : .never))
                Button { shown = nil } label: {
                    Image(systemName: "xmark").font(.system(size: 15, weight: .medium)).frame(width: 40, height: 40)
                }
                .glassEffect(.regular.interactive(), in: Circle())
                .padding(16)
                .accessibilityLabel("Close")
            }
            .environment(\.colorScheme, .dark)
        }
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
            .accessibilityHint(long ? (expanded ? "Shows less" : "Shows the whole message") : "")
    }
}
