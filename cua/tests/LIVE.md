# Live findings

Results of runs against real apps in the tart VM (`go run ./cmd/cua-vm`), newest first. "Compared" entries ran the same scenario through shuttle v0.1.50 installed in the same VM.

## Scenario suite (T2/T3/T4)

`internal/scenario` (build tag `scenario`) is the live suite: 106 tests, one per `scenario/<Name>` and `internal/scenario.Test<Name>` the catalog plans. It runs inside the VM:

```sh
go run ./cmd/cua-vm test -tags scenario ./internal/scenario -- -test.timeout 3h
go run ./cmd/cua-vm exec -- cat /Users/admin/cua/scenario-report.json   # the report
```

- Each test resets CUAFixture (or prepares Terminal, TextEdit, Safari or System Settings), drives fennec-cua (`fennec-cua serve` with a scratch config, dialled over MCP) and shuttle (`shuttle serve` plus `shuttle mcp` with a scratch `XDG_CONFIG_HOME`), one backend at a time: the running one goes first, so a comparison costs one daemon switch. A config change (input mode, allowlist) restarts the daemon.
- Outcomes are judged from the fixture's true state, from files a Terminal script writes, or from accessibility reads the harness makes itself (the test binary holds the grants). `internal/parity.Judge` compares the two sides; a check must hold for fennec-cua, shuttle's verdict is recorded.
- `-scenario.backends fennec|shuttle` runs one side; `-scenario.report PATH` moves the report. The report keeps earlier entries, so a run of some tests updates theirs.
- Harness hygiene learned the hard way: wait 1.2 s of input idleness before each side (a fresh daemon takes the other backend's input for the user's and yields); cancel any menu the fixture shows (a menu left open swallows every later press and hides the focused window); wait for AX to list a scene's windows (about 300 ms after the fixture says ready); bring the fixture forward through `AXFrontmost` when another app kept the front; answer the VM's one-time "tart-guest-agent ... bypass the private window picker" alert (every process the harness starts is attributed to the guest agent); register the fixture with LaunchServices and link it into `~/Applications` (cua-driver launches apps by name from there); end a Terminal script's whole tty; kill the authorization prompt with sudo.

Final run (both backends, 2026-11-13, 106 tests): fennec-cua passes 76, fails 13, skips 17. Of the 82 with a shuttle side, both pass 50, fennec-cua alone passes 19, shuttle alone passes 2 (`SystemSettingsSidebarClick`, `TypePathIntoSavePanel`), both fail 11; 7 are fennec-cua only (spec capabilities, or no shuttle MCP surface).

## Open

- **System Settings' sidebar ignores fennec-cua's window-targeted click** (`TestSystemSettingsSidebarClick`). An unnamed foreground point click goes to cua-driver as a click in the window's own pixels (`pid`, `window_id`); the SwiftUI sidebar does not switch panes, while shuttle's screen click (`scope: desktop`, pointer moved) does. CAP-RGB-054 asks for a real pointer click. Changing `foregroundPointClick` to a desktop click contradicts three unit tests (`TestClickTargetsTopWindow`, `TestClickDesktopFallbacks`, `TestNegativeCoordinates`) that encode the window-targeted design, so it is left for review.
- **A background element click activates the fixture** (`TestBackgroundTypeNoFocusSteal`): fennec-cua types "hello" into Note, but CUAFixture ends active. shuttle leaves it inactive and types nothing. Not yet traced to fennec-cua or the driver.
- **The save panel opened late by shift+cmd+s is not taken for a chooser** (`TestTypePathIntoSavePanel`, `TestSaveOverwriteConfirmNoSecondReturn`): the typed name goes in after the selected base name ("report-1.txt.txt"). Two causes were fixed on fakes (the "Save As:" label is static text whose words are its value; a panel shown as a sheet sits in its document window's tree), but the live run still misses it; the remaining suspect is the plan's kept pre-step read of the window (`vet.go`), which a fresh read now replaces only inside `chooserType`.
- **A late validation alert is reported only sometimes** (`TestLateDialogReportedAtEnd`): the alert opens 0.7 s after Return and AX lists it about 300 ms later, at the edge of the 1 s end-of-plan wait. shuttle never reports it.

## Compared with shuttle

- **Terminal partial lines: fennec-cua stops, shuttle runs them.** A screen that shows only "abc" of "abcdef" (`TestTerminalPartialType`) and a repeated command that lands as "git pu" over a "git push" already in scrollback (`TestTerminalRepeatCommandPartial`): shuttle sends the Return both times; fennec-cua stops before it.
- **Secret on an echoing prompt:** both withhold the Return; fennec-cua answers `not_shown`, shuttle a free-text failure.
- **Floating cover:** a foreground click naming "Covered Target" at a point another app's floating panel covers: both now click the target behind (`TestFloatingPanelCoversPoint`). shuttle refuses the same click with `window` (`target_ambiguous`), fennec-cua sends it to the target in the background (`TestWindowClickCoveredRefused`).
- **Hung app (`TestHungAppStaleWindow`):** both list the frozen window as stale without waiting.
- **Allowlist blackout (`TestAllowlistBlackout`): shuttle leaks, fennec-cua masks.** With `CUAFixture` allowed, shuttle's screen capture shows the CUACover helper's window (pixel 241,241,241); fennec-cua paints it black.
- **Authorization prompt (`TestSecurityPromptRefusal`): shuttle sends the click** onto the SecurityAgent dialog; fennec-cua sends nothing (it refuses with `focus_mismatch`, not yet `security_prompt`, because accessibility does not hit-test the prompt's window).
- **Close of a sheet without a close button (`TestCloseDialogStaysOpen`):** fennec-cua reports the sheet still open; shuttle closes it (by Escape) and passes.
- **Typing with a sheet holding the focus (`TestTypeFocusHeldBySheet`):** fennec-cua stops; shuttle's sign-in did not complete and its type went into Username anyway.
- **Background typing into a field after a click elsewhere (`TestBackgroundTypeWrongFocusRefused`):** both refuse now.
- **Late and empty dialogs:** where a step's target is missing, shuttle asks its decision model; with no network in the VM each such step costs it 65 s before failing (`TestSlowDialogFill`, `TestPressRefusedFallsBackVerified`). fennec-cua waits for the window and needs no model.
- **Launch by name:** shuttle's `app` step fails in the VM with `bring_to_front ... not verified as the frontmost process's focused window` (`TestLaunchFrontsWindow`, `TestFastRelaunchFronts`, `TestKeyByAppOnly`); fennec-cua's launch takes the running fixture's window.
- **Recording (`TestRecordTextEditDraft`):** with the harness posting a user's click and keys at the HID level, both drafts hold the TextEdit app step, the click and the typed run. `TestRecordSafariPage` passed on both in an earlier run and failed in the final one on the harness side (Safari's page tree had not loaded the Activate button when the harness looked for it).
- **What we learned from shuttle:** a macOS popup button whose menu a synthetic press cannot keep open is picked by focusing it and typing the option's name (type-select). fennec-cua does this when an opened popup lists no options.
- **Form plan (`form` scene): fennec-cua 3/3, shuttle fails.** shuttle changed Color to Blue but read the popup back after 400 ms, before the value updated. fennec-cua reads back for up to 1.5 s.

## Environment limits (skipped or failing for both)

- Skipped with a reason in the report: a second display (`ClickOnNegativeOriginDisplay`, `WindowShotSecondDisplay`, `PointWithoutWindowSecondDisplay`), Qt apps (`QtWindowForegroundClick`, `BackgroundClickFocusesQtField`, `QtBackgroundTypeKeepsField`), Chrome (`LongChromeTitleTarget`, `LaunchChromeProfileArgs`, `ChromeBackgroundType620`, `RecordChromePage`), kitty (`KittyBackgroundType`), a grounding model (`GroundUnderOtherWindowRefused`), a locked screen (`LockedScreenType`), Fennec.app's confirm dialog and packaging (`ConfirmRestoresFront`, `FirstRunCaptureNoRestart`, `PermissionGuideNaming`), the driver as active app (`KeyAfterDriverActive`). The VM has no network, so nothing can be fetched into it; Chrome's dmg could be pushed from the host.
- **Background input in this VM:** cua-driver delivers no background click to the fixture's AppKit views (`TestBackgroundClickWindowLocation`: neither backend lands; fennec-cua reports it unconfirmed, shuttle reports success), refuses input to a window on another Space (`TestAllowlistOtherSpaceType`, both: `off_space_or_ax_unresolved`), refuses a background cmd+a in a save sheet that is not key (`TestBackgroundPlanSaveSheet`, both). A background line into the slow scene's single-line field (`TestBackgroundTypeReturnSingleLine`) passes for fennec-cua; shuttle types nothing.
- **macOS reports success for refused AX actions:** the fixture's Stubborn button refuses its press inside the app while `AXPress` returns success, so neither backend can see the refusal.
- **Key equivalents of buttons are not in AX** (`TestKeyEquivalentDisabledControl`): AppKit exposes no shortcut on a disabled button, so neither backend can refuse cmd+d before sending.
- **The runModal alert is not the AX front window** (`TestWaitAlertStops`): a wait for "Dialogs" passes on both while the app-modal alert is up.
- **Activation:** apps opened through LaunchServices from the guest agent's session do not come to the front; Safari launches (`TestLaunchSafariWithURLCold`, `TestLaunchSafariWithURLRunning`) show the page in one process but leave the previous app in front, for both backends.

## Fixed

Found by the scenario suite, each with a unit test on fakes:

- **Driver replies were read from the wrong level** (`internal/driver.Payload`, `TestPayload`, `actions.TestParseReplyStructured`). The real cua-driver answers MCP-style with its fields under `structuredContent`; actions, the launch step and the hit-checked click decoded the top level and saw nothing: every hit-checked click was refused as "not in the driver's snapshot", every unverifiable reply read as confirmed.
- **Driver errors counted as success** (`cmd/fennec-cua`: `ToolErrors: true`; `actions.classify` reads `isError`; `TestWindowClickReadsContextFirst`). A window-pixel click the driver refused with `screenshot_context_missing` was reported done while nothing was clicked. The refusal now counts as nothing sent; the window is read (`get_window_state`) and the input sent once more.
- **Menus opened for accessibility stayed open** (`plan/ops.go`, `TestMenuClosedByCancel`). Escape did not reach them; an open menu swallowed every later press and left "no focused window" for the rest of the run. Menus now close with their own `AXCancel` (Escape only as a fallback), and the entry is pressed with its menus closed.
- **Menu check marks were never read** (`desk/tree.go` reads `AXMenuItemMarkChar`; `view.MenuMarkStates`, `TestMenuMarkStates`): menu items carry no `AXValue`.
- **A toggle's read-back read the stale mark** (`plan/ops.go`, `TestMenuToggleReadsBackInReopenedMenu`): the mark is read again in the reopened (validated) menu, for up to 1 s.
- **A menu step did not wait for a front window**, and **an auto plan refused a menu step without a plan window** (`plan/step.go`, `TestAutoMenuWithoutPlanWindow`); the step now reports `input: foreground`.
- **A dialog that fills after showing gave up after 2 s** (`plan/target.go`, `TestLateDialogGetsItsOwnWait`): a window that comes to the front gets a wait of its own.
- **Field clicks stayed unconfirmed** (`actions.focusLands`, `TestFieldClickConfirmedByFocus`): the focus moving into the clicked field proves the click; a confirmed type into the field proves a background click before it (`plan.readBack`, `TestConfirmedTypeProvesClick`).
- **Unproven foreground steps failed whole plans** (`plan.openNotes`). Only background input left unconfirmed stops a plan, as in shuttle; a foreground step the driver could not confirm stays `unconfirmed` (retry false) and is named in `caveats`.
- **A press that opens an app-modal alert failed the plan** as `app_not_responding` (`actions.axFailure`, `TestPressIntoModalCountsAsDone`); it now counts as done, unproven, never followed by a click.
- **Terminal: a repeated command that landed in part got its Return** (`plan/typing.go`, `TestTerminalRepeatedLineNeedsNewCopy`): the driver confirms a line it finds anywhere on screen; with both screens read only a new copy counts.
- **Save panel as chooser** (`plan/chooser.go`, `TestChooserLabelInValue`, `TestChooserAsSheetInWindow`, `TestLateSheetPanelAfterShortcut`): the label is static text whose words are its value; a sheet in the window's tree counts; a stale kept read is read again. Still open live, see above.
- **Slider and stepper values** (`ax.SetNumber`, `desk.SetValue` writes a number to a numeric `AXValue`, `view.NumberOf`, `TestNumberOf`; `plan.stepTo`, `TestSliderSteppedWhenWriteRefused`): a string write was refused with -25201; a control that takes no written value is walked with `AXIncrement`/`AXDecrement`.
- **Link URLs** (`desk.urlOf`, `TestURLOfString`): AppKit text links give `AXURL` as a string. **Page addresses** (`desk/tree.go`): a web area's `AXURL` is the page address the web check reads; it was never filled.
- **Hit test inside the asked window** (`view.PointInWindow`, `desk.AtIn`, `TestPointInAskedWindow`): observe at a point with a window answered the covering app's element and named the asked window as covering it.
- **Floating panels did not count as covers** (`desk.coverAt` takes layers below the Dock's; `actions.namedPointClick` consults the system-wide hit, `TestNamedClickUnderFloatingPanel`): a foreground click naming a window landed on another app's floating window on top.
- **The hit window had no app name** (`desk.appName`), so the security-prompt check could not match it.
- **Allowlist blackout was never applied** (`cmd/fennec-cua/mask.go`, `TestMaskImage`): screen and region captures are now masked by the window server's list (which holds accessory apps' and floating windows) taken before and after the capture; vision screenshots are refused under an allowlist.
- **Background twin titles** (`plan.twinTitles`, `TestBackgroundTwinTitlesRefused`; `actions` cross-Space twins, `TestTitleTwinsAcrossSpacesRefused` updated): a title two shown windows share, or two windows across Spaces, is refused for background input, as shuttle does.
- **A background point click was not remembered** (`actions.backgroundPointClick`, `TestBackgroundPointClickThenTypeChecksFocus`), so a type after a click elsewhere went into the old field.
- **Password fields read back as bullets** (`actions.masked`, `TestMasked`) count by length; a slow focus read (Safari) is retried within the read-back budget instead of failing at once.
- **Popup buttons did not open under a synthetic press.** On macOS 26.6.2 the fixture's `NSPopUpButton` sometimes stays closed whichever way it is pressed (driver click, `AXPress`, `AXShowMenu`). `set` now falls back to type-select when no list shows, and reads the value back until it settles.
- **Secure fields could not be set or checked.** `secret` is accepted on `set`; a secure field's read-back (mask characters) is checked by length and never quoted.
- **A password leaked into the plan-level error** when a secret step failed: the step result was redacted, the copy at the top of the result was not. Every secret of the plan is now scrubbed from the plan's error.
- **Open menus' items counted as hidden.** The desk marks a menu item with a non-empty frame as showing.
- **Options read before the list opened; selector re-resolved onto a label.** `set` polls for options for up to 1 s and presses the element by its ref.
- **cua-driver's own overlay window listed.** Skipped by pid and by name.
- **Ended driver sessions never retried.** The real driver appends text to "session has ended"; the retry matches the message's start.

## Passing

- Live AX reads of the fixture (162-node tree in 176 ms, roles, states, menu bar) and the window list.
- `act` over MCP through the real cua-driver: two presses of `Increment`, the fixture reports both.
