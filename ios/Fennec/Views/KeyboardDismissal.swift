import SwiftUI
import UIKit
import SwiftTerm

/// Tapping anywhere outside a text input puts the keyboard away (FEN-62).
///
/// One tap recognizer sits on the app's window. It never cancels or delays
/// touches and recognizes alongside every other gesture, so buttons, rows
/// and menus still get the same tap; the keyboard just goes away with it.
/// Taps on a text input (text view, text field, the terminal) keep the
/// keyboard, and so do taps inside a `.keepsKeyboard()` area such as a
/// composer bar, so Send, attach and image chips don't close it mid-message.
extension View {
    /// Install once at the root of the window.
    func dismissesKeyboardOnOutsideTap() -> some View {
        background(KeyboardDismissInstaller().frame(width: 0, height: 0).accessibilityHidden(true))
    }

    /// Taps inside this view don't dismiss the keyboard.
    func keepsKeyboard() -> some View {
        background(KeyboardKeepArea().accessibilityHidden(true))
    }
}

private struct KeyboardDismissInstaller: UIViewRepresentable {
    func makeUIView(context: Context) -> InstallerView { InstallerView() }
    func updateUIView(_ view: InstallerView, context: Context) {}

    final class InstallerView: UIView {
        override func didMoveToWindow() {
            super.didMoveToWindow()
            isUserInteractionEnabled = false
            guard let window, !(window.gestureRecognizers ?? []).contains(where: { $0 is KeyboardDismissTap }) else { return }
            window.addGestureRecognizer(KeyboardDismissTap())
        }
    }
}

private struct KeyboardKeepArea: UIViewRepresentable {
    func makeUIView(context: Context) -> KeepAreaView {
        let view = KeepAreaView()
        view.isUserInteractionEnabled = false
        view.backgroundColor = .clear
        KeepAreaView.all.add(view)
        return view
    }
    func updateUIView(_ view: KeepAreaView, context: Context) {}

    final class KeepAreaView: UIView {
        static let all = NSHashTable<KeepAreaView>.weakObjects()
    }
}

final class KeyboardDismissTap: UITapGestureRecognizer, UIGestureRecognizerDelegate {
    init() {
        super.init(target: nil, action: nil)
        addTarget(self, action: #selector(tapped))
        cancelsTouchesInView = false
        delaysTouchesBegan = false
        delaysTouchesEnded = false
        delegate = self
    }

    @objc private func tapped() {
        guard state == .ended else { return }
        view?.endEditing(true)
    }

    func gestureRecognizer(_ recognizer: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
        guard let window = view as? UIWindow else { return false }
        // Tapping a text input moves the cursor or focuses it; keep the keyboard.
        // Only real editors count: SwiftUI's hosting views adopt UIKeyInput
        // themselves, so checking for that protocol would keep every tap.
        var hit = touch.view
        while let current = hit {
            if current is UITextField || (current as? UITextView)?.isEditable == true || current is TerminalView { return false }
            hit = current.superview
        }
        let point = touch.location(in: window)
        for area in KeyboardKeepArea.KeepAreaView.all.allObjects where area.window === window && !area.isHidden {
            if area.bounds.contains(area.convert(point, from: window)) { return false }
        }
        return true
    }

    func gestureRecognizer(_ recognizer: UIGestureRecognizer,
                           shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool { true }
}
