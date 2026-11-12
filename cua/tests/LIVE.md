# Live findings

Results of runs against real apps in the tart VM (`go run ./cmd/cua-vm`), newest first. "Compared" entries ran the same scenario through shuttle v0.1.50 installed in the same VM.

## Open

- (none)

## Compared with shuttle

- **Form plan (`form` scene): fennec-cua 3/3, shuttle fails.** Steps: set Name, check Subscribe, pick Blue in the Color popup, set the password, Submit with an expect. shuttle changed Color to Blue (the fixture confirms) but reported the step failed: it read the popup back after 400 ms, before the value updated. fennec-cua reads back for up to 1.5 s.
- **What we learned from shuttle:** a macOS popup button whose menu a synthetic press cannot keep open is picked by focusing it and typing the option's name (type-select). fennec-cua does this when an opened popup lists no options.

## Fixed

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
