# Predictive local echo for cloud terminals

Status: design, not implemented. Owner surface: the `fen` attach client on the Mac.

## Problem

Every keystroke in a cloud session pays one full round trip before the user
sees it: Mac `fen` → daemon → resident SSH channel → cloud PTY → agent TUI →
herdr re-render → back. On the current link that is 200–300 ms. The transport
adds nothing measurable on top (TCP_NODELAY is set, no output coalescing
timers), so the only way to make typing feel instant is to draw the keystroke
locally before the server confirms it, and to repair the screen when the guess
is wrong.

Goal: printable typing, Backspace and Left/Right in the agent's composer appear
within one local frame (<16 ms) while the screen remains exactly what the
server says once confirmation arrives.

Non-goals for v1: predicting Enter, Shift+Enter, Tab completion, history
navigation, paste, or anything that changes layout.

## What the agents actually emit

Recorded on the cloud runtime with `script --log-timing`, 100×30,
`TERM=xterm-256color`.

Claude Code 2.1.325:

- Runs on the alternate screen (`CSI ?1049h`), enables SGR any-event mouse
  tracking (`?1003h ?1006h`), bracketed paste, focus reports, and pushes Kitty
  keyboard flags 5 (`CSI >5u`: disambiguate + alternate keys). Printable keys
  still arrive as plain UTF-8.
- Uses the real hardware cursor for the composer. Every frame is wrapped in
  synchronized output and ends by placing and showing the cursor:
  `CSI ?2026h CSI ?25l … CSI 27;4H CSI ?25h CSI ?2026l`.
- One typed character produces a ~56-byte diff that writes exactly that cell,
  10–30 ms after the PTY write when idle:
  `CSI H CR CSI 3C CSI 26B e CSI 30;1H CSI 27;5H CSI ?25h`.
- Backspace: move to the cell, `CSI K` (erase to end of line), cursor back one.
- Left arrow: a cursor-only frame (`CSI 27;3H`).
- Inserting `Z` before `x` rewrites `Zx` — standard insert semantics.
- Shift+Enter (`CSI 13;2u`) grows the composer upward: the existing line moves
  up one row, the cursor stays on the bottom row at the indent column, and the
  border and hint rows above are redrawn (450 bytes). Bottom-anchored layout.
- Dialogs (folder trust) hide the cursor for their whole lifetime.

Codex 0.169: blocks on a cursor position query (`CSI 6n`) before reading input,
so an unattached `script` capture could not exercise its composer. It must be
recorded through the real `fen` path, where `DisplayResponder` answers the
query (Phase 0). Its renderer (ratatui) also positions the hardware cursor in
the composer, which is the property this design depends on.

herdr-core sits between the agent and `fen` and re-renders the pane. The
client therefore must not depend on the app's exact byte patterns, only on the
resulting screen state. This is another reason to validate against an emulated
screen rather than matching echoed bytes.

## Mosh: what to keep and what to drop

Mosh's `PredictionEngine` (`src/frontend/terminaloverlay.cc`) is the reference
design. It keeps the server's framebuffer locally, records each prediction as a
conditional overlay on a cell or the cursor, and on every server frame
classifies each overlay as `Pending`, `Correct`, `CorrectNoCredit` or
`IncorrectOrExpired`.

Keep:

1. **Validate against screen state, not echoed bytes.** A prediction is a
   claim about a cell's contents; the shadow screen decides. This is robust to
   herdr re-rendering and to any diffing strategy.
2. **Epochs.** Any keystroke that is not predictable (control keys, Enter,
   Escape, unknown CSI) starts a new epoch. Predictions in an unconfirmed epoch
   are recorded but not shown; the first correct prediction confirms the epoch
   and reveals everything recorded in it. This is what makes password prompts,
   menus and vim normal mode safe: if the app never echoes, nothing is shown.
3. **No credit for coincidence.** A cell that already held the predicted
   character does not confirm an epoch.
4. **Kill the epoch on a misprediction**, and reset entirely when the
   predicted cursor is wrong.
5. **Insert-mode line model.** Typing shifts the rest of the row right;
   Backspace shifts it left; Left/Right move the cursor. This matches both
   observed composers.
6. **Never predict in the last column**, across scroll, or across a resize.

Drop or change:

1. **Fixed 50 ms server echo timeout.** Mosh's server declares input "late
   acked" 50 ms after processing it; if the app has not echoed by then, the
   prediction counts as wrong. Node TUIs under load (Claude Code while
   streaming) routinely exceed 50 ms, which would cause flicker. We instead
   treat *unchanged* cells as still pending until a deadline (1.5 s), and only
   reject when the cell shows something that is neither the original nor the
   prediction. A server signal can tighten this later (Phase 2).
2. **Underlining unconfirmed predictions.** Mosh underlines whenever SRTT
   exceeds 80 ms. On our link that is every prediction, permanently. We draw
   predictions in the neighbouring cell's style with no decoration, and only
   flag (underline) a prediction that has been pending past 1 s.
3. **Owning the whole display.** Mosh redraws the local terminal from its own
   framebuffer. That would drop everything our emulator does not model:
   hyperlinks (OSC 8), notifications (OSC 9/777), OSC 52, Kitty keyboard and
   mouse mode toggles, images, title updates. We keep byte passthrough and
   paint small, self-contained patches on top (see Rendering).
4. **Wide characters.** Mosh skips anything with `wcwidth != 1` (`XXX handle
   wide characters`). Committed IME text (Chinese, Japanese, Korean) must be
   predicted. We predict East Asian Wide/Fullwidth code points as two-cell
   glyphs and treat ambiguous-width, emoji, ZWJ and combining sequences as
   unpredictable.
5. **Every non-printable input starts an epoch.** Claude Code enables
   any-event mouse tracking, so moving the mouse floods input with SGR mouse
   reports. Under mosh's rule that would keep predictions permanently
   tentative. Mouse reports, focus reports and Kitty key-release events are
   neutral: they neither predict nor start an epoch.
6. **Context awareness.** Mosh ignores what the server does between
   keystrokes. We additionally start a new epoch when the server hides the
   cursor, moves it to a different row while no prediction is pending, switches
   screens, scrolls, or changes the scroll region. This catches dialogs and
   layout changes that appear without user input.

## Architecture

All of this runs in the Mac `fen` process. Phase 1 needs no protocol or
cloud change.

```
stdin ─► terminalinput.Decoder ─► predict.Engine.Input ─► sendInput (unchanged)
                                        │ patch bytes
                                        ▼
remote "data" frames ─► predict.Engine.Output ─► SessionTitleWriter ─► stdout
                         │ shadow emulator (server truth)
                         └ overlay set (cells + cursor, epoch, deadline)
```

- `internal/predict` (new package): `Engine`, overlay bookkeeping, input
  classifier, rendering of patches. Pure, deterministic, clock injected.
- Shadow emulator: a VT model fed every byte the server sends. Needs CUP and
  relative moves, ED/EL/ECH/ICH/DCH, SGR including truecolor, DECSTBM, IND/RI,
  autowrap with pending-wrap state, DECOM, IRM, alternate screen, DECTCEM,
  `?2026` synchronized output, G0/SI/SO, wide cells. Phase 0 decides between
  `github.com/charmbracelet/x/vt` and an in-tree emulator limited to this list;
  the selection test is the recorded traces replayed to identical cell grids.
- A single mutex orders the input path's patch writes with the output path's
  passthrough writes, since they run on different goroutines.

Integration points: `internal/launcher/stream_terminal.go` (`sendInput` and
the `case "data"` write), and `streamOwner.pumpInput` for the reconnecting
path. The browser terminal and the native macOS terminal are out of scope for
v1.

## Prediction rules

Input classification, after `terminalinput.Decoder` has joined escape
sequences:

| Input | Action |
| --- | --- |
| Printable code point, width 1 | Insert prediction at cursor; cursor +1 |
| Code point with East Asian width W/F | Two-cell insert prediction; cursor +2 |
| Kitty `CSI codepoint[;mods] u` that maps to text (shift only) | Same as the printed character |
| `DEL` / `BS` | Delete-left prediction; cursor −1 (no-op at composer start) |
| `CSI C` / `CSI D` (no modifiers) | Cursor-only prediction ±1 |
| SGR mouse report, focus in/out, Kitty key release | Neutral |
| Bracketed paste, Enter, Shift+Enter, Tab, Esc, Ctrl/Alt chords, Up/Down, anything else | New epoch, no prediction |

Display preconditions, all on the shadow screen; if any fails the keystroke is
recorded tentative or starts a new epoch:

- Cursor visible, not in pending-wrap state, origin mode off.
- Not inside a synchronized-update block the server has opened but not closed.
- The prediction stays at least two columns from the right edge (composer
  wrapping is app-specific).
- The epoch is confirmed.

Validation, run after each server chunk once the shadow parser is back in the
ground state and outside `?2026h…l`:

| Shadow cell vs. prediction | Result |
| --- | --- |
| Equals prediction, differs from every original | Correct: confirm epoch, retire overlay, record latency |
| Equals prediction and an original | Correct without credit: retire overlay |
| Still equals the original contents | Pending until the 1.5 s deadline, then expire |
| Anything else | Incorrect: kill the epoch, repaint truth |

The cursor prediction is validated the same way: pending while the shadow
cursor sits where it was when the prediction was made; incorrect if it lands
anywhere other than the predicted position.

## Rendering

Server bytes are forwarded unchanged. The engine emits extra bytes only at
safe points and always returns the terminal to exactly the state the server
believes it is in.

- **Paint**: `CSI ?2026h`, then for each displayed overlay cell a CUP, the SGR
  needed for that cell (copied from the left neighbour, per mosh), and the
  glyph; finally CUP to the predicted cursor, then `CSI ?2026l`.
- **Before forwarding server bytes**: if anything is painted, first restore the
  cursor and SGR pen to the shadow emulator's values (explicit CUP + SGR; never
  DECSC/DECRC, which the app may use). Painted cells stay on screen; the
  server's next frame either overwrites them (confirmation or correction) or
  leaves them for validation.
- **After a server chunk**: validate; for incorrect or expired overlays,
  repaint the true cell from the shadow grid; repaint still-pending overlays
  the server just overwrote with older state (for example a frame produced
  before the latest keystroke arrived); place the cursor at the predicted
  position if any overlay is still pending.
- Never inject between bytes of an escape sequence or inside a server
  synchronized block; defer to the next safe point.
- Resize, alternate-screen switch, scroll, or a scroll-region change discards
  all overlays and repaints their cells from the shadow grid.

Invariant (tested): whenever no overlay is outstanding, the user's screen is
cell-for-cell identical to what pure passthrough would have produced.

## Enablement

- Setting `terminal.predictive_echo`: `adaptive` (default), `always`, `off`.
  Environment override `FEN_PREDICTIVE_ECHO` for debugging.
- `adaptive`: enabled while the smoothed keystroke-to-confirmation time exceeds
  30 ms, disabled below 20 ms with no overlays active (mosh's hysteresis). The
  confirmation time is measured from the engine's own Correct events, so no
  separate probe is needed.
- `FEN_PREDICTIVE_ECHO=underline` draws every displayed prediction underlined
  for manual verification.
- Counters (predicted, displayed, confirmed, mispredicted, expired, p50/p95
  confirmation latency) go to `launchprofile` for diagnostics; never to product
  UI.

## Phases

**Phase 0: evidence and emulator choice (1–2 days)**

- Add an opt-in trace recorder in `fen` (input and output with timestamps,
  local file, off by default) and capture Claude Code, Codex, Kimi and Pi
  through the real herdr path: typing ASCII, IME text, backspace, arrows,
  Shift+Enter, slash menu, permission dialog, typing while the agent streams.
- Pick the shadow emulator: replaying every trace must reproduce the same cell
  grid as a reference terminal.

**Phase 1: client-only engine (about 1 week)**

- `internal/predict` with the rules above, wired into the attach client.
- Tests: golden replays of the Phase 0 traces with injected 250 ms latency
  (assert which keystrokes displayed immediately and the final screen);
  randomized property test for the convergence invariant; unit tests for
  epochs, the neutral input classes, wide characters, and safe-point
  injection.
- Acceptance on the Mac against a real session: the user types into Claude
  Code and Codex with ≥95% of composer keystrokes displayed locally, zero
  residual artifacts after confirmation, and no prediction shown in dialogs or
  password prompts.

**Phase 2: tighter confirmation and layout prediction**

- Server `echo_ack` capability (protocol version bump): after writing input
  sequence N to the PTY, the cloud service emits `echo_ack N` once the app has
  produced output and gone idle for 15 ms, or after 250 ms with no output. "App
  answered without echoing" then rejects in one RTT instead of waiting for the
  1.5 s deadline.
- Context memory: once the composer row and prompt prefix have been confirmed,
  a new epoch that starts on the same row with the same prefix is confirmed
  immediately, so the first character after Backspace-to-empty or a cursor jump
  is also instant.
- Experimental Shift+Enter prediction for the bottom-anchored composer
  observed in Claude Code, behind its own flag, enabled only if Phase 0 traces
  show a stable pattern across agents.

## Risks

- Width disagreement between our table and the app's string-width library for
  rare code points. Mitigated by predicting only unambiguous W/F ranges;
  anything else starts an epoch.
- Apps that move the hardware cursor away from the composer while idle would
  disable prediction (safe failure, not corruption).
- Terminals without synchronized output may show brief tearing during patch
  painting; patches are a few cells and the bytes are written in one `write`.
- herdr behaviour under resize and reattach needs trace coverage; the engine
  discards overlays on any resize.
