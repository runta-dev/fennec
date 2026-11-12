import AppKit

// FixtureMenu marks the time a key equivalent is being matched, so a menu
// command knows whether it ran from its shortcut or from the menu itself.
final class FixtureMenu: NSMenu {
    static var inKeyEquivalent = false
    override func performKeyEquivalent(with e: NSEvent) -> Bool {
        let was = FixtureMenu.inKeyEquivalent
        FixtureMenu.inKeyEquivalent = true
        defer { FixtureMenu.inKeyEquivalent = was }
        return super.performKeyEquivalent(with: e)
    }
}

final class MenuHandler: NSObject, NSMenuItemValidation {
    static let shared = MenuHandler()
    var rulerItem: NSMenuItem?
    var statusItem: NSMenuItem?
    var wrapItem: NSMenuItem?
    var untitled = 0

    // resetMarks leaves the toggle items' marks and titles as AppKit would
    // after launch: not yet validated, so the Show Ruler mark reads off while
    // the ruler is on until its menu opens.
    func resetMarks() {
        rulerItem?.state = .off
        statusItem?.state = .off
        wrapItem?.title = "Wrap to Page"
        untitled = 0
    }

    @objc func run(_ item: NSMenuItem) {
        let id = item.representedObject as? String ?? item.title
        let m = Model.shared
        m.commands.append(["name": id, "via": FixtureMenu.inKeyEquivalent ? "shortcut" : "menu"])
        switch id {
        case "toggle-status-bar":
            m.statusBar.toggle()
            Scenes.menusChanged()
        case "toggle-ruler":
            m.ruler.toggle()
            Scenes.menusChanged()
        case "wrap":
            m.wrapToPage.toggle()
            Scenes.menusChanged()
        case "new-window":
            untitled += 1
            let n = untitled
            makeWindow("untitled-\(n)", n == 1 ? "Untitled" : "Untitled \(n)",
                       textField("untitled-\(n)-field", "Body"), x: 120 + CGFloat(n) * 24, y: 120 + CGFloat(n) * 24)
        case "open":
            m.after(0.8) { Scenes.openDialog() }
        case "close-window":
            NSApp.keyWindow?.performClose(nil)
        case "save-as":
            m.after(0.6) { Scenes.showSavePanel(on: NSApp.mainWindow) }
        default:
            break
        }
    }

    func validateMenuItem(_ item: NSMenuItem) -> Bool {
        let m = Model.shared
        switch item.representedObject as? String {
        case "toggle-status-bar": item.state = m.statusBar ? .on : .off
        case "toggle-ruler": item.state = m.ruler ? .on : .off
        case "wrap": item.title = m.wrapToPage ? "Wrap to Window" : "Wrap to Page"
        case "export": return false
        default: break
        }
        return true
    }
}

func cmdItem(_ title: String, _ id: String, key: String = "", mods: NSEvent.ModifierFlags = .command) -> NSMenuItem {
    let i = NSMenuItem(title: title, action: #selector(MenuHandler.run(_:)), keyEquivalent: key)
    i.keyEquivalentModifierMask = mods
    i.target = MenuHandler.shared
    i.representedObject = id
    return i
}

func stdItem(_ title: String, _ sel: Selector, _ key: String, mods: NSEvent.ModifierFlags = .command) -> NSMenuItem {
    let i = NSMenuItem(title: title, action: sel, keyEquivalent: key)
    i.keyEquivalentModifierMask = mods
    return i
}

func submenu(_ title: String, _ items: [NSMenuItem]) -> NSMenuItem {
    let i = NSMenuItem(title: title, action: nil, keyEquivalent: "")
    let m = FixtureMenu(title: title)
    for it in items { m.addItem(it) }
    i.submenu = m
    return i
}

func buildMainMenu() {
    let main = FixtureMenu(title: "Main")
    let h = MenuHandler.shared

    main.addItem(submenu("CUAFixture", [
        cmdItem("About CUAFixture", "about"),
        .separator(),
        stdItem("Quit CUAFixture", #selector(NSApplication.terminate(_:)), "q"),
    ]))

    let f5 = String(Character(UnicodeScalar(NSF5FunctionKey)!))
    main.addItem(submenu("File", [
        cmdItem("New Window", "new-window", key: "n"),
        cmdItem("Open\u{2026}", "open", key: "o"),
        submenu("Open Recent", [cmdItem("Report.txt", "recent-report"), cmdItem("Notes.txt", "recent-notes")]),
        .separator(),
        cmdItem("Close Window", "close-window", key: "w"),
        cmdItem("Save As\u{2026}", "save-as", key: "s", mods: [.command, .shift]),
        cmdItem("Export\u{2026}", "export", key: "e", mods: [.command, .shift]),
        .separator(),
        cmdItem("Print Preview", "print-preview"),
        cmdItem("Print\u{2026}", "print", key: "p"),
    ]))

    main.addItem(submenu("Edit", [
        stdItem("Undo", Selector(("undo:")), "z"),
        stdItem("Redo", Selector(("redo:")), "z", mods: [.command, .shift]),
        .separator(),
        stdItem("Cut", #selector(NSText.cut(_:)), "x"),
        stdItem("Copy", #selector(NSText.copy(_:)), "c"),
        stdItem("Paste", #selector(NSText.paste(_:)), "v"),
        stdItem("Delete", #selector(NSText.delete(_:)), ""),
        stdItem("Select All", #selector(NSText.selectAll(_:)), "a"),
    ]))

    let status = cmdItem("Show Status Bar", "toggle-status-bar", key: "/")
    let ruler = cmdItem("Show Ruler", "toggle-ruler", key: "r", mods: [.command, .control])
    h.statusItem = status
    h.rulerItem = ruler
    main.addItem(submenu("View", [
        status,
        ruler,
        .separator(),
        cmdItem("Zoom In", "zoom-in", key: "+"),
        cmdItem("Zoom Out", "zoom-out", key: "-"),
        cmdItem("Actual Size", "actual-size", key: "0", mods: [.control, .option, .shift]),
        cmdItem("Reload", "reload", key: f5, mods: []),
        cmdItem("Forward Delete Line", "forward-delete-line", key: String(Character(UnicodeScalar(NSDeleteFunctionKey)!)), mods: [.command]),
    ]))

    let wrap = cmdItem("Wrap to Page", "wrap", key: "w", mods: [.command, .shift])
    h.wrapItem = wrap
    main.addItem(submenu("Format", [
        submenu("Font", [
            cmdItem("Bold", "bold", key: "b"),
            cmdItem("Italic", "italic", key: "i"),
            cmdItem("Underline", "underline", key: "u"),
        ]),
        submenu("Text", [
            submenu("Align", [
                cmdItem("Align Left", "align-left", key: "{"),
                cmdItem("Center", "align-center", key: "|"),
                cmdItem("Align Right", "align-right", key: "}"),
            ]),
            cmdItem("Uppercase", "uppercase"),
        ]),
        .separator(),
        wrap,
    ]))

    let window = submenu("Window", [
        stdItem("Minimize", #selector(NSWindow.performMiniaturize(_:)), "m"),
        stdItem("Bring All to Front", #selector(NSApplication.arrangeInFront(_:)), ""),
    ])
    main.addItem(window)
    NSApp.mainMenu = main
    NSApp.windowsMenu = window.submenu
}
