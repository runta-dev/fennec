import AppKit

enum PopupPlacement {
    static func origin(anchor: NSRect, screen: NSRect, visible: NSRect, size: NSSize, menuBarHeight: CGFloat) -> NSPoint? {
        let values = [anchor.minX, anchor.minY, anchor.width, anchor.height, screen.minX, screen.minY, screen.width, screen.height, visible.minX, visible.minY, visible.width, visible.height, size.width, size.height, menuBarHeight]
        guard values.allSatisfy({ $0.isFinite }), !anchor.isEmpty, !screen.isEmpty, !visible.isEmpty,
              size.width > 0, size.height > 0, anchor.intersects(screen),
              anchor.midY >= screen.maxY - max(menuBarHeight, screen.maxY - visible.maxY) - 4,
              anchor.maxY <= screen.maxY + 1 else { return nil }
        let x = min(max(anchor.midX - size.width / 2, visible.minX + 8), visible.maxX - size.width - 8)
        let top = min(anchor.minY, visible.maxY) - 8
        return NSPoint(x: x, y: max(visible.minY + 8, top - size.height))
    }
}
