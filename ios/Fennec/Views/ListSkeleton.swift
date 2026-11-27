import SwiftUI

/// Placeholder rows shown while the first list loads. Same geometry as
/// RequestRow and GroupHeader, so nothing moves when the real rows arrive.
struct ListSkeleton: View {
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
        .accessibilityLabel(L10n.text("desktop.loading"))
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
