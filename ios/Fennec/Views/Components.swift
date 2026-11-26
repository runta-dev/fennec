import SwiftUI

/// A tiny travelling dash; no text or spinner competes with the message.
struct SendingDash: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        TimelineView(.animation(minimumInterval: 1.0 / 30, paused: reduceMotion)) { context in
            let phase = context.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 1.2) / 1.2
            Capsule().fill(Theme.fg3).frame(width: 8, height: 2)
                .offset(x: reduceMotion ? 0 : CGFloat(sin(phase * .pi * 2)) * 7)
                .frame(width: 28, height: 20)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.text("ios.sending"))
        .accessibilityIdentifier("message-sending")
    }
}

/// An agent's own mark with no backing tile. Monochrome marks are template
/// images, so they follow the foreground color in both themes.
struct AgentIcon: View {
    var agent: AgentKind
    var size: CGFloat = 30
    var state: SessionState?

    var body: some View {
        Image(agent.iconName)
            .resizable()
            .scaledToFit()
            .foregroundStyle(Theme.fg)
            .frame(width: size, height: size)
            .overlay(alignment: .bottomTrailing) {
                if let state { StatusDot(state: state) }
            }
            .accessibilityLabel(agent.displayName)
    }
}

/// Dot pinned to the icon's bottom-right corner.
struct StatusDot: View {
    var state: SessionState
    @State private var dim = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var color: Color {
        switch state {
        case .needsInput: Theme.accent
        case .working: Theme.working
        case .idle: Theme.fg3
        }
    }

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 7, height: 7)
            .background(Circle().stroke(Theme.bg, lineWidth: 4))
            .opacity(dim ? 0.45 : 1)
            .onAppear {
                guard state == .working, !reduceMotion else { return }
                withAnimation(.easeInOut(duration: 1).repeatForever()) { dim = true }
            }
            .accessibilityLabel(state.label)
    }
}

struct FoxMark: View {
    var size: CGFloat
    var body: some View {
        Image("fox").resizable().scaledToFit().frame(width: size, height: size)
            .accessibilityLabel("Fennec").accessibilityIdentifier("fennec-logo")
    }
}

/// Round glass button for navigation (back, more, new, account).
struct GlassCircleButton<Label: View>: View {
    var action: () -> Void
    @ViewBuilder var label: () -> Label

    var body: some View {
        Button(action: action) {
            label()
                .font(.system(size: 17, weight: .medium))
                .foregroundStyle(Theme.fg)
                .frame(width: 44, height: 44)
        }
        .buttonStyle(.plain)
        .glassEffect(.regular.interactive(), in: Circle())
    }
}

struct GroupHeader: View {
    var title: String
    var count: Int?
    var body: some View {
        HStack(spacing: 6) {
            Text(title).font(.groupHeader).foregroundStyle(Theme.fg2)
            if let count { Text("\(count)").font(.system(size: 13)).foregroundStyle(Theme.fg3).monospacedDigit() }
            Spacer()
        }
        .padding(.horizontal, 18)
        .padding(.top, 14)
        .padding(.bottom, 4)
    }
}

/// Grouped list card used in settings-style screens (content layer, opaque).
struct ListCard<Content: View>: View {
    @ViewBuilder var content: () -> Content
    var body: some View {
        VStack(spacing: 0) { content() }
            .background(Theme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).strokeBorder(Theme.line))
    }
}

struct ListRow<Trailing: View>: View {
    var icon: String?
    var iconColor: Color = Theme.fg2
    var title: String
    var subtitle: String?
    var titleColor: Color = Theme.fg
    var showDivider = true
    @ViewBuilder var trailing: () -> Trailing

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 14) {
                if let icon {
                    Image(systemName: icon).regularIconWeight().font(.system(size: 18)).foregroundStyle(iconColor).frame(width: 24)
                }
                VStack(alignment: .leading, spacing: 1) {
                    Text(title).font(.system(size: 15.5)).foregroundStyle(titleColor)
                    if let subtitle { Text(subtitle).font(.caption2Muted).foregroundStyle(Theme.fg3) }
                }
                Spacer(minLength: 8)
                trailing()
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .contentShape(Rectangle())
            if showDivider { Divider().overlay(Theme.line).padding(.leading, 16) }
        }
    }
}

extension ListRow where Trailing == EmptyView {
    init(icon: String? = nil, iconColor: Color = Theme.fg2, title: String, subtitle: String? = nil,
         titleColor: Color = Theme.fg, showDivider: Bool = true) {
        self.init(icon: icon, iconColor: iconColor, title: title, subtitle: subtitle,
                  titleColor: titleColor, showDivider: showDivider, trailing: { EmptyView() })
    }
}

/// Relative time like the Mac panel: 11:02, Yesterday, Wed, Nov 9.
func shortTime(_ date: Date) -> String {
    let calendar = Calendar.current
    if calendar.isDateInToday(date) { return date.formatted(date: .omitted, time: .shortened) }
    if calendar.isDateInYesterday(date) { return "Yesterday" }
    if let days = calendar.dateComponents([.day], from: date, to: .now).day, days < 7 {
        return date.formatted(.dateTime.weekday(.abbreviated))
    }
    return date.formatted(.dateTime.month(.abbreviated).day())
}
