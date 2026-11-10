import AppKit

@main
struct PopupPlacementTests {
    static func main() {
        let screen = NSRect(x: 0, y: 0, width: 1920, height: 1080)
        let visible = NSRect(x: 0, y: 24, width: 1920, height: 1032)
        let size = NSSize(width: 440, height: 390)
        precondition(PopupPlacement.origin(anchor: .zero, screen: screen, visible: visible, size: size, menuBarHeight: 24) == nil)
        precondition(PopupPlacement.origin(anchor: NSRect(x: 0, y: 0, width: 24, height: 24), screen: screen, visible: visible, size: size, menuBarHeight: 24) == nil)
        let anchor = NSRect(x: 1800, y: 1056, width: 24, height: 24)
        let origin = PopupPlacement.origin(anchor: anchor, screen: screen, visible: visible, size: size, menuBarHeight: 24)!
        precondition(origin.y + size.height == visible.maxY - 8)
        precondition(origin.x + size.width <= visible.maxX - 8)
        let secondary = NSRect(x: -1920, y: -1080, width: 1920, height: 1080)
        let secondaryVisible = NSRect(x: -1920, y: -1080, width: 1920, height: 1056)
        let secondaryAnchor = NSRect(x: -200, y: -24, width: 24, height: 24)
        let second = PopupPlacement.origin(anchor: secondaryAnchor, screen: secondary, visible: secondaryVisible, size: size, menuBarHeight: 24)!
        precondition(second.y + size.height == -32)
        precondition(PopupPlacement.origin(anchor: anchor, screen: secondary, visible: secondaryVisible, size: size, menuBarHeight: 24) == nil)
        print("PASS uninitialized anchors never produce a popup origin; valid displays retain menu gap and edge clamping")
    }
}
