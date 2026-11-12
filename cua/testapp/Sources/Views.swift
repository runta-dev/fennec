import AppKit

func drawText(_ s: String, at p: NSPoint, size: CGFloat = 13, color: NSColor = .labelColor) {
    (s as NSString).draw(at: p, withAttributes: [.font: NSFont.systemFont(ofSize: size), .foregroundColor: color])
}

// ClickPad records every mouse button press with its button, click count,
// modifiers and point (view points, top-left origin), and every scroll.
// It is drawn as a grid of labelled cells (A1, B1, ...) so a screenshot shows
// where a click lands.
final class ClickPad: FlippedView {
    let name: String
    let cols = 5, rows = 4
    var sink: ([String: Any]) -> Void
    var scrollSink: ([String: Any]) -> Void

    init(name: String, id: String, size: NSSize, sink: (([String: Any]) -> Void)? = nil) {
        self.name = name
        self.sink = sink ?? { Model.shared.log("clicks", $0) }
        self.scrollSink = { Model.shared.log("scrolls", $0) }
        super.init(frame: NSRect(origin: .zero, size: size))
        markID(self, id)
        _ = fixed(self, size.width, size.height)
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        setAccessibilityLabel(name)
    }

    required init?(coder: NSCoder) { fatalError() }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    func cell(_ p: NSPoint) -> String {
        let c = min(max(Int(p.x / (bounds.width / CGFloat(cols))), 0), cols - 1)
        let r = min(max(Int(p.y / (bounds.height / CGFloat(rows))), 0), rows - 1)
        return "\(Character(UnicodeScalar(65 + c)!))\(r + 1)"
    }

    override func draw(_ dirty: NSRect) {
        NSColor(calibratedWhite: 0.93, alpha: 1).setFill()
        bounds.fill()
        NSColor.gray.setStroke()
        let cw = bounds.width / CGFloat(cols), rh = bounds.height / CGFloat(rows)
        for c in 0..<cols {
            for r in 0..<rows {
                let rect = NSRect(x: CGFloat(c) * cw, y: CGFloat(r) * rh, width: cw, height: rh)
                NSBezierPath(rect: rect).stroke()
                drawText(cell(NSPoint(x: rect.midX, y: rect.midY)), at: NSPoint(x: rect.minX + 6, y: rect.minY + 4), color: .darkGray)
            }
        }
        drawText(name, at: NSPoint(x: 8, y: bounds.height - 20), size: 11, color: .gray)
    }

    func record(_ e: NSEvent, _ button: String) {
        let p = convert(e.locationInWindow, from: nil)
        sink(["pad": name, "button": button, "count": e.clickCount, "modifiers": modifierNames(e.modifierFlags),
              "x": Int(p.x.rounded(.down)), "y": Int(p.y.rounded(.down)), "cell": cell(p)])
    }

    override func mouseDown(with e: NSEvent) { record(e, "left") }
    override func rightMouseDown(with e: NSEvent) { record(e, "right") }
    override func otherMouseDown(with e: NSEvent) { record(e, "other") }
    override func scrollWheel(with e: NSEvent) {
        let p = convert(e.locationInWindow, from: nil)
        scrollSink(["pad": name, "dx": Double(e.scrollingDeltaX), "dy": Double(e.scrollingDeltaY), "cell": cell(p)])
    }
}

// KeyCatcher records raw key events while it is the first responder.
final class KeyCatcher: FlippedView {
    var lastTimestamp: TimeInterval = -1

    init(id: String, size: NSSize) {
        super.init(frame: NSRect(origin: .zero, size: size))
        markID(self, id)
        _ = fixed(self, size.width, size.height)
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        setAccessibilityLabel("Key Catcher")
    }

    required init?(coder: NSCoder) { fatalError() }

    override var acceptsFirstResponder: Bool { true }
    override func becomeFirstResponder() -> Bool { needsDisplay = true; return true }
    override func resignFirstResponder() -> Bool { needsDisplay = true; return true }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
    override func mouseDown(with event: NSEvent) { window?.makeFirstResponder(self) }

    override func draw(_ dirty: NSRect) {
        NSColor(calibratedRed: 0.95, green: 0.95, blue: 0.85, alpha: 1).setFill()
        bounds.fill()
        (window?.firstResponder === self ? NSColor.systemBlue : NSColor.gray).setStroke()
        let p = NSBezierPath(rect: bounds.insetBy(dx: 1, dy: 1))
        p.lineWidth = 2
        p.stroke()
        drawText("Key Catcher (click, then press keys)", at: NSPoint(x: 8, y: 8), color: .darkGray)
    }

    func record(_ e: NSEvent, equivalent: Bool) {
        if e.timestamp == lastTimestamp { return }
        lastTimestamp = e.timestamp
        Model.shared.log("keys", ["key_code": Int(e.keyCode), "chars": e.characters ?? "",
                                  "chars_ignoring": e.charactersIgnoringModifiers ?? "",
                                  "modifiers": modifierNames(e.modifierFlags), "repeat": e.isARepeat,
                                  "equivalent": equivalent])
    }

    override func keyDown(with e: NSEvent) { record(e, equivalent: false) }

    override func performKeyEquivalent(with e: NSEvent) -> Bool {
        if window?.firstResponder === self && e.type == .keyDown { record(e, equivalent: true) }
        return false
    }
}

// CanvasView draws a button and a text input with no accessibility elements
// at all. Clicks inside the drawn button count as presses; a click in the
// drawn input focuses it and later keys are appended to its text.
final class CanvasView: FlippedView {
    let buttonRect = NSRect(x: 30, y: 30, width: 140, height: 44)
    let inputRect = NSRect(x: 30, y: 110, width: 260, height: 32)
    var text = ""
    var inputFocused = false
    var presses = 0
    var misses = 0

    init(id: String) {
        super.init(frame: NSRect(x: 0, y: 0, width: 340, height: 180))
        identifier = NSUserInterfaceItemIdentifier(id)
        _ = fixed(self, 340, 180)
    }

    required init?(coder: NSCoder) { fatalError() }

    override func isAccessibilityElement() -> Bool { false }
    override func accessibilityChildren() -> [Any]? { [] }
    override var acceptsFirstResponder: Bool { true }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
    override func resignFirstResponder() -> Bool { inputFocused = false; needsDisplay = true; return true }

    override func draw(_ dirty: NSRect) {
        NSColor.white.setFill()
        bounds.fill()
        NSColor.systemOrange.setFill()
        NSBezierPath(roundedRect: buttonRect, xRadius: 8, yRadius: 8).fill()
        drawText("Draw Me", at: NSPoint(x: buttonRect.minX + 38, y: buttonRect.minY + 12), size: 15, color: .white)
        NSColor(calibratedWhite: 0.97, alpha: 1).setFill()
        inputRect.fill()
        (inputFocused ? NSColor.systemBlue : NSColor.gray).setStroke()
        NSBezierPath(rect: inputRect).stroke()
        drawText(text.isEmpty && !inputFocused ? "Type Here" : text + (inputFocused ? "|" : ""),
                 at: NSPoint(x: inputRect.minX + 6, y: inputRect.minY + 7),
                 color: text.isEmpty && !inputFocused ? .gray : .black)
    }

    override func mouseDown(with e: NSEvent) {
        let p = convert(e.locationInWindow, from: nil)
        if buttonRect.contains(p) {
            presses += 1
        } else if inputRect.contains(p) {
            inputFocused = true
            window?.makeFirstResponder(self)
        } else {
            misses += 1
        }
        needsDisplay = true
    }

    override func keyDown(with e: NSEvent) {
        guard inputFocused else { return }
        if e.keyCode == 51 {
            if !text.isEmpty { text.removeLast() }
        } else if let c = e.characters, !c.isEmpty, e.modifierFlags.intersection([.command, .control]).isEmpty,
                  c.unicodeScalars.allSatisfy({ $0.value >= 0x20 && !(0xF700...0xF8FF).contains($0.value) }) {
            text += c
        }
        needsDisplay = true
    }
}

// LogoTile is an accessible image with a name but no press action: an AX
// press is unsupported and only a real click counts.
final class LogoTile: NSView {
    var clicks = 0

    init(id: String) {
        super.init(frame: NSRect(x: 0, y: 0, width: 90, height: 60))
        markID(self, id)
        _ = fixed(self, 90, 60)
        setAccessibilityElement(true)
        setAccessibilityRole(.image)
        setAccessibilityLabel("Logo Tile")
    }

    required init?(coder: NSCoder) { fatalError() }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
    override func draw(_ dirty: NSRect) {
        NSColor.systemTeal.setFill()
        NSBezierPath(ovalIn: bounds.insetBy(dx: 4, dy: 4)).fill()
        drawText("LOGO", at: NSPoint(x: 26, y: 22), color: .white)
    }
    override func mouseDown(with event: NSEvent) { clicks += 1 }
}

// StubbornButton refuses its accessibility press; a real click still works.
final class StubbornButton: NSButton {
    var refused = 0
    override func accessibilityPerformPress() -> Bool {
        refused += 1
        return false
    }
}

// ColorCycler changes colour on a timer: a region that never settles.
final class ColorCycler: NSView {
    var phase = 0
    let colors: [NSColor] = [.systemRed, .systemGreen, .systemBlue, .systemYellow]
    override func draw(_ dirty: NSRect) {
        colors[phase % colors.count].setFill()
        bounds.fill()
    }
}

// CountingScrollView counts scroll-wheel events it receives.
final class CountingScrollView: NSScrollView {
    var name = ""
    override func scrollWheel(with e: NSEvent) {
        Model.shared.log("scrolls", ["pad": name, "dx": Double(e.scrollingDeltaX), "dy": Double(e.scrollingDeltaY), "cell": ""])
        super.scrollWheel(with: e)
    }
}

// DragPad holds a draggable token and a drop zone, both exposed to
// accessibility as child elements with frames.
final class DragPad: FlippedView {
    var token = NSRect(x: 30, y: 90, width: 60, height: 60)
    let zone = NSRect(x: 300, y: 60, width: 150, height: 120)
    var dragStart: NSPoint?
    var grabOffset = NSPoint.zero
    let tokenEl = NSAccessibilityElement()
    let zoneEl = NSAccessibilityElement()

    init(id: String) {
        super.init(frame: NSRect(x: 0, y: 0, width: 480, height: 240))
        markID(self, id)
        _ = fixed(self, 480, 240)
        setAccessibilityElement(true)
        setAccessibilityRole(.group)
        setAccessibilityLabel("Drag Pad")
        tokenEl.setAccessibilityRole(.button)
        tokenEl.setAccessibilityLabel("Token")
        tokenEl.setAccessibilityParent(self)
        zoneEl.setAccessibilityRole(.group)
        zoneEl.setAccessibilityLabel("Drop Zone")
        zoneEl.setAccessibilityParent(self)
        updateAX()
    }

    required init?(coder: NSCoder) { fatalError() }

    // Parent-space frames use the parent's unflipped coordinates.
    func unflip(_ r: NSRect) -> NSRect { NSRect(x: r.minX, y: bounds.height - r.maxY, width: r.width, height: r.height) }
    func updateAX() {
        tokenEl.setAccessibilityFrameInParentSpace(unflip(token))
        zoneEl.setAccessibilityFrameInParentSpace(unflip(zone))
    }
    override func accessibilityChildren() -> [Any]? { [zoneEl, tokenEl] }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    var inZone: Bool { zone.contains(NSPoint(x: token.midX, y: token.midY)) }

    override func draw(_ dirty: NSRect) {
        NSColor(calibratedWhite: 0.95, alpha: 1).setFill()
        bounds.fill()
        NSColor.systemGreen.withAlphaComponent(0.3).setFill()
        zone.fill()
        drawText("Drop Zone", at: NSPoint(x: zone.minX + 40, y: zone.minY + 50))
        NSColor.systemPurple.setFill()
        NSBezierPath(roundedRect: token, xRadius: 6, yRadius: 6).fill()
        drawText("Token", at: NSPoint(x: token.minX + 10, y: token.minY + 22), color: .white)
    }

    override func mouseDown(with e: NSEvent) {
        let p = convert(e.locationInWindow, from: nil)
        if token.contains(p) {
            dragStart = p
            grabOffset = NSPoint(x: p.x - token.minX, y: p.y - token.minY)
        }
    }

    override func mouseDragged(with e: NSEvent) {
        guard dragStart != nil else { return }
        let p = convert(e.locationInWindow, from: nil)
        token.origin = NSPoint(x: p.x - grabOffset.x, y: p.y - grabOffset.y)
        needsDisplay = true
    }

    override func mouseUp(with e: NSEvent) {
        guard let s = dragStart else { return }
        let p = convert(e.locationInWindow, from: nil)
        dragStart = nil
        updateAX()
        Model.shared.log("drags", ["from": [Int(s.x), Int(s.y)], "to": [Int(p.x), Int(p.y)], "in_zone": inZone])
    }
}
