# Live findings

Results of runs against real apps in the tart VM (`go run ./cmd/cua-vm`), newest first. Each open item names the capability it blocks.

## Open

- **Popup buttons do not open (blocks CAP-STP-002 popup half, CAP-RGB-023).** On macOS 26.6.2, pressing the fixture's `Color` popup (`NSPopUpButton`, AX role `AXPopUpButton`) leaves it closed, whichever way it is pressed: cua-driver's synthetic click at its centre, `AXPress` and `AXShowMenu` all return success, and a screenshot 2 s later shows the menu closed. A `set` on it therefore stops with "has no option". Next: check how cua-driver posts the click (a popup needs its mouse-down to reach the window server, not only the target process), compare with shuttle on the same scene, and try a real mouse-down/up pair through the driver.

## Fixed

- **Open menus' items counted as hidden.** The view treats menu items as hidden unless the backend marks them `showing`; the desk never did, so items of an open popup or menu were invisible to `find` and `set`. The desk now marks a menu item with a non-empty frame as showing (closed menus' items have no frame).
- **Options read before the list opened.** `set` on a popup read its options in the same instant it pressed; it now polls for up to 1 s, and presses the element it resolved (by ref) instead of resolving the selector again, which could hit a label of the same name.
- **cua-driver's own overlay window listed.** The desk now skips the driver's process by pid and by name.
- **Ended driver sessions never retried.** The real driver appends text to "session has ended"; the retry now matches the message's start.

## Passing

- Live AX reads of the fixture (162-node tree in 176 ms, roles, states, menu bar) and the window list.
- `act` over MCP through the real cua-driver: two presses of `Increment`, the fixture reports both.
- A five-step form plan: `set` of a text field and a check box confirmed by read-back; it stops, as it should, at the popup above.
