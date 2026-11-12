# CUAFixture scenes

CUAFixture (`cua/testapp`) is the app that T2 scenario and T3 comparison tests drive. A test resets it to a named scene, lets a backend (fennec-cua or shuttle) do a task, then reads the app's true state from its control socket and compares. Go access is `cua/internal/fixture` (`Launch`, `Reset`, `State`, `RawState`, `Scenes`, `Freeze`, `Close`).

Build with `bash cua/testapp/build.sh [out-dir]` (default `cua/testapp/build/CUAFixture.app`, ad-hoc signed). Launching it opens windows on the real desktop, so launch it only in the test VM.

## Control socket

The app listens on `$CUA_FIXTURE_SOCKET` (mode 0600). Without that variable, as when a backend under test launches the app, it listens on `$TMPDIR/cua-fixture.sock` unless another instance already answers there (`fixture.DefaultSocketPath`). One JSON object per line in each direction:

| Request | Answer |
|---|---|
| `{"op":"scenes"}` | `{"ok":true,"scenes":["basic",...]}` |
| `{"op":"reset","scene":"form"}` | `{"ok":true}` once the scene is ready (some scenes settle asynchronously: full screen, page load, minimizing) |
| `{"op":"state"}` | `{"ok":true,"state":{...}}` |
| `{"op":"freeze","seconds":5}` | `{"ok":true}` at once; then the main thread blocks for that long (0-60 s) |
| `{"op":"quit"}` | `{"ok":true}`; the app tears down and exits |
| anything else | `{"ok":false,"error":"..."}` |

Any request may carry `timeout_ms` (default 10000): how long to wait for the main thread. A `state` or `reset` the frozen main thread does not answer in time gets `{"ok":false,"error":"app is busy: main thread did not answer","busy":true}`. The Go client sets `timeout_ms` from its context deadline.

Requests are served in the run loop's common modes, so they are answered during menu tracking, inside `NSAlert.runModal` and inside `NSApp.runModal(for:)`; only a blocked main thread (the `busy` scene, `freeze`) holds them back. A reset aborts any app-modal loop, ends sheets, closes every window, stops timers and kills the cover helper before building the new scene; work scheduled by an earlier scene is dropped.

## State

| Key | Meaning |
|---|---|
| `scene` | current scene |
| `active` | the app is frontmost |
| `windows` | every visible or minimized fixture window, sheet, alert and titled panel, sorted by id: `id`, `title`, `kind` (`window`, `panel`, `sheet`), `owner` (sheet parent id), `sheet` (attached sheet id), `key`, `main`, `visible`, `minimized`, `transparent` (alpha 0), `fullscreen`, `on_active_space`, `frame` (`[x, y, w, h]` desktop points, top-left origin on the primary display), `screen` (index in `NSScreen.screens`) |
| `modal` | title of the app-modal window, or null |
| `focus` | `{window, element}`: key window id and the fixture id of its first responder (a field being edited reports the field) |
| `menu` | `status_bar`, `ruler` (bools), `wrap` (`window` or `page`): the true toggle states |
| `commands` | menu commands run, in order: `{name, via}`, `via` is `shortcut` when the key equivalent ran it and `menu` otherwise (mouse or accessibility press) |
| `presses` | fixture id to number of times its action ran (buttons, check boxes, popups, steppers) |
| `edits` | fixture id to number of text-change notifications (one per typed character or per accessibility value write) |
| `controls` | fixture id to current value: text fields and areas give their string, check boxes and switches `on`/`off`/`mixed`, popups their title, plus scene-specific values listed below |
| `events` | `clicks` (`pad`, `button` left/right/other, `count`, `modifiers` [ctrl, alt, shift, cmd], `x`, `y` view points from the pad's top left, `cell` such as `B3`), `scrolls` (`pad`, `dx`, `dy`, `cell`), `keys` (`key_code`, `chars`, `chars_ignoring`, `modifiers`, `repeat`, `equivalent`), `drags` (`from`, `to`, `in_zone`), `links` (`url`), `cover_clicks` (clicks on the other app's cover) |
| `facts` | scene-specific results listed below (dialog answers, submitted forms) |
| `launch` | `args` (command line), `opened` (URLs and files handed to the app since the last reset), `instances` (running processes with the fixture's bundle id) |
| `freezes` | `{seconds, source}` per main-thread freeze since the last reset |
| `volatile` | values that change on their own: `resets`, `pid`, `instance_pids`, clock text, live-list order, cover pid. `fixture.Client.State` drops this key so two runs compare equal; `RawState` keeps it |

A double click logs two clicks (count 1, then 2), as AppKit delivers them. Click coordinates can differ by a point between backends; a comparison that needs exact equality should compare `cell` or counts instead.

Unless noted, each scene opens its windows at fixed positions on the main display and activates the app.

## Scenes

### basic

Window `Basic`: `Count: N` label; buttons `Increment`, `Reset Count`, `Disabled Action` (disabled); text field `Note`; check box `Front On Edit` (when on, every edit of Note activates the app and fronts the window, like an app that steals the front mid-input).

State: `controls.count`, `controls.basic-note`, `controls.front-on-edit`, `presses.increment`, `facts.fronted_on_edit`.

Serves the generic and protocol-level oracle capabilities, where the fixture proves only that input landed once, never, or in order: CAP-DMN-011, CAP-DMN-012, CAP-DMN-014, CAP-DMN-015, CAP-DMN-017, CAP-DMN-021, CAP-DMN-022, CAP-DMN-023, CAP-DMN-024, CAP-DMN-025, CAP-DMN-026, CAP-DMN-028, CAP-DMN-029, CAP-DMN-037, CAP-RGB-005, CAP-RGB-012, CAP-RGB-014, CAP-RGB-016, CAP-RGB-017, CAP-RGB-025, CAP-RGB-060, CAP-RGB-066, CAP-RGB-070, CAP-RGA-050, CAP-RGA-065, CAP-STP-001, CAP-DSK-010, CAP-DSK-027, CAP-MAC-010, CAP-RGB-053, CAP-PLN-024 (Front On Edit), CAP-FLW-019, CAP-FLW-024, CAP-FLW-025.

### form, form-filled

Window `Form`: text field `Name`; `Email:` (trailing colon); secure field `Password`; check box `Subscribe`; tri-state check box `Notifications` (starts mixed); switch `Dark Mode`; radio group `Size` (Small, Medium, Large; Medium on); popup `Color` (Red, Green, Blue); editable combo box `City` (Berlin, Paris, Tokyo); `Font Size` text field showing `12 pt` with stepper `Font Size Stepper` (6-72; typed values are parsed and shown back with the unit); slider `Volume` (0-100, 50); text view `Notes`; text field `Shout` that rewrites its text to upper case on every change (like autocorrect); buttons `Submit` (default, Return) and `Clear`. `form-filled` starts with Name `Existing Name` and Notes `Existing notes`.

State: `controls.name`, `email`, `password` (the real text), `subscribe`, `notifications`, `dark-mode`, `size`, `color`, `city`, `font-size` (number), `font-size-text`, `volume`, `notes`, `shout`; `facts.submitted` (all of those at the moment Submit ran, or null); `edits.*`; `focus`.

Serves: CAP-MAC-007, CAP-STP-002, CAP-STP-005, CAP-STP-006, CAP-STP-007, CAP-STP-033, CAP-RGA-009 (Shout), CAP-RGA-016, CAP-RGA-019 (single-line field half), CAP-RGA-039, CAP-RGA-043, CAP-RGA-051, CAP-RGA-006 and CAP-PLN-019 and CAP-DSK-015 (non-terminal halves, Notes), CAP-RGA-008 (native-field half), CAP-DSK-009, CAP-DSK-011, CAP-DSK-012, CAP-DSK-013, CAP-DSK-014, CAP-DSK-021, CAP-RGB-023, CAP-RGB-032, CAP-RGB-038, CAP-RGB-041, CAP-RGB-059, CAP-RGB-073, CAP-PLN-013, CAP-VIS-002, CAP-VIS-017, CAP-VIS-019, CAP-DSK-037. `form-filled`: CAP-STP-027, CAP-RGB-061.

### login

Window `Sign In`: `Username`, secure `Password`, check box `Show Password` (swaps the secure field for a plain field also named `Password`, carrying the text over), button `Sign In` (default). Nothing is focused at first; focus moves into Username 0.7 s after the reset, like a page that focuses late. A successful sign-in shows a `Save Password?` sheet 0.3 s later that holds the focus (buttons `Save Password`, `Not Now`).

State: `controls.username`, `password`, `password-plain`, `password-visible`, `show-password`; `facts.signed_in` (count), `signed_in_as`, `save_password` (`saved`, `not-now` or null).

Serves: CAP-STP-025, CAP-STP-026, CAP-PLN-021, CAP-RGA-001, CAP-RGA-003 (password-field half), CAP-RGA-031, CAP-FLW-021, CAP-FLW-022, CAP-RGB-076, CAP-STP-024.

### dialogs

Window `Dialogs`: `Show Sheet` (sheet `Rename` with field `Sheet Field`, `OK` on Return, `Cancel` on Escape; the sheet has no close button, so a close request leaves it open); `Show Alert` (app-modal `NSAlert.runModal` "Confirm Action": the press does not return until it is answered); `Show Sheet Alert` (alert sheet "Discard changes?"); `Save As...` (a real save panel as a sheet, in a scratch folder that already holds `taken.txt`, so saving under that name raises the panel's own replace confirmation as a sheet on the sheet; nothing is written; a saved name becomes the window title); `Edit Then Prompt` (the title gains `*` after 0.2 s and an "Unsaved Changes" alert sheet opens 1.4 s later); field `Document Text`. File > Save As... (shift+cmd+s) opens the same panel 0.6 s late on the main window.

State: `facts.sheet_result` (`{button, text}`), `alert_result` (`Proceed`, `Cancel`, `aborted`), `sheet_alert_result`, `save_result` (`saved`, `cancelled`), `save_name`, `unsaved_result`; `windows[].owner` / `sheet`; `modal`.

Serves: CAP-STP-004 (close of a sheet that stays), CAP-STP-014 (late panel), CAP-STP-015, CAP-STP-016 (overwrite confirmation), CAP-STP-019, CAP-RGA-025, CAP-RGA-029, CAP-RGA-040, CAP-RGB-040, CAP-RGB-075, CAP-RGB-053 and CAP-RGB-027 (press that opens a modal alert; sheet owners), CAP-PLN-023, CAP-DSK-001, CAP-MAC-007 (sheet and alert roles), CAP-FLW-023.

### hidden-modal

Window `Blocked Document` (field `Doc Field`, button `Doc Button`). 0.2 s after the reset an app-modal panel `Hidden Dialog` starts with alpha 0 (open but invisible, blocking the document); its `Dismiss` button stops the modal.

State: `modal` (`Hidden Dialog`), `windows[]` entry with `transparent: true`, `facts.hidden_dismissed`, `presses.doc-button` (must stay 0).

Serves: CAP-STP-016 (hidden modal).

### menus

Window `Menus` (text view `Body`, labels showing status bar, ruler and wrap state). The menu bar is app-wide and present in every scene:

- CUAFixture: About, Quit (cmd+q)
- File: New Window (cmd+n, opens `Untitled`, `Untitled 2`, ...), Open... (cmd+o, opens `Open Dialog` 0.8 s late), Open Recent > Report.txt, Notes.txt, Close Window (cmd+w), Save As... (shift+cmd+s), Export... (disabled), Print Preview, Print... (cmd+p)
- Edit: Undo, Redo, Cut, Copy, Paste, Delete, Select All (cmd+a)
- View: Show Status Bar (cmd+/, toggle, starts off), Show Ruler (ctrl+cmd+r, toggle, starts ON but its check mark reads off until the menu is opened and AppKit validates it, as in a freshly launched TextEdit), Zoom In (cmd++), Zoom Out (cmd+-), Actual Size (ctrl+alt+shift+0, no command key), Reload (F5, a function-key equivalent), Forward Delete Line (cmd+forward delete)
- Format: Font > Bold (cmd+b), Italic (cmd+i), Underline (cmd+u); Text > Align > Align Left, Center, Align Right (three levels deep), Uppercase; Wrap to Page (shift+cmd+w; after use it retitles to Wrap to Window, but only when its menu is next validated)
- Window: Minimize (cmd+m), Bring All to Front

State: `menu.status_bar`, `menu.ruler`, `menu.wrap`, `commands` (with `via`).

Serves: CAP-STP-003, CAP-STP-011, CAP-STP-012, CAP-RGB-021, CAP-RGA-033, CAP-RGA-040, CAP-MAC-011 (menu items), CAP-MAC-016, CAP-DSK-025, CAP-DSK-036, CAP-PLN-013 (menu-bar step under auto).

### names

Window `Names`: buttons `Print Preview` (against the File menu's Print...), `Save...` (with an ellipsis character), `_Options`, `Account:`, `Close All`, a showing `Bold` button (against Format > Font > Bold, hidden while the menu is closed) plus a hidden `Bold` button, `Underline Text` (shares a word with Format > Font > Underline); table `Styles` with rows Italic (against the Italic menu item), Strikethrough, Shadow.

State: `presses.*`, `controls.styles-selected`, `commands`.

Serves: CAP-DSK-037, CAP-PLN-005, CAP-STP-012.

### tabs

Window `Tabs`: tab view `Settings Tabs` with General (check box `Enable Sync`), Advanced (field `Proxy`), About.

State: `controls.selected-tab`, `enable-sync`, `proxy`.

Serves: CAP-MAC-011 (tabs), CAP-MAC-007 (tab role).

### lists

Window `Lists`: outline `Files` (Documents > Reports > Q1 Report, Q2 Report; Documents > Letters > Cover Letter; Pictures > Holiday; Music; Documents expanded), multi-select table `People` (Name, Role: Alice, Bob, Carol, Dave, Eve; double click opens a row).

State: `controls.tree-selected` (path such as `Documents/Reports/Q1 Report`), `tree-expanded`, `table-selected`, `table-opened`.

Serves: CAP-MAC-011 (tree and list items), CAP-PLN-005 (list items as targets).

### live-list

Window `Live List`: table `Queue` (Alpha ... Foxtrot) whose last row moves to the top every 0.7 s, keeping the selection on the same names; check box `Pause Updates`.

State: `controls.queue-selected`, `queue-opened`; `volatile.queue-order`, `volatile.queue-rotations`.

Serves: CAP-DSK-027 (refs to moving rows), CAP-PLN-005, CAP-VIS-006 (a plan mapped by an earlier screenshot).

### slow

Window `Slow Start`: `Open Slowly` opens `Slow Dialog` after 1.5 s, empty at first; 1 s later it fills with field `Code` and buttons `Confirm` (default) and `Cancel`. Return in field `Query` opens `Validation Error` (button `OK`) 0.7 s later.

State: `facts.confirmed_code`, `facts.validation_shown`.

Serves: CAP-STP-013, CAP-STP-014, CAP-STP-020 (transient gap), CAP-STP-024, CAP-PLN-027, CAP-FLW-017, CAP-RGB-076.

### windows

Windows `Windows` (`Open Second Window`, which opens another `Second Window` per press; field `Main Field`), `Closable` (`Close This Window` closes its own window), `Edit Me` (typing into its field retitles it `Edit Me*`, a title-only change), and a window with a 128-character title the window server elides.

State: `controls.second-windows-opened`, `facts.closable_closed`, `windows`.

Serves: CAP-DSK-001, CAP-DSK-029, CAP-DSK-042, CAP-RGB-052, CAP-RGB-071, CAP-RGB-077, CAP-RGA-025, CAP-RGA-063, CAP-PLN-022 (window gone), CAP-PLN-027, CAP-VIS-001.

### twins

Windows titled `Twin` three times (`twin-a`, `twin-b` in front, `twin-c` minimized), `Report` and `Report Draft` (one exact match, one containing match), `Summary One` and `Summary Two` (two containing matches, no exact one), and `Solo` (the only window of its title, minimized). Each has field `Note` and button `Mark`, with ids suffixed by the window id (`note@twin-a`, `mark@twin-b`).

State: `controls.note@<window>`, `presses.mark@<window>`, `windows[].minimized`.

Serves: CAP-DSK-004 (minimized), CAP-DSK-007, CAP-DSK-021, CAP-DSK-028, CAP-RGB-045, CAP-RGB-049, CAP-RGB-051, CAP-PLN-022 (same-titled windows), CAP-PLN-028, CAP-STP-019, CAP-RGA-025, CAP-MAC-002 (shown versus minimized frames).

### unnamed

Window `Unnamed Controls`: an image-only button with no title or description, a text field with no accessible name next to a plain `Shipping` label, a check box with an empty title, a popup with no label.

State: `presses.icon-button`, `controls.shipping-field`, `untitled-check`, `untitled-popup`.

Serves: CAP-VIS-002 (empty labels), CAP-RGB-028, CAP-RGB-039, CAP-FLW-014.

### canvas

Window `Canvas`: a drawn orange `Draw Me` button and a drawn `Type Here` input in a view with no accessibility elements at all (a click on the input focuses it and keys are appended); `Logo Tile`, an accessible image with no press action (AX press unsupported, a real click counts); `Stubborn`, a button that refuses its AX press (a real click works).

State: `controls.canvas-presses`, `canvas-misses`, `canvas-text`, `canvas-input-focused`, `logo-tile-clicks`, `stubborn-ax-refusals`, `presses.stubborn`.

Serves: CAP-DMN-005, CAP-FLW-014, CAP-FLW-015, CAP-FLW-016, CAP-RGB-039, CAP-RGB-041, CAP-RGB-056, CAP-RGB-058, CAP-RGB-064 (focus half), CAP-RGB-059, CAP-DSK-014 (no focused text field), CAP-MAC-010 (refused action), CAP-MAC-012, CAP-STP-004 (press not performed), CAP-VIS-020.

### animated

Window `Animated`: a spinning progress indicator, a clock updated every 0.1 s, a square cycling through four colours every 0.25 s, and `Stable Button`.

State: `controls.animating`, `presses.stable-button`, `volatile.clock`.

Serves: CAP-DSK-044, CAP-RGB-011, CAP-RGB-024 (click right after a stillness wait).

### link

Window `Link`: a read-only text view "Read the Fennec Docs for details." whose `Fennec Docs` links to `https://example.com/Docs/Fennec?ref=CUA` (mixed case, for exact comparison); a borderless `Release Notes` button with the link role and URL `https://example.com/release-notes`. Link clicks are recorded, never opened.

State: `events.links`, `presses.release-notes`.

Serves: CAP-DSK-036, CAP-DSK-038 (link URL in reads), CAP-RGA-057, CAP-STP-007 (link value), CAP-MAC-010 (press on a link).

### scroll

Window `Scroll`: read-only text view `Long Text` (2000 lines with emoji and accented letters, about 140,000 characters, ending in `END OF TEXT`) and scroll view `Long List` (buttons `Item 001` to `Item 200`, then `Bottom Button`, out of view at first).

State: `controls.long-text-length`, `long-text-scroll-y`, `long-list-scroll-y`, `presses.bottom-button`, `events.scrolls` (Long List wheel events).

Serves: CAP-DSK-038, CAP-RGA-055, CAP-DSK-045, CAP-RGB-011, CAP-STP-023.

### big-tree

Window `Big Tree`: a scroll view `Big List` with 1400 rows of a `Row NNNN` label and a `Delete` button (ids `delete@NNNN`), then `Final Button`: over 2800 elements, past the 2500-node outline cap.

State: `presses.delete@NNNN`, `presses.final-button`.

Serves: CAP-STP-023 (outline cut short), CAP-DSK-027.

### drag

Window `Drag`: `Drag Pad` with a draggable `Token` and a `Drop Zone` (both accessibility children with frames), and slider `Level`.

State: `controls.token-in-zone`, `token-origin`, `level`; `events.drags`.

Serves: CAP-DSK-006, CAP-DSK-045, CAP-RGB-011, CAP-RGB-022 (background drag), CAP-RGB-049, CAP-DSK-030 (both drag ends), CAP-DMN-017 (confirm before drag).

### click-pad

Window `Click Pad`: a 500x320 pad drawn as a 5x4 grid of labelled cells (A1-E4) recording every press with button, count, modifiers and point, and every scroll.

State: `events.clicks`, `events.scrolls`.

Serves: CAP-DSK-006, CAP-DSK-009, CAP-DSK-011, CAP-DSK-030, CAP-RGB-022 (pointer not in window images), CAP-RGB-024, CAP-RGB-028, CAP-RGB-049, CAP-RGB-054, CAP-RGB-023, CAP-RGB-052, CAP-PLN-026, CAP-FLW-018, CAP-FLW-014, CAP-VIS-005, CAP-VIS-006, CAP-VIS-008, CAP-VIS-013, CAP-VIS-021.

### keys

Window `Keys`: `Key Catcher` (first responder, records raw key events including key equivalents), field `Echo Field`, a disabled `Disabled Shortcut` button with cmd+d, and `Shortcut Target` with cmd+k.

State: `events.keys`, `controls.echo`, `presses.shortcut-target`, `presses.disabled-shortcut` (must stay 0), `commands` (menu shortcuts).

Serves: CAP-MAC-014, CAP-MAC-015, CAP-RGA-017, CAP-RGA-044, CAP-VIS-016, CAP-VIS-009 (key to the front window), CAP-DSK-012 (confirmed background key).

### busy

Window `Busy`: stepper `Freeze Seconds` (1-30, 5), `Freeze Now` (blocks the main thread for that long from inside its action, so an accessibility press on it does not return in time), field `After Freeze`. The socket's `freeze` op does the same without input.

State: `freezes`, `controls.freeze-seconds`, `controls.after-freeze`; during a freeze `state` answers busy.

Serves: CAP-RGA-058, CAP-RGB-029, CAP-DSK-001 (stale windows), CAP-RGB-053 (press not answered in 500 ms), CAP-MAC-012, CAP-DMN-015.

### web

Window `Web Form`: a WKWebView with a local page at `https://fixture.example/app/form`: inputs `email` and `web-password`, two `Save` buttons (`save-top`, `save-bottom`), link `Docs` (recorded, not followed), `Go to Login` (pushes `/app/login` and shows a login section, like a redirect), textarea `Notes`, and a 120-row table of same-named `Delete` buttons with DOM ids `delete-1` ... `delete-120`.

State: `controls.web` (`path`, `clicks` by DOM id, `inputs` by DOM id), `controls.web-loaded`.

Serves: CAP-STP-029, CAP-STP-030, CAP-DSK-038 (page URL and DOM id), CAP-RGA-015, CAP-RGA-026, CAP-RGB-083 (address and DOM-id half; the Chromium switch needs Chrome), CAP-RGB-081 (if the backend treats a WebKit view as a browser).

### spaces

Windows `Space Twin` (`space-here`, on the desktop), `Space Twin` (`space-away`, full screen, its own Space) and `Away Only` (full screen). The reset moves the two away windows into full screen one after the other, then fronts `space-here`, which switches back to the desktop Space; it is ready when both are full screen and `space-here` is on the active Space. Each window has `Space Field` and `Space Button` (`space-field@<window>`, `space-button@<window>`).

State: `windows[].on_active_space`, `windows[].fullscreen`, per-window field values and presses.

Serves: CAP-MAC-002, CAP-DSK-002, CAP-DSK-003, CAP-DSK-004 (other Space), CAP-RGB-034, CAP-RGB-045 (twins across Spaces), CAP-RGA-054, CAP-RGB-036 (off-Space lookup half).

### cover

Window `Covered Target` (pad `Target Pad`, button `Under Cover`, field `Covered Field`); window `Same App Cover` (a small pad of the same app over its left edge); and `Other App Cover`, a floating window of a second app, `CUACover` (bundled at `Contents/Helpers/CUACover.app`, bundle id `com.runta.fennec.cua-fixture.cover`), over the right part of the target pad. The helper reports its clicks to the fixture; the scene is ready once its window shows.

State: `events.clicks` (pads `Target Pad`, `Same App Cover`), `events.cover_clicks` (the other app), `presses.under-cover`, `controls.covered-field`, `facts.cover_ready`.

Serves: CAP-DSK-022, CAP-DSK-024, CAP-DSK-039, CAP-RGB-042, CAP-RGB-054, CAP-RGB-056, CAP-RGA-048, CAP-RGA-049, CAP-RGB-051, CAP-DSK-030 (named input gated on its target app), CAP-DSK-031 (blackout of CUACover under an allowlist of CUAFixture), CAP-MAC-010 (covered control), CAP-PLN-026, CAP-RGB-023.

### displays

Window `Main Display Probe` on the main display and, when there is a second display, `Other Display Probe` on it (preferring one left of or above the main display, at negative coordinates). Each has `Probe Button` and a click pad.

State: `controls.screens` (frames and scales), `windows[].screen`, `presses.probe@main`, `presses.probe@other`, `events.clicks`.

Serves: CAP-DSK-005, CAP-DSK-008, CAP-RGB-046, CAP-RGA-037, CAP-VIS-008, CAP-RGB-024. Needs a VM with two displays for the second-display half.

### launch

Windows `Launcher Home` (field `Launcher Field`), `Calculator Results` and `TextEdit Notes` (fixture windows whose titles name other apps). The app handles the `cua-fixture:` URL scheme and `.cuafixture` documents, so `launch CUAFixture` with a URL or file hands it to the running instance (`launch.opened`); an option argument starts a new instance (`launch.instances`), which serves on the default socket only if the first one does not.

State: `launch.opened`, `launch.instances`, `launch.args`, `controls.args`, `active`.

Serves: CAP-DSK-010, CAP-DSK-033, CAP-DSK-034, CAP-RGB-018, CAP-RGB-072, CAP-RGB-089, CAP-RGA-014, CAP-RGA-038, CAP-RGA-081, CAP-STP-032, CAP-PLN-001, CAP-PLN-002, CAP-PLN-003.

### empty

No windows; the app stays running and frontmost.

State: `windows` is empty.

Serves: CAP-STP-020, CAP-VIS-009 (no app window), CAP-DSK-039 and CAP-RGB-050 (point over no app window).

## Oracle capabilities without a fixture scene

These need a real app, system state or user input that the fixture cannot provide. They belong to T4 (real apps) or to the VM harness.

| Capability | Why not here |
|---|---|
| CAP-PLN-016, CAP-PLN-017, CAP-PLN-018, CAP-RGA-004, CAP-RGA-005, CAP-RGA-011; terminal halves of CAP-PLN-019, CAP-RGA-003, CAP-RGA-006, CAP-RGA-008, CAP-DSK-015 | Terminal echo handling applies only to recognised terminal emulators (Terminal, iTerm2, kitty, ...), identified by app; the fixture would be treated as an ordinary window. T4 Terminal. |
| CAP-RGB-036 (kitty half), CAP-RGA-013 | kitty value writes and Chromium/Electron typing pace need those apps. |
| CAP-STP-031, CAP-RGB-082, Chromium half of CAP-RGB-083 | The Chromium web-tree switch and browser recording need Chrome or Safari. T4. |
| CAP-RGA-019 (Safari address bar half) | Needs Safari. |
| CAP-RGA-018 | macOS security and consent prompts cannot be faked by an app. |
| CAP-RGB-065, lock half of CAP-RGB-022 | Need a locked screen. VM harness. |
| CAP-RGB-064 (asleep display half) | Needs a sleeping display. VM harness. |
| CAP-RGB-084 | Needs real user mouse or keyboard activity during foreground input; any scene (click-pad) works as the target once the harness can produce it. |
