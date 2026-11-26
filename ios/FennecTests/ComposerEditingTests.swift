import SwiftUI
import XCTest
@testable import Fennec

@MainActor
final class ComposerEditingTests: XCTestCase {
    func testStaleBindingCannotReorderActiveInputButExplicitResetClearsIt() {
        let composer = ComposerTextView(text: .constant("Follow"), focused: .constant(true),
                                        placeholder: "Message", canSend: true, onSend: {})
        let coordinator = ComposerTextView.Coordinator(composer)
        let editor = ActiveEditor()
        editor.text = "Follow"
        editor.selectedRange = NSRange(location: 6, length: 0)
        coordinator.synchronizeText(editor, text: "Fo", resetRevision: 0)
        XCTAssertEqual(editor.text, "Follow")
        XCTAssertEqual(editor.selectedRange, NSRange(location: 6, length: 0))
        editor.insertText(" up")
        XCTAssertEqual(editor.text, "Follow up")
        coordinator.synchronizeText(editor, text: "", resetRevision: 1)
        XCTAssertEqual(editor.text, "")
        XCTAssertEqual(editor.selectedRange, NSRange(location: 0, length: 0))
        editor.text = "Next"
        editor.selectedRange = NSRange(location: 4, length: 0)
        coordinator.synchronizeText(editor, text: "", resetRevision: 1)
        XCTAssertEqual(editor.text, "Next")
    }

    func testReturnSendsOnlyWhenTheComposerSendsOnReturn() {
        var sends = 0
        let editor = UITextView()
        let chat = ComposerTextView.Coordinator(ComposerTextView(text: .constant("Hi"), focused: .constant(true),
                                                                 placeholder: "Message", canSend: true, onSend: { sends += 1 }))
        XCTAssertFalse(chat.textView(editor, shouldChangeTextIn: NSRange(location: 2, length: 0), replacementText: "\n"))
        XCTAssertEqual(sends, 1)
        let thread = ComposerTextView.Coordinator(ComposerTextView(text: .constant("Hi"), focused: .constant(true),
                                                                   placeholder: "Reply", canSend: true, onSend: { sends += 1 },
                                                                   sendsOnReturn: false, identifier: "thread-composer"))
        XCTAssertTrue(thread.textView(editor, shouldChangeTextIn: NSRange(location: 2, length: 0), replacementText: "\n"))
        XCTAssertEqual(sends, 1)
    }

    private final class ActiveEditor: UITextView {
        override var isFirstResponder: Bool { true }
    }
}
