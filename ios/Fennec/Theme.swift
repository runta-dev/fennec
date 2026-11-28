import SwiftUI
import UIKit

/// Design tokens from ios/DESIGN.html. Dark is the primary look; every color
/// has a light value so the app follows the system appearance.
enum Theme {
    static let bg = Color(light: 0xFFFFFF, dark: 0x0E0D0C)
    static let surface = Color(light: 0xF3F1EE, dark: 0x1B1918)
    static let surface2 = Color(light: 0xE8E5E1, dark: 0x262321)
    static let line = Color(light: 0xE6E2DD, dark: 0x2F2B28)
    static let fg = Color(light: 0x15120F, dark: 0xF4F1ED)
    static let fg2 = Color(light: 0x6C665F, dark: 0xA39D96)
    static let fg3 = Color(light: 0xA7A19A, dark: 0x6F6963)
    /// Runta orange: state, selection, switches. Never a button fill.
    static let accent = Color(hex: 0xF38D25)
    static let working = Color(light: 0x2F9A58, dark: 0x5FC081)
    static let danger = Color(light: 0xD6413A, dark: 0xEE5B51)
    /// Neutral primary: white in dark, black in light.
    static let primary = Color(light: 0x15120F, dark: 0xF4F1ED)
    static let onPrimary = Color(light: 0xFFFFFF, dark: 0x0E0D0C)
    static let bubble = Color(light: 0xEFECE8, dark: 0x2A2725)
}

extension Color {
    init(hex: UInt32) {
        self.init(.sRGB,
                  red: Double((hex >> 16) & 0xFF) / 255,
                  green: Double((hex >> 8) & 0xFF) / 255,
                  blue: Double(hex & 0xFF) / 255)
    }

    init(light: UInt32, dark: UInt32) {
        self.init(uiColor: UIColor { traits in
            let hex = traits.userInterfaceStyle == .dark ? dark : light
            return UIColor(red: CGFloat((hex >> 16) & 0xFF) / 255,
                           green: CGFloat((hex >> 8) & 0xFF) / 255,
                           blue: CGFloat(hex & 0xFF) / 255,
                           alpha: 1)
        })
    }
}

/// Weights follow the Fennec Mac panel: 400 body, 500 emphasis, 600 page titles.
extension Font {
    static let pageTitle = Font.system(size: 26, weight: .semibold)
    static let rowTitle = Font.system(size: 16, weight: .medium)
    static let rowBody = Font.system(size: 14.5)
    static let caption2Muted = Font.system(size: 12.5)
    static let groupHeader = Font.system(size: 13, weight: .medium)
}

/// Icons keep their regular weight when the system's Bold Text setting is on
/// (FEN-47): SF Symbols otherwise get much heavier, while text still follows
/// the setting. Applied to every icon the app draws itself. Without Bold Text
/// this is the default weight, so nothing changes. The navigation bar's back
/// chevron stays the system one on purpose: a replacement image is shrunk by
/// the bar for everyone.
extension View {
    func regularIconWeight() -> some View { environment(\.legibilityWeight, .regular) }
}

/// Thin 1pt Runta-orange outline used for focus and selection everywhere.
struct SelectionOutline: ViewModifier {
    var active: Bool
    var radius: CGFloat
    func body(content: Content) -> some View {
        content.overlay(
            RoundedRectangle(cornerRadius: radius, style: .continuous)
                .strokeBorder(active ? Theme.accent : Theme.line, lineWidth: 1)
        )
    }
}

extension View {
    func selectionOutline(_ active: Bool, radius: CGFloat = 14) -> some View {
        modifier(SelectionOutline(active: active, radius: radius))
    }
}

/// Primary call to action: neutral fill (white in dark, black in light).
struct PrimaryButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 16, weight: .medium))
            .frame(maxWidth: .infinity, minHeight: 50)
            .foregroundStyle(Theme.onPrimary)
            .background(Theme.primary, in: Capsule())
            .opacity(configuration.isPressed ? 0.8 : 1)
    }
}

/// Secondary action on the Liquid Glass control layer.
struct GlassCapsuleButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 16, weight: .medium))
            .frame(maxWidth: .infinity, minHeight: 50)
            .foregroundStyle(Theme.fg)
            .glassEffect(.regular.interactive(), in: Capsule())
            .opacity(configuration.isPressed ? 0.85 : 1)
    }
}

extension View {
    /// The page's Theme.bg, drawn under the keyboard too (FEN-236).
    /// `.background(Theme.bg)` stops at the keyboard's safe area, so the
    /// keyboard's rounded top corners showed the system black around them.
    func pageBackground() -> some View { background { Theme.bg.ignoresSafeArea() } }
}
