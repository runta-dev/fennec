import SwiftUI
import UIKit

/// A multiline composer with a Send key and Shift-Return for hardware
/// keyboards. IME composition never submits.
struct ComposerTextView: UIViewRepresentable {
    @Binding var text: String
    @Binding var focused: Bool
    var placeholder: String
    var canSend: Bool
    var onSend: () -> Void
    var onPasteImage: ((Data) -> Void)? = nil

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> ComposerInputView {
        let view = ComposerInputView()
        view.delegate = context.coordinator
        view.backgroundColor = .clear
        view.textContainerInset = UIEdgeInsets(top: 8, left: 0, bottom: 8, right: 0)
        view.textContainer.lineFragmentPadding = 0
        view.font = UIFont.preferredFont(forTextStyle: .body)
        view.adjustsFontForContentSizeCategory = true
        view.returnKeyType = .send
        view.enablesReturnKeyAutomatically = true
        view.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        view.accessibilityIdentifier = "chat-composer"
        context.coordinator.textView = view
        view.insertLineBreak = { [weak coordinator = context.coordinator] in coordinator?.insertNewline() }
        view.pasteImage = { [weak coordinator = context.coordinator] data in coordinator?.parent.onPasteImage?(data) }
        return view
    }

    func updateUIView(_ view: ComposerInputView, context: Context) {
        context.coordinator.parent = self
        context.coordinator.updatingView = true
        defer { context.coordinator.updatingView = false }
        if view.text != text, view.markedTextRange == nil || text.isEmpty {
            let selection = view.selectedRange
            if text.isEmpty { view.unmarkText() }
            view.text = text
            view.selectedRange = NSRange(location: min(selection.location, (text as NSString).length), length: 0)
            view.invalidateIntrinsicContentSize()
        }
        let foreground = UIColor(Theme.fg).resolvedColor(with: view.traitCollection)
        if view.textColor != foreground { view.textColor = foreground }
        if view.tintColor != foreground { view.tintColor = foreground }
        if view.accessibilityLabel != placeholder { view.accessibilityLabel = placeholder }
        if focused, !view.isFirstResponder { view.becomeFirstResponder() }
        if !focused, view.isFirstResponder { view.resignFirstResponder() }
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: ComposerInputView, context: Context) -> CGSize? {
        guard let width = proposal.width, width > 0 else { return nil }
        let lineHeight = uiView.font?.lineHeight ?? 20
        let maximum = lineHeight * 5 + 16
        // Measure independently of the live text view. UIKit text layout must
        // not invalidate the SwiftUI graph while it probes candidate widths.
        let content = (text + "\u{200B}") as NSString
        let bounds = content.boundingRect(with: CGSize(width: width, height: .greatestFiniteMagnitude),
                                          options: [.usesLineFragmentOrigin, .usesFontLeading],
                                          attributes: [.font: uiView.font ?? UIFont.preferredFont(forTextStyle: .body)],
                                          context: nil)
        return CGSize(width: width, height: min(maximum, max(lineHeight + 16, ceil(bounds.height) + 16)))
    }

    final class Coordinator: NSObject, UITextViewDelegate {
        var parent: ComposerTextView
        weak var textView: UITextView?
        private var insertingLineBreak = false
        var updatingView = false

        init(_ parent: ComposerTextView) { self.parent = parent }
        func textViewDidBeginEditing(_ textView: UITextView) {
            if !updatingView { parent.focused = true }
        }
        func textViewDidEndEditing(_ textView: UITextView) {
            if !updatingView { parent.focused = false }
        }
        func textViewDidChange(_ textView: UITextView) {
            guard !updatingView else { return }
            parent.text = textView.text
            textView.invalidateIntrinsicContentSize()
        }
        func textView(_ textView: UITextView, shouldChangeTextIn range: NSRange, replacementText text: String) -> Bool {
            if text == "\n", !insertingLineBreak, textView.markedTextRange == nil {
                parent.text = textView.text
                if parent.canSend { parent.onSend() }
                return false
            }
            return true
        }
        @objc func insertNewline() {
            guard let textView else { return }
            insertingLineBreak = true
            textView.insertText("\n")
            insertingLineBreak = false
            textViewDidChange(textView)
        }
    }
}

final class ComposerInputView: UITextView {
    var insertLineBreak: (() -> Void)?
    var pasteImage: ((Data) -> Void)?
    override func paste(_ sender: Any?) {
        // Read the clipboard only in response to the user's paste gesture.
        if let image = UIPasteboard.general.image, let data = image.pngData(), let pasteImage {
            pasteImage(data)
        } else { super.paste(sender) }
    }
    override func canPerformAction(_ action: Selector, withSender sender: Any?) -> Bool {
        if action == #selector(paste(_:)), pasteImage != nil, UIPasteboard.general.hasImages { return true }
        return super.canPerformAction(action, withSender: sender)
    }
    override var keyCommands: [UIKeyCommand]? {
        let newline = UIKeyCommand(input: "\r", modifierFlags: .shift, action: #selector(addLineBreak))
        newline.wantsPriorityOverSystemBehavior = true
        return (super.keyCommands ?? []) + [newline]
    }
    @objc private func addLineBreak() { insertLineBreak?() }
}
