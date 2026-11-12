import AppKit

final class FixtureWindow: NSWindow {
    override var canBecomeKey: Bool { true }
    override var canBecomeMain: Bool { true }
}

// Action adapts a closure to target/action.
final class Action: NSObject {
    let f: () -> Void
    init(_ f: @escaping () -> Void) { self.f = f }
    @objc func run(_ sender: Any?) { f() }
}

func bind(_ c: NSControl, _ f: @escaping () -> Void) {
    let a = Action(f)
    Model.shared.retained.append(a)
    c.target = a
    c.action = #selector(Action.run(_:))
}

func markID(_ v: NSView, _ id: String) {
    v.identifier = NSUserInterfaceItemIdentifier(id)
    v.setAccessibilityIdentifier(id)
}

// FieldWatch counts edits per text field and runs per-field change hooks.
final class FieldWatch: NSObject, NSTextFieldDelegate, NSTextViewDelegate, NSComboBoxDelegate {
    static let shared = FieldWatch()
    var onChange: [String: () -> Void] = [:]

    func controlTextDidChange(_ n: Notification) {
        guard let v = n.object as? NSView, let id = v.identifier?.rawValue else { return }
        Model.shared.edits[id, default: 0] += 1
        onChange[id]?()
    }

    func textDidChange(_ n: Notification) {
        guard let v = n.object as? NSView, let id = v.identifier?.rawValue else { return }
        Model.shared.edits[id, default: 0] += 1
        onChange[id]?()
    }
}

func button(_ title: String, _ id: String, _ f: @escaping () -> Void = {}) -> NSButton {
    let b = NSButton(title: title, target: nil, action: nil)
    b.bezelStyle = .rounded
    markID(b, id)
    bind(b) {
        Model.shared.press(id)
        f()
    }
    return b
}

func label(_ s: String) -> NSTextField {
    let l = NSTextField(labelWithString: s)
    return l
}

func textField(_ id: String, _ name: String, value: String = "", secure: Bool = false, width: CGFloat = 240) -> NSTextField {
    let f: NSTextField = secure ? NSSecureTextField() : NSTextField()
    f.stringValue = value
    if !name.isEmpty { f.setAccessibilityLabel(name) }
    markID(f, id)
    f.delegate = FieldWatch.shared
    f.translatesAutoresizingMaskIntoConstraints = false
    f.widthAnchor.constraint(equalToConstant: width).isActive = true
    Model.shared.reg(id) { [weak f] in orNull(f?.stringValue) }
    return f
}

func checkbox(_ title: String, _ id: String, state: NSControl.StateValue = .off, mixed: Bool = false) -> NSButton {
    let b = NSButton(checkboxWithTitle: title, target: nil, action: nil)
    b.allowsMixedState = mixed
    b.state = state
    markID(b, id)
    bind(b) { Model.shared.press(id) }
    Model.shared.reg(id) { [weak b] in orNull(b.map { checkState($0.state) }) }
    return b
}

// textArea is a multiline NSTextView in a scroll view.
func textArea(_ id: String, _ name: String, value: String = "", size: NSSize = NSSize(width: 320, height: 90), editable: Bool = true) -> (NSScrollView, NSTextView) {
    let sv = NSTextView.scrollableTextView()
    let tv = sv.documentView as! NSTextView
    tv.string = value
    tv.isEditable = editable
    tv.isRichText = false
    tv.isAutomaticQuoteSubstitutionEnabled = false
    tv.isAutomaticDashSubstitutionEnabled = false
    tv.isAutomaticTextReplacementEnabled = false
    tv.isAutomaticSpellingCorrectionEnabled = false
    tv.isContinuousSpellCheckingEnabled = false
    tv.setAccessibilityLabel(name)
    markID(tv, id)
    tv.delegate = FieldWatch.shared
    sv.borderType = .bezelBorder
    sv.translatesAutoresizingMaskIntoConstraints = false
    sv.widthAnchor.constraint(equalToConstant: size.width).isActive = true
    sv.heightAnchor.constraint(equalToConstant: size.height).isActive = true
    Model.shared.reg(id) { [weak tv] in orNull(tv?.string) }
    return (sv, tv)
}

func hrow(_ views: [NSView], spacing: CGFloat = 8) -> NSStackView {
    let s = NSStackView(views: views)
    s.orientation = .horizontal
    s.alignment = .centerY
    s.spacing = spacing
    return s
}

func labeled(_ name: String, _ v: NSView) -> NSStackView {
    let l = label(name)
    l.translatesAutoresizingMaskIntoConstraints = false
    l.widthAnchor.constraint(equalToConstant: 110).isActive = true
    return hrow([l, v])
}

func vstack(_ views: [NSView], spacing: CGFloat = 10) -> NSStackView {
    let s = NSStackView(views: views)
    s.orientation = .vertical
    s.alignment = .leading
    s.spacing = spacing
    return s
}

func fixed(_ v: NSView, _ w: CGFloat, _ h: CGFloat) -> NSView {
    v.translatesAutoresizingMaskIntoConstraints = false
    v.widthAnchor.constraint(equalToConstant: w).isActive = true
    v.heightAnchor.constraint(equalToConstant: h).isActive = true
    return v
}

// makeWindow builds a titled window around content, sized to fit, with its
// top-left corner at (x, y) points from the top-left of the main display's
// visible area.
@discardableResult
func makeWindow(_ id: String, _ title: String, _ content: NSView, x: CGFloat, y: CGFloat, show: Bool = true, screen: NSScreen? = nil) -> NSWindow {
    let w = FixtureWindow(contentRect: NSRect(x: 0, y: 0, width: 200, height: 100),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable],
                          backing: .buffered, defer: false)
    w.isReleasedWhenClosed = false
    w.title = title
    w.identifier = NSUserInterfaceItemIdentifier(id)
    w.setAccessibilityIdentifier(id)
    w.tabbingMode = .disallowed
    let container = NSView()
    content.translatesAutoresizingMaskIntoConstraints = false
    container.addSubview(content)
    NSLayoutConstraint.activate([
        content.leadingAnchor.constraint(equalTo: container.leadingAnchor, constant: 20),
        content.trailingAnchor.constraint(equalTo: container.trailingAnchor, constant: -20),
        content.topAnchor.constraint(equalTo: container.topAnchor, constant: 20),
        content.bottomAnchor.constraint(equalTo: container.bottomAnchor, constant: -20),
    ])
    w.contentView = container
    container.layoutSubtreeIfNeeded()
    let size = container.fittingSize
    w.setContentSize(NSSize(width: max(size.width, 260), height: max(size.height, 80)))
    let vf = (screen ?? NSScreen.main ?? NSScreen.screens[0]).visibleFrame
    w.setFrameTopLeftPoint(NSPoint(x: vf.minX + x, y: vf.maxY - y))
    if show { w.makeKeyAndOrderFront(nil) }
    return w
}

// FlippedView puts the origin at the top left, like the state's coordinates.
class FlippedView: NSView {
    override var isFlipped: Bool { true }
}
