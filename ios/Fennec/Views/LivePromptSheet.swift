import SwiftUI

/// A question a live agent is waiting on, read from its screen by the Go
/// client (mobile/prompt.go): an approval or a choice with numbered options.
struct LivePrompt: Decodable, Equatable {
    struct Option: Decodable, Equatable, Identifiable {
        var key: String
        var title: String
        var id: String { key }
    }
    var question: String?
    var detail: String?
    var options: [Option]?
}

extension SessionStore {
    /// Reads what a waiting session asks; clears it once the session moves on.
    func loadPrompt(_ id: String) async {
        guard let core, let session = session(id), let address = session.address else { return }
        guard session.state == .needsInput else {
            livePrompts[id] = nil
            return
        }
        let json = await Task.detached { try? goCall { core.prompt(address, sessionID: id, error: $0) } }.value
        guard let json, let data = json.data(using: .utf8),
              let prompt = try? JSONDecoder().decode(LivePrompt.self, from: data) else { return }
        // A screen without a menu shows no sheet; the terminal stays one tap away.
        livePrompts[id] = (prompt.options?.count ?? 0) >= 2 ? prompt : nil
    }

    /// Presses the option's key in the session, then reads its new state.
    func answerPrompt(_ id: String, key: String) async -> Bool {
        guard let core, let address = session(id)?.address, !answeringPrompts.contains(id) else { return false }
        answeringPrompts.insert(id)
        defer { answeringPrompts.remove(id) }
        let sent = await Task.detached { (try? goCall { core.answer(address, sessionID: id, key: key, error: $0) }) != nil }.value
        guard sent else { return false }
        livePrompts[id] = nil
        await refresh()
        await loadTranscript(id)
        return true
    }
}

struct LivePromptSheet: View {
    var agent: AgentKind
    var prompt: LivePrompt
    var answering: Bool
    var onAnswer: (String) -> Void
    var onOpenTerminal: () -> Void

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 8) {
                    AgentIcon(agent: agent, size: 20)
                    Text(L10n.text("ios.agent_asks", values: ["agent": agent.displayName]))
                        .font(.system(size: 13, weight: .medium)).foregroundStyle(Theme.fg2)
                }
                Text(prompt.question?.isEmpty == false ? prompt.question! : L10n.text("ios.needs_answer"))
                    .font(.system(size: 17, weight: .medium))
                    .fixedSize(horizontal: false, vertical: true)
                if let detail = prompt.detail, !detail.isEmpty {
                    Text(detail)
                        .font(.system(size: 13, design: .monospaced))
                        .textSelection(.enabled)
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.bg, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                ForEach(prompt.options ?? []) { option in
                    Button { onAnswer(option.key) } label: {
                        HStack(alignment: .firstTextBaseline, spacing: 10) {
                            Text(option.key).font(.system(size: 13, weight: .semibold)).monospacedDigit()
                                .foregroundStyle(Theme.fg3).frame(width: 14)
                            Text(option.title).font(.system(size: 15)).multilineTextAlignment(.leading)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .padding(.horizontal, 14).padding(.vertical, 12)
                        .background(Theme.surface2, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                        .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .disabled(answering)
                    .accessibilityIdentifier("prompt-option-\(option.key)")
                }
                Button(L10n.text("ios.open_terminal"), action: onOpenTerminal)
                    .font(.system(size: 14, weight: .medium)).foregroundStyle(Theme.fg2)
                    .frame(maxWidth: .infinity).padding(.top, 4)
                if answering { ProgressView().frame(maxWidth: .infinity) }
            }
            .padding(20)
        }
        .presentationDetents([.medium, .large])
        .presentationBackgroundInteraction(.enabled(upThrough: .medium))
    }
}

/// Above the composer while a dismissed question still waits.
struct PromptWaitingPill: View {
    var action: () -> Void
    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Circle().fill(Theme.accent).frame(width: 7, height: 7)
                Text(L10n.text("ios.needs_answer")).font(.system(size: 13, weight: .medium))
                Image(systemName: "chevron.up").regularIconWeight().font(.system(size: 10, weight: .semibold))
            }
            .padding(.horizontal, 14).frame(height: 32)
            .glassEffect(.regular.interactive(), in: Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("prompt-waiting")
    }
}
