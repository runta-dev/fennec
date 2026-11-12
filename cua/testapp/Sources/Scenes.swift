import AppKit

// Scenes lists every scene in a stable order; the control socket's "scenes"
// op answers these names.
let sceneTable: [(name: String, build: () -> Void)] = [
    ("basic", Scenes.basic),
    ("form", { Scenes.form(filled: false) }),
    ("form-filled", { Scenes.form(filled: true) }),
    ("login", Scenes.login),
    ("dialogs", Scenes.dialogs),
    ("hidden-modal", Scenes.hiddenModal),
    ("menus", Scenes.menus),
    ("names", Scenes.names),
    ("tabs", Scenes.tabs),
    ("lists", Scenes.lists),
    ("live-list", Scenes.liveList),
    ("slow", Scenes.slow),
    ("windows", Scenes.windows),
    ("twins", Scenes.twins),
    ("unnamed", Scenes.unnamed),
    ("canvas", Scenes.canvas),
    ("animated", Scenes.animated),
    ("link", Scenes.link),
    ("scroll", Scenes.scroll),
    ("big-tree", Scenes.bigTree),
    ("drag", Scenes.drag),
    ("click-pad", Scenes.clickPad),
    ("keys", Scenes.keys),
    ("busy", Scenes.busy),
    ("web", Scenes.web),
    ("spaces", Scenes.spaces),
    ("cover", Scenes.cover),
    ("displays", Scenes.displays),
    ("launch", Scenes.launch),
    ("empty", {}),
]

let sceneNames = sceneTable.map { $0.name }

func teardownScene() {
    let m = Model.shared
    m.gen += 1
    if NSApp.modalWindow != nil { NSApp.abortModal() }
    for t in m.timers { t.invalidate() }
    for f in m.teardowns { f() }
    for w in NSApp.windows where w.sheetParent != nil {
        w.sheetParent?.endSheet(w)
        w.orderOut(nil)
    }
    for w in NSApp.windows where isFixtureWindow(w) {
        w.orderOut(nil)
        w.close()
    }
    FieldWatch.shared.onChange = [:]
    m.clear()
}

// resetScene rebuilds the windows to a scene's initial state and returns an
// error message, or nil.
func resetScene(_ name: String) -> String? {
    guard let s = sceneTable.first(where: { $0.name == name }) else { return "unknown scene \(name)" }
    let resets = Model.shared.resets
    teardownScene()
    MenuHandler.shared.resetMarks()
    let m = Model.shared
    m.scene = name
    m.resets = resets + 1
    s.build()
    NSApp.activate(ignoringOtherApps: true)
    return nil
}

enum Scenes {
    static var saveDir: URL = {
        let u = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("cua-fixture-save-\(getpid())")
        try? FileManager.default.createDirectory(at: u, withIntermediateDirectories: true)
        return u
    }()

    // MARK: basic

    static func basic() {
        let m = Model.shared
        var count = 0
        let value = label("Count: 0")
        markID(value, "count-label")
        m.reg("count") { count }
        let inc = button("Increment", "increment") {
            count += 1
            value.stringValue = "Count: \(count)"
        }
        let reset = button("Reset Count", "reset-count") {
            count = 0
            value.stringValue = "Count: 0"
        }
        let disabled = button("Disabled Action", "disabled-action")
        disabled.isEnabled = false
        let note = textField("basic-note", "Note")
        // With Front On Edit on, the app brings itself to the front whenever
        // the note changes, like an app that steals focus mid-input.
        let front = checkbox("Front On Edit", "front-on-edit")
        var w: NSWindow!
        FieldWatch.shared.onChange["basic-note"] = {
            guard front.state == .on else { return }
            m.facts["fronted_on_edit"] = (m.facts["fronted_on_edit"] as? Int ?? 0) + 1
            NSApp.activate(ignoringOtherApps: true)
            w.makeKeyAndOrderFront(nil)
        }
        w = makeWindow("basic", "Basic", vstack([value, hrow([inc, reset, disabled]), labeled("Note", note), front]), x: 80, y: 60)
    }

    // MARK: form

    static func form(filled: Bool) {
        let m = Model.shared
        let name = textField("name", "Name", value: filled ? "Existing Name" : "")
        let email = textField("email", "Email:")
        let password = textField("password", "Password", secure: true)
        let subscribe = checkbox("Subscribe", "subscribe")
        let notify = checkbox("Notifications", "notifications", state: .mixed, mixed: true)
        let sw = NSSwitch()
        sw.setAccessibilityLabel("Dark Mode")
        markID(sw, "dark-mode")
        bind(sw) { m.press("dark-mode") }
        m.reg("dark-mode") { [weak sw] in orNull(sw.map { checkState($0.state) }) }

        var radios: [NSButton] = []
        for size in ["Small", "Medium", "Large"] {
            let r = NSButton(radioButtonWithTitle: size, target: nil, action: nil)
            markID(r, "size-\(size.lowercased())")
            r.state = size == "Medium" ? .on : .off
            bind(r) {
                m.press("size-\(size.lowercased())")
                for o in radios where o !== r { o.state = .off }
            }
            radios.append(r)
        }
        let radioGroup = hrow(radios)
        radioGroup.setAccessibilityElement(true)
        radioGroup.setAccessibilityRole(.radioGroup)
        radioGroup.setAccessibilityLabel("Size")
        m.reg("size") { orNull(radios.first(where: { $0.state == .on })?.title) }

        let color = NSPopUpButton(frame: .zero, pullsDown: false)
        color.addItems(withTitles: ["Red", "Green", "Blue"])
        color.setAccessibilityLabel("Color")
        markID(color, "color")
        bind(color) { m.press("color") }
        m.reg("color") { [weak color] in orNull(color?.titleOfSelectedItem) }

        let city = NSComboBox()
        city.addItems(withObjectValues: ["Berlin", "Paris", "Tokyo"])
        city.setAccessibilityLabel("City")
        city.completes = false
        markID(city, "city")
        city.delegate = FieldWatch.shared
        _ = fixed(city, 200, 26)
        m.reg("city") { [weak city] in orNull(city?.stringValue) }

        // Font Size: a text field that shows its number with a "pt" unit, kept
        // in step with a stepper.
        let fontField = textField("font-size-text", "Font Size", value: "12 pt", width: 80)
        let stepper = NSStepper()
        stepper.minValue = 6
        stepper.maxValue = 72
        stepper.increment = 1
        stepper.integerValue = 12
        stepper.valueWraps = false
        stepper.setAccessibilityLabel("Font Size Stepper")
        markID(stepper, "font-size-stepper")
        bind(stepper) {
            m.press("font-size-stepper")
            fontField.stringValue = "\(stepper.integerValue) pt"
        }
        bind(fontField) {
            let digits = fontField.stringValue.trimmingCharacters(in: .whitespaces).prefix { $0.isNumber }
            if let n = Int(digits), n >= 6, n <= 72 { stepper.integerValue = n }
            fontField.stringValue = "\(stepper.integerValue) pt"
        }
        m.reg("font-size") { [weak stepper] in orNull(stepper?.integerValue) }

        let volume = NSSlider(value: 50, minValue: 0, maxValue: 100, target: nil, action: nil)
        volume.setAccessibilityLabel("Volume")
        markID(volume, "volume")
        _ = fixed(volume, 200, 24)
        m.reg("volume") { [weak volume] in orNull(volume.map { Int($0.doubleValue.rounded()) }) }

        let (notesView, _) = textArea("notes", "Notes", value: filled ? "Existing notes" : "")
        // Shout rewrites whatever lands in it to upper case, like autocorrect.
        let shout = textField("shout", "Shout")
        FieldWatch.shared.onChange["shout"] = {
            let up = shout.stringValue.uppercased()
            if up != shout.stringValue { shout.stringValue = up }
        }

        m.facts["submitted"] = NSNull()
        let submit = button("Submit", "submit") {
            var snap: [String: Any] = [:]
            for k in ["name", "email", "password", "subscribe", "notifications", "dark-mode", "size", "color", "city", "font-size", "volume", "notes"] {
                snap[k] = orNull(m.readers[k]?())
            }
            m.facts["submitted"] = snap
        }
        submit.keyEquivalent = "\r"
        let clear = button("Clear", "clear") {
            name.stringValue = ""
            email.stringValue = ""
            password.stringValue = ""
        }

        makeWindow("form", "Form", vstack([
            labeled("Name", name),
            labeled("Email:", email),
            labeled("Password", password),
            hrow([subscribe, notify]),
            labeled("Dark Mode", sw),
            labeled("Size", radioGroup),
            labeled("Color", color),
            labeled("City", city),
            labeled("Font Size", hrow([fontField, stepper])),
            labeled("Volume", volume),
            labeled("Notes", notesView),
            labeled("Shout", shout),
            hrow([submit, clear]),
        ]), x: 80, y: 40)
    }

    // MARK: login

    static func login() {
        let m = Model.shared
        let user = textField("username", "Username")
        let secure = textField("password", "Password", secure: true)
        let plain = textField("password-plain", "Password")
        plain.isHidden = true
        m.reg("password-visible") { !plain.isHidden }
        let show = checkbox("Show Password", "show-password")
        bind(show) {
            m.press("show-password")
            let visible = show.state == .on
            if visible { plain.stringValue = secure.stringValue } else { secure.stringValue = plain.stringValue }
            plain.isHidden = !visible
            secure.isHidden = visible
        }
        m.facts["signed_in"] = 0
        m.facts["save_password"] = NSNull()
        var w: NSWindow!
        let signIn = button("Sign In", "sign-in") {
            let pw = plain.isHidden ? secure.stringValue : plain.stringValue
            guard !user.stringValue.isEmpty, !pw.isEmpty else { return }
            m.facts["signed_in"] = (m.facts["signed_in"] as? Int ?? 0) + 1
            m.facts["signed_in_as"] = user.stringValue
            m.after(0.3) {
                let a = NSAlert()
                a.messageText = "Save Password?"
                a.informativeText = "Save the password for \(user.stringValue)?"
                a.addButton(withTitle: "Save Password")
                a.addButton(withTitle: "Not Now")
                a.window.identifier = NSUserInterfaceItemIdentifier("save-password-sheet")
                let g = m.gen
                a.beginSheetModal(for: w) { r in
                    guard m.gen == g else { return }
                    m.facts["save_password"] = r == .alertFirstButtonReturn ? "saved" : "not-now"
                }
            }
        }
        signIn.keyEquivalent = "\r"
        w = makeWindow("login", "Sign In", vstack([
            labeled("Username", user),
            labeled("Password", hrow([secure, plain])),
            show,
            signIn,
        ]), x: 120, y: 80)
        w.makeFirstResponder(nil)
        // Like a page that focuses its field shortly after load.
        m.after(0.7) { w.makeFirstResponder(user) }
    }

    // MARK: dialogs

    static func dialogs() {
        let m = Model.shared
        for k in ["sheet_result", "alert_result", "sheet_alert_result", "save_result", "save_name"] { m.facts[k] = NSNull() }
        var w: NSWindow!
        let showSheet = button("Show Sheet", "show-sheet") {
            let sheet = FixtureWindow(contentRect: NSRect(x: 0, y: 0, width: 360, height: 140),
                                      styleMask: [.titled], backing: .buffered, defer: false)
            sheet.isReleasedWhenClosed = false
            sheet.title = "Rename"
            sheet.identifier = NSUserInterfaceItemIdentifier("rename-sheet")
            let f = textField("sheet-field", "Sheet Field")
            let ok = button("OK", "sheet-ok") {
                m.facts["sheet_result"] = ["button": "OK", "text": f.stringValue]
                w.endSheet(sheet)
            }
            ok.keyEquivalent = "\r"
            let cancel = button("Cancel", "sheet-cancel") {
                m.facts["sheet_result"] = ["button": "Cancel", "text": f.stringValue]
                w.endSheet(sheet)
            }
            cancel.keyEquivalent = "\u{1b}"
            let content = vstack([labeled("New name", f), hrow([cancel, ok])])
            content.translatesAutoresizingMaskIntoConstraints = false
            let c = NSView()
            c.addSubview(content)
            NSLayoutConstraint.activate([
                content.leadingAnchor.constraint(equalTo: c.leadingAnchor, constant: 20),
                content.topAnchor.constraint(equalTo: c.topAnchor, constant: 20),
            ])
            sheet.contentView = c
            w.beginSheet(sheet) { _ in sheet.orderOut(nil) }
        }
        // An application-modal alert: the press does not return until the
        // alert is answered.
        let showAlert = button("Show Alert", "show-alert") {
            let a = NSAlert()
            a.messageText = "Confirm Action"
            a.informativeText = "Proceed with the action?"
            a.addButton(withTitle: "Proceed")
            a.addButton(withTitle: "Cancel")
            a.window.identifier = NSUserInterfaceItemIdentifier("confirm-alert")
            let g = m.gen
            let r = a.runModal()
            guard m.gen == g else { return }
            m.facts["alert_result"] = r == .alertFirstButtonReturn ? "Proceed" : (r == .alertSecondButtonReturn ? "Cancel" : "aborted")
        }
        let sheetAlert = button("Show Sheet Alert", "show-sheet-alert") {
            let a = NSAlert()
            a.messageText = "Discard changes?"
            a.addButton(withTitle: "Discard")
            a.addButton(withTitle: "Keep")
            a.window.identifier = NSUserInterfaceItemIdentifier("discard-alert")
            let g = m.gen
            a.beginSheetModal(for: w) { r in
                guard m.gen == g else { return }
                m.facts["sheet_alert_result"] = r == .alertFirstButtonReturn ? "Discard" : "Keep"
            }
        }
        let saveAs = button("Save As\u{2026}", "save-as-button") { showSavePanel(on: w) }
        // Retitles the window first (an unsaved-changes mark) and only later
        // opens the real dialog.
        let editPrompt = button("Edit Then Prompt", "edit-then-prompt") {
            m.after(0.2) { w.title = "Dialogs*" }
            m.after(1.4) {
                let a = NSAlert()
                a.messageText = "Unsaved Changes"
                a.addButton(withTitle: "Save")
                a.addButton(withTitle: "Don't Save")
                a.window.title = "Unsaved Changes"
                a.window.identifier = NSUserInterfaceItemIdentifier("unsaved-alert")
                let g = m.gen
                a.beginSheetModal(for: w) { r in
                    guard m.gen == g else { return }
                    m.facts["unsaved_result"] = r == .alertFirstButtonReturn ? "Save" : "Don't Save"
                }
            }
        }
        let doc = textField("doc-field", "Document Text")
        w = makeWindow("dialogs", "Dialogs", vstack([
            hrow([showSheet, showAlert, sheetAlert]),
            hrow([saveAs, editPrompt]),
            labeled("Document Text", doc),
        ]), x: 100, y: 60)
    }

    // showSavePanel opens a real save panel in a scratch folder that already
    // holds taken.txt, so saving under that name raises the panel's own
    // replace confirmation as a sheet on the sheet. Nothing is written.
    static func showSavePanel(on w: NSWindow?) {
        let m = Model.shared
        let taken = saveDir.appendingPathComponent("taken.txt")
        if !FileManager.default.fileExists(atPath: taken.path) {
            FileManager.default.createFile(atPath: taken.path, contents: Data("taken\n".utf8))
        }
        let p = NSSavePanel()
        p.directoryURL = saveDir
        p.nameFieldStringValue = "Untitled.txt"
        p.nameFieldLabel = "Save As:"
        p.allowedContentTypes = [.plainText]
        p.identifier = NSUserInterfaceItemIdentifier("save-panel")
        m.retained.append(p)
        let g = m.gen
        let done: (NSApplication.ModalResponse) -> Void = { r in
            guard m.gen == g else { return }
            m.facts["save_result"] = r == .OK ? "saved" : "cancelled"
            m.facts["save_name"] = r == .OK ? orNull(p.url?.lastPathComponent) : NSNull()
            // A saved document takes the file name as its window title.
            if r == .OK, let w, let name = p.url?.lastPathComponent { w.title = name }
        }
        if let w { p.beginSheetModal(for: w, completionHandler: done) } else { p.begin(completionHandler: done) }
    }

    // openDialog is File > Open: a plain dialog window that shows late.
    static func openDialog() {
        let m = Model.shared
        let f = textField("open-path", "Path")
        let ok = button("Open", "open-ok") { m.facts["open_path"] = f.stringValue }
        ok.keyEquivalent = "\r"
        var w: NSWindow!
        let cancel = button("Cancel", "open-cancel") { w.close() }
        w = makeWindow("open-dialog", "Open Dialog", vstack([labeled("Path", f), hrow([cancel, ok])]), x: 160, y: 140)
    }

    // MARK: hidden modal

    static func hiddenModal() {
        let m = Model.shared
        m.facts["hidden_dismissed"] = false
        let field = textField("doc-field", "Doc Field")
        let b = button("Doc Button", "doc-button")
        makeWindow("blocked-document", "Blocked Document", vstack([labeled("Doc Field", field), b]), x: 100, y: 80)
        m.after(0.2) {
            var panel: NSWindow!
            let dismiss = button("Dismiss", "hidden-dismiss") {
                m.facts["hidden_dismissed"] = true
                NSApp.stopModal()
                panel.orderOut(nil)
            }
            panel = makeWindow("hidden-dialog", "Hidden Dialog", vstack([label("This dialog is modal but transparent."), dismiss]),
                               x: 160, y: 140, show: false)
            panel.alphaValue = 0
            panel.ignoresMouseEvents = true
            NSApp.runModal(for: panel)
        }
    }

    // MARK: menus

    static var menusRefresh: (() -> Void)?

    static func menusChanged() { menusRefresh?() }

    static func menus() {
        let m = Model.shared
        let status = label("")
        let ruler = label("")
        let wrap = label("")
        markID(status, "status-bar-label")
        markID(ruler, "ruler-label")
        markID(wrap, "wrap-label")
        let refresh = {
            status.stringValue = m.statusBar ? "Status Bar: shown" : "Status Bar: hidden"
            ruler.stringValue = m.ruler ? "Ruler: shown" : "Ruler: hidden"
            wrap.stringValue = m.wrapToPage ? "Wrap: page" : "Wrap: window"
        }
        refresh()
        menusRefresh = refresh
        m.teardowns.append { menusRefresh = nil }
        let (body, _) = textArea("menus-body", "Body", value: "Use the View and Format menus.")
        makeWindow("menus", "Menus", vstack([body, status, ruler, wrap]), x: 100, y: 60)
    }

    // MARK: names

    static func names() {
        let m = Model.shared
        let printPreview = button("Print Preview", "print-preview-button")
        let save = button("Save\u{2026}", "save-ellipsis")
        let options = button("_Options", "options-underscore")
        let account = button("Account:", "account-colon")
        let closeAll = button("Close All", "close-all")
        let bold = button("Bold", "bold-button")
        let underline = button("Underline Text", "underline-text")
        let hiddenBold = button("Bold", "hidden-bold")
        hiddenBold.isHidden = true

        let styles = SimpleTable(id: "styles", name: "Styles", rows: ["Italic", "Strikethrough", "Shadow"])
        m.retained.append(styles)
        m.reg("styles-selected") { styles.selectedNames() }
        makeWindow("names", "Names", vstack([
            hrow([printPreview, save, options, account]),
            hrow([closeAll, bold, underline, hiddenBold]),
            labeled("Styles", styles.scroll),
        ]), x: 100, y: 60)
    }

    // MARK: tabs

    static func tabs() {
        let m = Model.shared
        let tv = NSTabView()
        markID(tv, "tabs")
        tv.setAccessibilityLabel("Settings Tabs")
        let sync = checkbox("Enable Sync", "enable-sync")
        let proxy = textField("proxy", "Proxy")
        for (title, view) in [("General", sync as NSView), ("Advanced", labeled("Proxy", proxy)), ("About", label("CUAFixture tabs scene"))] {
            let item = NSTabViewItem(identifier: title)
            item.label = title
            let c = NSView()
            view.translatesAutoresizingMaskIntoConstraints = false
            c.addSubview(view)
            NSLayoutConstraint.activate([
                view.leadingAnchor.constraint(equalTo: c.leadingAnchor, constant: 16),
                view.topAnchor.constraint(equalTo: c.topAnchor, constant: 16),
            ])
            item.view = c
            tv.addTabViewItem(item)
        }
        _ = fixed(tv, 420, 200)
        m.reg("selected-tab") { [weak tv] in orNull(tv?.selectedTabViewItem?.label) }
        makeWindow("tabs", "Tabs", tv, x: 100, y: 60)
    }

    // MARK: lists

    static func lists() {
        let m = Model.shared
        let tree = TreeController(id: "files", name: "Files", roots: [
            Node("Documents", [
                Node("Reports", [Node("Q1 Report"), Node("Q2 Report")]),
                Node("Letters", [Node("Cover Letter")]),
            ]),
            Node("Pictures", [Node("Holiday")]),
            Node("Music"),
        ])
        m.retained.append(tree)
        tree.outline.expandItem(tree.roots[0])
        m.reg("tree-selected") { tree.selectedPath() }
        m.reg("tree-expanded") { tree.expandedPaths() }

        let people = SimpleTable(id: "people", name: "People", rows: ["Alice", "Bob", "Carol", "Dave", "Eve"],
                                 second: ["Engineer", "Designer", "Manager", "Analyst", "Engineer"], multiple: true)
        m.retained.append(people)
        m.reg("table-selected") { people.selectedNames() }
        m.reg("table-opened") { people.opened }
        makeWindow("lists", "Lists", hrow([tree.scroll, people.scroll], spacing: 20), x: 100, y: 60)
    }

    // MARK: live list

    static func liveList() {
        let m = Model.shared
        let t = SimpleTable(id: "queue", name: "Queue", rows: ["Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot"])
        m.retained.append(t)
        let pause = checkbox("Pause Updates", "pause-updates")
        var rotations = 0
        m.every(0.7) {
            guard pause.state == .off else { return }
            let selected = Set(t.selectedNames())
            t.rows.insert(t.rows.removeLast(), at: 0)
            t.table.reloadData()
            t.table.selectRowIndexes(IndexSet(t.rows.indices.filter { selected.contains(t.rows[$0]) }), byExtendingSelection: false)
            rotations += 1
        }
        m.reg("queue-selected") { t.selectedNames() }
        m.reg("queue-opened") { t.opened }
        m.regVolatile("queue-order") { t.rows }
        m.regVolatile("queue-rotations") { rotations }
        makeWindow("live-list", "Live List", vstack([t.scroll, pause]), x: 100, y: 60)
    }

    // MARK: slow

    static func slow() {
        let m = Model.shared
        m.facts["confirmed_code"] = NSNull()
        m.facts["validation_shown"] = 0
        let open = button("Open Slowly", "open-slowly") {
            m.after(1.5) {
                let content = vstack([label(" ")])
                let w = makeWindow("slow-dialog", "Slow Dialog", content, x: 180, y: 160)
                m.after(1.0) {
                    let code = textField("code", "Code")
                    let confirm = button("Confirm", "confirm") {
                        m.facts["confirmed_code"] = code.stringValue
                        w.close()
                    }
                    confirm.keyEquivalent = "\r"
                    let cancel = button("Cancel", "slow-cancel") { w.close() }
                    content.addArrangedSubview(labeled("Code", code))
                    content.addArrangedSubview(hrow([cancel, confirm]))
                    w.setContentSize(w.contentView!.fittingSize)
                    w.makeFirstResponder(code)
                }
            }
        }
        // Return in Query raises a validation window a moment later.
        let query = textField("query", "Query")
        bind(query) {
            m.after(0.7) {
                m.facts["validation_shown"] = (m.facts["validation_shown"] as? Int ?? 0) + 1
                var v: NSWindow!
                let ok = button("OK", "validation-ok") { v.close() }
                ok.keyEquivalent = "\r"
                v = makeWindow("validation", "Validation Error", vstack([label("Query is not valid."), ok]), x: 200, y: 200)
            }
        }
        makeWindow("slow", "Slow Start", vstack([open, labeled("Query", query)]), x: 100, y: 60)
    }

    // MARK: windows

    static func windows() {
        let m = Model.shared
        var opened = 0
        m.reg("second-windows-opened") { opened }
        let openSecond = button("Open Second Window", "open-second") {
            opened += 1
            let n = opened
            let f = textField("second-\(n)-field", "Second Field")
            makeWindow("second-\(n)", "Second Window", vstack([label("Second window \(n)"), labeled("Second Field", f)]),
                       x: 420 + CGFloat(n) * 20, y: 80 + CGFloat(n) * 20)
        }
        let mainField = textField("main-field", "Main Field")
        makeWindow("long-title", "Quarterly Planning Review for the Northern Region Operations Team - Draft Version 7 (Shared With Finance, Legal and Executive Staff)",
                   vstack([button("Long Title Button", "long-title-button")]), x: 60, y: 420)
        var closable: NSWindow!
        m.facts["closable_closed"] = false
        let close = button("Close This Window", "close-this") {
            m.facts["closable_closed"] = true
            closable.close()
        }
        closable = makeWindow("closable", "Closable", vstack([label("This window closes itself."), close]), x: 420, y: 300)
        var edit: NSWindow!
        let editField = textField("edit-me-field", "Edit Me Field")
        FieldWatch.shared.onChange["edit-me-field"] = { edit.title = "Edit Me*" }
        edit = makeWindow("edit-me", "Edit Me", vstack([labeled("Text", editField)]), x: 60, y: 300)
        makeWindow("windows", "Windows", vstack([openSecond, labeled("Main Field", mainField)]), x: 60, y: 60)
    }

    // MARK: twins

    static func twins() {
        let m = Model.shared
        func twin(_ id: String, _ title: String, x: CGFloat, y: CGFloat) -> NSWindow {
            let f = textField("note@\(id)", "Note")
            let b = button("Mark", "mark@\(id)")
            return makeWindow(id, title, vstack([label(id), labeled("Note", f), b]), x: x, y: y)
        }
        _ = twin("report", "Report", x: 520, y: 60)
        _ = twin("report-draft", "Report Draft", x: 520, y: 220)
        _ = twin("summary-one", "Summary One", x: 520, y: 380)
        _ = twin("summary-two", "Summary Two", x: 760, y: 380)
        let solo = twin("solo", "Solo", x: 760, y: 60)
        let hidden = twin("twin-c", "Twin", x: 300, y: 300)
        _ = twin("twin-a", "Twin", x: 60, y: 60)
        _ = twin("twin-b", "Twin", x: 60, y: 260)
        hidden.miniaturize(nil)
        solo.miniaturize(nil)
        m.ready = { hidden.isMiniaturized && solo.isMiniaturized }
    }

    // MARK: unnamed

    static func unnamed() {
        let m = Model.shared
        let star = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { r in
            NSColor.systemYellow.setFill()
            NSBezierPath(ovalIn: r.insetBy(dx: 2, dy: 2)).fill()
            return true
        }
        let iconButton = NSButton(image: star, target: nil, action: nil)
        iconButton.bezelStyle = .rounded
        markID(iconButton, "icon-button")
        bind(iconButton) { m.press("icon-button") }
        let field = textField("shipping-field", "")
        let check = NSButton(checkboxWithTitle: "", target: nil, action: nil)
        markID(check, "untitled-check")
        bind(check) { m.press("untitled-check") }
        m.reg("untitled-check") { checkState(check.state) }
        let pop = NSPopUpButton(frame: .zero, pullsDown: false)
        pop.addItems(withTitles: ["One", "Two", "Three"])
        markID(pop, "untitled-popup")
        bind(pop) { m.press("untitled-popup") }
        m.reg("untitled-popup") { orNull(pop.titleOfSelectedItem) }
        makeWindow("unnamed", "Unnamed Controls", vstack([
            hrow([label("Shipping"), field]),
            hrow([iconButton, check, pop]),
        ]), x: 100, y: 60)
    }

    // MARK: canvas

    static func canvas() {
        let m = Model.shared
        let c = CanvasView(id: "canvas")
        let tile = LogoTile(id: "logo-tile")
        let stubborn = StubbornButton(title: "Stubborn", target: nil, action: nil)
        stubborn.bezelStyle = .rounded
        markID(stubborn, "stubborn")
        bind(stubborn) { m.press("stubborn") }
        m.reg("canvas-presses") { c.presses }
        m.reg("canvas-misses") { c.misses }
        m.reg("canvas-text") { c.text }
        m.reg("canvas-input-focused") { c.inputFocused }
        m.reg("logo-tile-clicks") { tile.clicks }
        m.reg("stubborn-ax-refusals") { stubborn.refused }
        makeWindow("canvas", "Canvas", vstack([c, hrow([tile, stubborn])]), x: 100, y: 60)
    }

    // MARK: animated

    static func animated() {
        let m = Model.shared
        let spinner = NSProgressIndicator()
        spinner.style = .spinning
        spinner.isIndeterminate = true
        spinner.setAccessibilityLabel("Spinner")
        _ = fixed(spinner, 32, 32)
        spinner.startAnimation(nil)
        let clock = label("00:00.0")
        clock.font = .monospacedDigitSystemFont(ofSize: 20, weight: .regular)
        markID(clock, "clock")
        let cycler = ColorCycler()
        _ = fixed(cycler, 60, 40)
        let start = Date()
        m.every(0.1) {
            let t = Date().timeIntervalSince(start)
            clock.stringValue = String(format: "%02d:%04.1f", Int(t) / 60, t.truncatingRemainder(dividingBy: 60))
        }
        m.every(0.25) {
            cycler.phase += 1
            cycler.needsDisplay = true
        }
        m.reg("animating") { true }
        m.regVolatile("clock") { clock.stringValue }
        let stable = button("Stable Button", "stable-button")
        makeWindow("animated", "Animated", vstack([hrow([spinner, clock, cycler]), stable]), x: 100, y: 60)
    }

    // MARK: link

    static let docsURL = "https://example.com/Docs/Fennec?ref=CUA"
    static let notesURL = "https://example.com/release-notes"

    static func link() {
        let m = Model.shared
        let (sv, tv) = textArea("link-text", "Link Text", size: NSSize(width: 360, height: 60), editable: false)
        let s = NSMutableAttributedString(string: "Read the Fennec Docs for details.")
        s.addAttribute(.link, value: URL(string: docsURL)!, range: NSRange(location: 9, length: 11))
        tv.textStorage?.setAttributedString(s)
        let delegate = LinkCatcher()
        tv.delegate = delegate
        m.retained.append(delegate)
        let notes = LinkButton(title: "Release Notes", target: nil, action: nil)
        notes.isBordered = false
        notes.contentTintColor = .linkColor
        markID(notes, "release-notes")
        notes.setAccessibilityRole(.link)
        notes.url = URL(string: notesURL)
        bind(notes) {
            m.press("release-notes")
            m.log("links", ["url": notesURL])
        }
        makeWindow("link", "Link", vstack([sv, notes]), x: 100, y: 60)
    }

    // MARK: scroll

    static func longText() -> String {
        var lines: [String] = []
        for i in 1...2000 {
            lines.append(String(format: "Line %04d: the quick brown fox jumps over the lazy dog \u{1F98A} caf\u{00E9} \u{00FC}ber", i))
        }
        lines.append("END OF TEXT")
        return lines.joined(separator: "\n")
    }

    static func scroll() {
        let m = Model.shared
        let (textScroll, _) = textArea("long-text", "Long Text", value: longText(), size: NSSize(width: 420, height: 200), editable: false)
        m.readers.removeValue(forKey: "long-text")
        m.reg("long-text-length") { longText().count }
        m.reg("long-text-scroll-y") { [weak textScroll] in orNull(textScroll.map { Int($0.contentView.bounds.origin.y) }) }

        let list = vstack((1...200).map { i in button(String(format: "Item %03d", i), String(format: "item-%03d", i)) }, spacing: 4)
        list.addArrangedSubview(button("Bottom Button", "bottom-button"))
        let doc = FlippedView()
        list.translatesAutoresizingMaskIntoConstraints = false
        doc.addSubview(list)
        NSLayoutConstraint.activate([
            list.leadingAnchor.constraint(equalTo: doc.leadingAnchor, constant: 8),
            list.topAnchor.constraint(equalTo: doc.topAnchor, constant: 8),
        ])
        doc.layoutSubtreeIfNeeded()
        doc.frame = NSRect(origin: .zero, size: NSSize(width: 300, height: list.fittingSize.height + 16))
        let sv = CountingScrollView()
        sv.name = "Long List"
        sv.hasVerticalScroller = true
        sv.documentView = doc
        sv.setAccessibilityLabel("Long List")
        markID(sv, "long-list")
        _ = fixed(sv, 320, 260)
        m.reg("long-list-scroll-y") { [weak sv] in orNull(sv.map { Int($0.contentView.bounds.origin.y) }) }
        makeWindow("scroll", "Scroll", hrow([textScroll, sv], spacing: 20), x: 80, y: 60)
    }

    // MARK: big tree

    static func bigTree() {
        let rows = 1400
        let list = vstack((1...rows).map { i in
            hrow([label(String(format: "Row %04d", i)), button("Delete", String(format: "delete@%04d", i))])
        }, spacing: 2)
        list.addArrangedSubview(button("Final Button", "final-button"))
        let doc = FlippedView()
        list.translatesAutoresizingMaskIntoConstraints = false
        doc.addSubview(list)
        NSLayoutConstraint.activate([
            list.leadingAnchor.constraint(equalTo: doc.leadingAnchor, constant: 8),
            list.topAnchor.constraint(equalTo: doc.topAnchor, constant: 8),
        ])
        doc.layoutSubtreeIfNeeded()
        doc.frame = NSRect(origin: .zero, size: NSSize(width: 300, height: list.fittingSize.height + 16))
        let sv = CountingScrollView()
        sv.name = "Big List"
        sv.hasVerticalScroller = true
        sv.documentView = doc
        sv.setAccessibilityLabel("Big List")
        markID(sv, "big-list")
        _ = fixed(sv, 320, 400)
        makeWindow("big-tree", "Big Tree", sv, x: 100, y: 40)
    }

    // MARK: drag

    static func drag() {
        let m = Model.shared
        let pad = DragPad(id: "drag-pad")
        m.reg("token-in-zone") { pad.inZone }
        m.reg("token-origin") { [Int(pad.token.minX), Int(pad.token.minY)] }
        let level = NSSlider(value: 0, minValue: 0, maxValue: 100, target: nil, action: nil)
        level.setAccessibilityLabel("Level")
        markID(level, "level")
        _ = fixed(level, 300, 24)
        m.reg("level") { Int(level.doubleValue.rounded()) }
        makeWindow("drag", "Drag", vstack([pad, labeled("Level", level)]), x: 100, y: 60)
    }

    // MARK: click pad

    static func clickPad() {
        let pad = ClickPad(name: "Click Pad", id: "click-pad", size: NSSize(width: 500, height: 320))
        makeWindow("click-pad", "Click Pad", pad, x: 100, y: 60)
    }

    // MARK: keys

    static func keys() {
        let catcher = KeyCatcher(id: "key-catcher", size: NSSize(width: 420, height: 120))
        let echo = textField("echo", "Echo Field", width: 420)
        let disabled = button("Disabled Shortcut", "disabled-shortcut")
        disabled.keyEquivalent = "d"
        disabled.keyEquivalentModifierMask = .command
        disabled.isEnabled = false
        let target = button("Shortcut Target", "shortcut-target")
        target.keyEquivalent = "k"
        target.keyEquivalentModifierMask = .command
        let w = makeWindow("keys", "Keys", vstack([catcher, labeled("Echo Field", echo), hrow([disabled, target])]), x: 100, y: 60)
        w.makeFirstResponder(catcher)
    }

    // MARK: busy

    static func busy() {
        let m = Model.shared
        let seconds = NSStepper()
        seconds.minValue = 1
        seconds.maxValue = 30
        seconds.integerValue = 5
        seconds.setAccessibilityLabel("Freeze Seconds")
        markID(seconds, "freeze-seconds")
        let shown = label("5 s")
        bind(seconds) { shown.stringValue = "\(seconds.integerValue) s" }
        m.reg("freeze-seconds") { seconds.integerValue }
        let freeze = button("Freeze Now", "freeze-now") { freezeMain(Double(seconds.integerValue), source: "button") }
        let after = textField("after-freeze", "After Freeze")
        makeWindow("busy", "Busy", vstack([labeled("Freeze Seconds", hrow([seconds, shown])), freeze, labeled("After Freeze", after)]), x: 100, y: 60)
    }

    // MARK: spaces

    static func spaces() {
        let m = Model.shared
        func win(_ id: String, _ title: String, x: CGFloat) -> NSWindow {
            let f = textField("space-field@\(id)", "Space Field")
            let b = button("Space Button", "space-button@\(id)")
            let w = makeWindow(id, title, vstack([label(id), labeled("Space Field", f), b]), x: x, y: 80)
            w.collectionBehavior.insert(.fullScreenPrimary)
            return w
        }
        let here = win("space-here", "Space Twin", x: 80)
        let away = win("space-away", "Space Twin", x: 380)
        let awayOnly = win("away-only", "Away Only", x: 680)
        let delegate = FullScreenWatcher()
        m.retained.append(delegate)
        away.delegate = delegate
        awayOnly.delegate = delegate
        delegate.queue = [away, awayOnly]
        delegate.done = { here.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true) }
        delegate.next()
        m.ready = {
            away.styleMask.contains(.fullScreen) && awayOnly.styleMask.contains(.fullScreen) && here.isOnActiveSpace && delegate.queue.isEmpty
        }
    }

    // MARK: cover

    static func cover() {
        let m = Model.shared
        let pad = ClickPad(name: "Target Pad", id: "target-pad", size: NSSize(width: 520, height: 260))
        let under = button("Under Cover", "under-cover")
        let field = textField("covered-field", "Covered Field")
        let target = makeWindow("covered-target", "Covered Target", vstack([pad, hrow([under, field])]), x: 100, y: 80)
        let ownPad = ClickPad(name: "Same App Cover", id: "same-app-pad", size: NSSize(width: 160, height: 120))
        let own = makeWindow("same-app-cover", "Same App Cover", ownPad, x: 60, y: 140)
        own.orderFront(nil)
        // The other app's cover is a second process placed over the right
        // part of the target pad.
        let tf = target.frame
        let rect = NSRect(x: tf.maxX - 220, y: tf.minY + 60, width: 200, height: 160)
        m.facts["cover_ready"] = false
        let child = Process()
        // A helper bundle with its own name, so allowlists can tell it apart.
        let helper = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/CUACover.app/Contents/MacOS/CUACover")
        child.executableURL = FileManager.default.isExecutableFile(atPath: helper.path) ? helper : Bundle.main.executableURL
        var env = ProcessInfo.processInfo.environment
        env.removeValue(forKey: "CUA_FIXTURE_SOCKET")
        env["CUA_FIXTURE_COVER"] = "\(Int(rect.minX)),\(Int(rect.minY)),\(Int(rect.width)),\(Int(rect.height))"
        child.environment = env
        let pipe = Pipe()
        child.standardOutput = pipe
        let g = m.gen
        var buffer = Data()
        pipe.fileHandleForReading.readabilityHandler = { h in
            let d = h.availableData
            if d.isEmpty { h.readabilityHandler = nil; return }
            DispatchQueue.main.async {
                guard m.gen == g else { return }
                buffer.append(d)
                while let nl = buffer.firstIndex(of: 0x0A) {
                    let line = buffer[buffer.startIndex..<nl]
                    buffer.removeSubrange(buffer.startIndex...nl)
                    guard let obj = try? JSONSerialization.jsonObject(with: Data(line)) as? [String: Any] else { continue }
                    if obj["ready"] != nil {
                        m.facts["cover_ready"] = true
                    } else {
                        m.log("cover_clicks", obj)
                    }
                }
            }
        }
        do {
            try child.run()
            m.regVolatile("cover_pid") { Int(child.processIdentifier) }
        } catch {
            m.facts["cover_error"] = error.localizedDescription
        }
        m.teardowns.append {
            pipe.fileHandleForReading.readabilityHandler = nil
            if child.isRunning { child.terminate() }
        }
        m.ready = { (m.facts["cover_ready"] as? Bool ?? false) || m.facts["cover_error"] != nil }
    }

    // MARK: displays

    static func displays() {
        let m = Model.shared
        m.reg("screens") {
            NSScreen.screens.map { s -> [String: Any] in ["frame": desktopFrame(s.frame), "scale": Double(s.backingScaleFactor)] }
        }
        let screens = NSScreen.screens
        makeWindow("main-display-probe", "Main Display Probe", vstack([button("Probe Button", "probe@main"), ClickPad(name: "Main Probe Pad", id: "main-probe-pad", size: NSSize(width: 240, height: 140))]), x: 80, y: 80, screen: screens.first)
        if screens.count > 1 {
            // Prefer a display left of or above the main one (negative coordinates).
            let other = screens.dropFirst().first(where: { $0.frame.minX < 0 || $0.frame.maxY > screens[0].frame.maxY }) ?? screens[1]
            makeWindow("other-display-probe", "Other Display Probe", vstack([button("Probe Button", "probe@other"), ClickPad(name: "Other Probe Pad", id: "other-probe-pad", size: NSSize(width: 240, height: 140))]), x: 80, y: 80, screen: other)
        }
    }

    // MARK: launch

    static func launch() {
        let m = Model.shared
        m.reg("args") { Array(CommandLine.arguments.dropFirst()) }
        makeWindow("calculator-results", "Calculator Results", vstack([label("A fixture window whose title names another app.")]), x: 420, y: 80)
        makeWindow("textedit-notes", "TextEdit Notes", vstack([label("A fixture window whose title names another app.")]), x: 420, y: 240)
        makeWindow("launcher-home", "Launcher Home", vstack([label("Opened URLs and documents are reported in state.launch.opened."), textField("launcher-field", "Launcher Field")]), x: 80, y: 80)
    }

    // MARK: web

    static func web() { WebScene.build() }
}

// LinkCatcher records link clicks in a text view instead of opening them.
final class LinkCatcher: NSObject, NSTextViewDelegate {
    func textView(_ textView: NSTextView, clickedOnLink link: Any, at charIndex: Int) -> Bool {
        let s = (link as? URL)?.absoluteString ?? "\(link)"
        Model.shared.log("links", ["url": s])
        return true
    }
}

final class LinkButton: NSButton {
    var url: URL?
    override func accessibilityURL() -> URL? { url }
}

// FullScreenWatcher moves windows into full screen one at a time.
final class FullScreenWatcher: NSObject, NSWindowDelegate {
    var queue: [NSWindow] = []
    var done: () -> Void = {}

    func next() {
        guard let w = queue.first else { done(); return }
        w.makeKeyAndOrderFront(nil)
        w.toggleFullScreen(nil)
    }

    func windowDidEnterFullScreen(_ n: Notification) {
        if let w = n.object as? NSWindow, queue.first === w { queue.removeFirst() }
        let g = Model.shared.gen
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
            if Model.shared.gen == g { self.next() }
        }
    }
}

// MARK: tables and trees

final class SimpleTable: NSObject, NSTableViewDataSource, NSTableViewDelegate {
    var rows: [String]
    let second: [String]?
    let table = NSTableView()
    let scroll = NSScrollView()
    var opened: [String] = []

    init(id: String, name: String, rows: [String], second: [String]? = nil, multiple: Bool = false) {
        self.rows = rows
        self.second = second
        super.init()
        let c1 = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("name"))
        c1.title = "Name"
        c1.width = 140
        table.addTableColumn(c1)
        if second != nil {
            let c2 = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("role"))
            c2.title = "Role"
            c2.width = 120
            table.addTableColumn(c2)
        }
        table.allowsMultipleSelection = multiple
        table.dataSource = self
        table.delegate = self
        table.setAccessibilityLabel(name)
        markID(table, id)
        table.target = self
        table.doubleAction = #selector(open(_:))
        scroll.documentView = table
        scroll.hasVerticalScroller = true
        scroll.borderType = .bezelBorder
        _ = fixed(scroll, second == nil ? 200 : 300, 180)
    }

    @objc func open(_ s: Any?) {
        if table.clickedRow >= 0 { opened.append(rows[table.clickedRow]) }
    }

    func selectedNames() -> [String] { table.selectedRowIndexes.filter { $0 < rows.count }.map { rows[$0] } }

    func numberOfRows(in tableView: NSTableView) -> Int { rows.count }

    func tableView(_ tableView: NSTableView, viewFor col: NSTableColumn?, row: Int) -> NSView? {
        let text = col?.identifier.rawValue == "role" ? (second?[row] ?? "") : rows[row]
        let cell = NSTableCellView()
        let l = NSTextField(labelWithString: text)
        l.translatesAutoresizingMaskIntoConstraints = false
        cell.addSubview(l)
        cell.textField = l
        NSLayoutConstraint.activate([
            l.leadingAnchor.constraint(equalTo: cell.leadingAnchor, constant: 4),
            l.centerYAnchor.constraint(equalTo: cell.centerYAnchor),
        ])
        return cell
    }
}

final class Node: NSObject {
    let name: String
    let children: [Node]
    weak var parent: Node?
    init(_ name: String, _ children: [Node] = []) {
        self.name = name
        self.children = children
        super.init()
        for c in children { c.parent = self }
    }
    var path: String { parent.map { $0.path + "/" + name } ?? name }
}

final class TreeController: NSObject, NSOutlineViewDataSource, NSOutlineViewDelegate {
    let roots: [Node]
    let outline = NSOutlineView()
    let scroll = NSScrollView()

    init(id: String, name: String, roots: [Node]) {
        self.roots = roots
        super.init()
        let c = NSTableColumn(identifier: NSUserInterfaceItemIdentifier("name"))
        c.title = name
        c.width = 200
        outline.addTableColumn(c)
        outline.outlineTableColumn = c
        outline.dataSource = self
        outline.delegate = self
        outline.setAccessibilityLabel(name)
        markID(outline, id)
        scroll.documentView = outline
        scroll.hasVerticalScroller = true
        scroll.borderType = .bezelBorder
        _ = fixed(scroll, 240, 180)
    }

    func all() -> [Node] {
        var out: [Node] = []
        func walk(_ n: Node) { out.append(n); n.children.forEach(walk) }
        roots.forEach(walk)
        return out
    }

    func selectedPath() -> Any {
        let r = outline.selectedRow
        guard r >= 0, let n = outline.item(atRow: r) as? Node else { return NSNull() }
        return n.path
    }

    func expandedPaths() -> [String] { all().filter { !$0.children.isEmpty && outline.isItemExpanded($0) }.map { $0.path } }

    func outlineView(_ o: NSOutlineView, numberOfChildrenOfItem item: Any?) -> Int { (item as? Node)?.children.count ?? roots.count }
    func outlineView(_ o: NSOutlineView, child index: Int, ofItem item: Any?) -> Any { (item as? Node)?.children[index] ?? roots[index] }
    func outlineView(_ o: NSOutlineView, isItemExpandable item: Any) -> Bool { !((item as? Node)?.children.isEmpty ?? true) }
    func outlineView(_ o: NSOutlineView, viewFor col: NSTableColumn?, item: Any) -> NSView? {
        let cell = NSTableCellView()
        let l = NSTextField(labelWithString: (item as? Node)?.name ?? "")
        l.translatesAutoresizingMaskIntoConstraints = false
        cell.addSubview(l)
        cell.textField = l
        NSLayoutConstraint.activate([
            l.leadingAnchor.constraint(equalTo: cell.leadingAnchor, constant: 2),
            l.centerYAnchor.constraint(equalTo: cell.centerYAnchor),
        ])
        return cell
    }
}

// MARK: cover process

// runCover runs this binary as a separate app that shows one floating
// window at the given AppKit frame and reports clicks on stdout.
func runCover(_ spec: String) {
    let p = spec.split(separator: ",").compactMap { Double($0) }
    let frame = p.count == 4 ? NSRect(x: p[0], y: p[1], width: p[2], height: p[3]) : NSRect(x: 200, y: 200, width: 200, height: 160)
    let app = NSApplication.shared
    app.setActivationPolicy(.accessory)
    let w = NSWindow(contentRect: frame, styleMask: [.titled], backing: .buffered, defer: false)
    w.title = "Other App Cover"
    w.level = .floating
    w.isReleasedWhenClosed = false
    let emit: ([String: Any]) -> Void = { e in
        if let d = try? JSONSerialization.data(withJSONObject: e, options: [.sortedKeys]) {
            FileHandle.standardOutput.write(d + Data([0x0A]))
        }
    }
    let pad = ClickPad(name: "Other App Pad", id: "other-app-pad", size: frame.size, sink: emit)
    pad.scrollSink = { e in var e = e; e["scroll"] = true; emit(e) }
    w.contentView = pad
    w.setFrame(w.frameRect(forContentRect: frame), display: true)
    w.orderFrontRegardless()
    emit(["ready": true])
    // Exit when the parent goes away.
    let parent = getppid()
    Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { _ in
        if getppid() != parent { exit(0) }
    }
    app.run()
}
