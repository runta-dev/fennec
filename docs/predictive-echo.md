# Composer-aware predictive echo for cloud terminals

Status: design, not implemented. Surface: the `fen` attach client on the Mac,
plus one small capability in the cloud terminal service.

## Problem

Every keystroke in a cloud session pays a full round trip before it appears:
Mac `fen` → daemon → resident SSH channel → cloud PTY → agent TUI → herdr
re-render → back. On the current link that is 200–300 ms. The transport adds
nothing measurable (TCP_NODELAY, no coalescing timers), so the only way to make
typing feel instant is to render the user's edit locally and reconcile with the
server afterwards.

## Scope

This is a vertical feature. Fennec launches a fixed set of agents (Claude
Code, Codex, Kimi, Pi, OpenCode, Cursor, Grok) behind herdr, so the engine
models exactly those renderers, not arbitrary TUIs. Each supported agent gets a
profile written from its actual rendering logic: the source where available
(Codex's ratatui composer and wrapping code, Pi's TypeScript UI) and the
shipped bundle plus recorded traces where it is not (Claude Code's Ink
renderer). Sessions of an agent without a profile, or an agent version whose
self-check fails, run without prediction. There is no generic fallback
predictor, and the shadow emulator only needs to cover what herdr emits for
these agents.

## Core idea

The engine's unit of prediction is the agent's **composer**: its message input
box. The engine does not guess at terminal cells; it knows that the user is
editing a text buffer, knows how that agent lays the buffer out on screen, and
predicts the next layout of the whole composer.

- **Lock**: find the composer on the server's screen (per-agent profile),
  parse it back into `{text, cursor}`, and confirm the parse by re-rendering it.
  No lock means no prediction.
- **Predict**: apply the user's edits to the locked text model, run the
  agent's layout, and paint the predicted composer, including wrapping,
  growing and shrinking.
- **Reconcile**: on each server frame, re-parse the composer and match it
  against the pending edits, the way networked games do client-side prediction
  with server reconciliation. Confirmed edits are dropped; the rest are replayed
  on the new server state; a divergence rolls the screen back to server truth.

Being composer-aware is what makes the hard cases work. A cell-level predictor
like mosh's cannot handle any of these:

| Case | Cell overlay (mosh) | Composer model |
| --- | --- | --- |
| First character into an empty composer | Shifts the dim placeholder right; wrong | Placeholder belongs to the empty state; the first character replaces it |
| Typing past the right edge | Stops predicting | Word-wraps exactly as the agent does |
| Shift+Enter | Unpredictable | New line; the box grows upward (Claude Code) |
| Backspace at a line start | Unpredictable | Joins lines; the box shrinks |
| First character after Shift+Enter, Backspace-to-empty, Ctrl+C clear | Waits one RTT (new epoch) | Immediate; the state is known |
| Layout moves (popup opens, transcript grows) | All predictions killed | Matched at text level; repainted at the composer's new position |
| Agent output appearing outside the box | Can corrupt overlays | Never touched; the engine paints only inside the composer region |

## What the agents emit (evidence)

Recorded on the cloud runtime with `script --log-timing`, Claude Code
2.1.325, `TERM=xterm-256color`, 100×30 and 40×20.

Screen and protocol:

- Alternate screen; SGR any-event mouse tracking; bracketed paste; focus
  reports; Kitty keyboard flags 5 (printable keys still arrive as UTF-8).
- Every frame is wrapped in `CSI ?2026h … ?2026l` and ends by placing and
  showing the hardware cursor at the composer cursor. Dialogs hide the cursor.
- The agent echoes a character 10–30 ms after the PTY write when idle. A
  single-character frame is about 56 bytes and rewrites only that cell.

Composer layout (the profile is derived from these facts):

```
row k-1  ctrl+g to edit in Vim          (right-aligned, only while the text has a hard newline)
row k    ────────────────────────────── (full width, rgb 136,136,136)
row k+1  ❯␣the quick brown fox jumps over lazy   (❯ U+276F, ␣ = U+00A0)
row k+2    dogs again                           (continuation indent: 2 spaces)
row k+3  ────────────────────────────── (bottom border)
row k+4  footer / status
```

- Text starts at column 3; the text width is `columns − 4` (36 at 40
  columns). Wrapping is by word: typing `d` after `lazy␣` moved `d` to a new
  row instead of splitting the word.
- The composer is bottom-anchored. Growing by one row (soft wrap or
  Shift+Enter) moves the top border and anything above it in the composer chrome
  up one row; the last text row stays on the same screen row. Backspace at a
  line start shrinks it back.
- The hint row (`ctrl+g to edit in Vim`) appeared when a hard newline was
  inserted, not on soft wrap.
- The empty composer shows a dim (SGR 2) placeholder whose text changes
  between sessions (`Try "how do I log an error?"`, `Try "fix typecheck
  errors"`). The first character writes itself and erases to end of line.
- Backspace erases to end of line; Left/Right are cursor-only frames;
  Ctrl+C clears the composer back to the placeholder.
- The renderer may replace runs of spaces with CHA jumps (`CSI 13G`), so the
  screen must be read through an emulator, never by matching bytes.

Still missing, collected in Phase 0: the Codex composer. On this runtime
`codex` opens a command center (task list) before the composer, so it has to be
recorded through a dedicated, labeled test session. Also missing: Claude Code
with a transcript above the composer and while the agent is streaming, the
slash and `@` popups, `!` bash mode, vim mode, IME text and narrow widths.

herdr-core sits between the agent and `fen` and re-renders the pane. Profiles
therefore describe screen state (glyphs, attributes, geometry), never the
agent's byte patterns.

## Lessons taken from mosh

Mosh's `PredictionEngine` (`src/frontend/terminaloverlay.cc`) is the reference
for local echo over a slow link.

Kept:

- **Server state is the only truth**, read from an emulated screen. Mosh
  validates every overlay against its server framebuffer; we validate the
  parsed composer.
- **Hold back until confirmed.** Mosh shows a new epoch's predictions only
  after one of them is confirmed. Our equivalent is the lock: until the
  composer has been parsed and verified, nothing is shown. Password prompts,
  menus and dialogs have no locked composer, so they are safe by construction.
- **No credit for coincidence**: a composer state that already matched before
  the edit does not confirm it.
- **Adaptive enablement with hysteresis** on measured confirmation latency.
- **Repair on divergence**: repaint truth immediately when a prediction is
  refuted.

Dropped or changed:

- **Cell overlays and the insert-mode line model**: replaced by the composer
  text model and the agent's real layout (see the table above).
- **Fixed 50 ms echo timeout**: mosh treats a slow echo as a wrong prediction,
  which flickers whenever a Node TUI is busy. We use an explicit server
  `echo_ack` (below) and treat "nothing changed yet" as pending, not wrong.
- **Underlining above 80 ms RTT**: on our link that would underline
  everything. Predictions render in the composer's real style; only an edit
  pending for more than 1 s is flagged.
- **Owning the display**: mosh redraws the terminal from its own framebuffer
  and would drop OSC 8, OSC 9/777, OSC 52, Kitty protocol toggles and images.
  We pass server bytes through unchanged and paint the composer region on top.
- **Narrow-only text**: mosh skips wide characters. Committed IME text is
  predicted using the agent's width function. Because the cursor is placed at
  the predicted position, the terminal's IME preedit appears where the user
  is typing, not one round trip behind.
- **Mouse reports breaking prediction**: mosh starts a new epoch on every
  non-printable byte, and Claude Code's any-event mouse mode would make that
  constant. Mouse, focus and key-release events are neutral here.

## Architecture

```
stdin ─► terminalinput.Decoder ─► predict.Engine.Input ─► sendInput (+ input seq)
                                        │ composer paint
                                        ▼
remote frames ─► predict.Engine.Output ─► SessionTitleWriter ─► stdout
                  │ shadow emulator (server truth)
                  ├ composer locator + profile
                  ├ confirmed composer state + pending edits
                  └ renderer (passthrough + composer patches)
```

New package `internal/predict`, pure and deterministic with an injected clock.
Integration points: `internal/launcher/stream_terminal.go` (`sendInput` and
the `data` case) and `streamOwner.pumpInput` for the reconnecting path. A single
mutex orders patch writes from the input goroutine with passthrough writes from
the output loop.

### Shadow emulator

Fed every server byte. It needs to cover what herdr emits for the supported
agents, which Phase 0 enumerates from traces (so far: CUP, relative moves, CHA,
EL/ED, SGR with truecolor and dim, DECTCEM, `?2026`, alternate screen, G0/SI,
wide cells, scrolling). Phase 0 chooses between `github.com/charmbracelet/x/vt`
and a small in-tree emulator limited to that set, by replaying traces to
identical grids.

### Composer profiles

A profile is data plus a few small functions, selected by the session's agent
(already known from the launch plan) and versioned by the agent's version
string. A profile defines:

- **Anchors**: how to find the composer: the prompt glyph and its column, the
  continuation indent, the border rows and their style, and the cursor
  constraint (inside the text rows).
- **Empty state**: how the placeholder is drawn (dim text after the prompt) so
  it parses as empty text.
- **Layout**: text column, text width as a function of the terminal width,
  wrap mode (word or character), whitespace handling at soft-wrap points, the
  width function (East Asian Wide/Fullwidth = 2; ambiguous, emoji and ZWJ
  sequences are unpredictable), growth direction (up for Claude Code), maximum
  visible rows before internal scrolling, and chrome rules such as the hint row.
- **Keymap**: which inputs map to which edit operations (below), including
  that agent's Shift+Enter encodings (`CSI 13;2u`, `ESC CR`, `\` + CR).

The profile describes the agent only, not the user's terminal or herdr.

### Lock and parse

After each server frame that ends in the ground state outside a synchronized
block:

1. Locate the anchors in the shadow grid. The cursor must be visible and
   inside the text rows.
2. Parse rows into text. Soft and hard breaks are told apart by running the
   layout: a break is soft exactly when the next row's first word would not have
   fit on the previous row. Hard breaks the engine itself produced (Shift+Enter)
   are also recorded in the edit log.
3. **Self-check**: render the parsed `{text, cursor}` with the profile layout
   and compare it with the grid region cell for cell (glyph, width and
   attributes the profile declares relevant). Only a matching parse yields a
   lock.

A profile that fails its self-check three times in a session is suspended for
that session and logged with the agent version, so a layout change in a new
agent release disables prediction instead of corrupting the screen.

### Edit operations

| Input | Operation |
| --- | --- |
| Printable text, committed IME text | `Insert(text)` |
| Shift+Enter (profile encodings) | `Insert("\n")` |
| Backspace | `DeleteBackward` (joins lines at a line start) |
| Delete | `DeleteForward` |
| Left / Right | Cursor ±1 grapheme |
| Ctrl+A / Ctrl+E, Home / End | Line start / end (if the profile keymap says so) |
| Up / Down inside a multi-line composer | Visual row move; on the first or last row they become barriers (history) |
| Ctrl+U, Ctrl+W, Alt+Backspace | Profile-defined, otherwise barrier |
| Mouse, focus, key release | Neutral |
| Enter, Tab, Esc, paste, other chords | Barrier |

A **barrier** sends the key, keeps everything already painted, and suspends new
predictions until the server's `echo_ack` covers it and the composer re-locks.
Edits typed in the meantime are queued and replayed on the post-barrier state,
so the first character after Enter appears one round trip after the submission
rather than one round trip after itself.

Mode triggers such as `/`, `@`, `!` and `#` at the start of an empty composer
are predicted as ordinary text. Any popups or prompt changes they cause arrive
from the server, and the composer is re-located on the next frame.

### Reconciliation

State: `confirmed` (last verified server composer) and `pending` (edits with
input sequence numbers). The predicted state is `apply(pending, confirmed)`.

- Each input frame carries an implicit sequence: the cumulative input byte
  count the client already tracks for credits.
- New capability `echo_ack_v1` in the cloud terminal service: after writing
  input up to sequence N to the PTY, it emits `echo_ack{seq: N}` on the same
  ordered stream once the agent has produced output and then been idle for 15
  ms, or after 250 ms with no output. Frames are ordered, so when the client
  sees `echo_ack N`, its shadow grid contains the agent's reaction to every key
  up to N, or the agent is busy.
- On each re-lock, find the longest prefix of `pending` whose application to
  `confirmed` equals the server state. Drop that prefix and adopt the server
  state as `confirmed`. This often confirms edits before the ack arrives.
- When `echo_ack N` arrives and the server state matches no prefix that
  includes every edit ≤ N:
  - if the agent produced output after those keys, the prediction diverged:
    drop all pending predictions, repaint truth, count a misprediction;
  - if the ack was the 250 ms no-output case, keep waiting (the agent is busy)
    up to a 1.5 s deadline, then repaint truth.
- Older cloud services without `echo_ack_v1` use prefix matching plus the
  1.5 s deadline only.

### Rendering

Server bytes are forwarded unchanged. The engine adds bytes only at safe points
(shadow parser in the ground state, outside a server `?2026` block) and always
returns the terminal to the state the server believes it is in.

- **Paint** the predicted composer as one synchronized update: compute the
  predicted region (text rows, borders and chrome rows the profile owns,
  shifted by the growth rule), diff it against the shadow grid, write only the
  differing cells with explicit CUP and SGR, then CUP to the predicted cursor.
  One keystroke is typically 30–80 bytes; a reflow or growth is a few hundred.
- **Before forwarding server bytes**, restore the cursor position and SGR pen
  to the shadow emulator's values (explicit CUP and SGR, never DECSC/DECRC,
  which the agent may use).
- **After a server frame**, reconcile and paint again: cells where the
  predicted region still differs from the new server grid are repainted, and
  cells the engine painted earlier that are no longer part of any prediction are
  restored from the shadow grid.
- Growth upward overwrites rows above the composer with shifted copies from
  the shadow grid. Only rows the profile declares as composer chrome or blank
  are ever shifted; if the rows above are transcript content and the profile
  cannot vouch for them, the growth is shown only once the server confirms it.
- Resize, alternate-screen switch, loss of lock or a hidden cursor drop all
  predictions and restore every painted cell from the shadow grid.

Invariant (tested): whenever `pending` is empty, the user's screen is cell for
cell what pure passthrough would have produced.

## Enablement

- Setting `terminal.predictive_echo`: `adaptive` (default), `always`, `off`.
  Environment override `FEN_PREDICTIVE_ECHO` for debugging; `underline` draws
  every predicted cell underlined for manual verification.
- `adaptive` shows predictions while the smoothed confirmation latency is above
  30 ms and stops below 20 ms when nothing is pending.
- Counters (edits predicted, confirmed, diverged, expired, barrier waits,
  self-check failures per profile version, p50/p95 confirmation latency) go to
  `launchprofile` diagnostics, never to product UI.

## Phases

**Phase 0: traces and emulator (2–3 days)**

- Read each target agent's composer rendering code: the text buffer, wrapping
  (word boundaries, whitespace at wrap points, width function), prompt and
  continuation glyphs, placeholder, growth and scroll limits, and key bindings.
  The profile is written from this code; traces verify it.
- Opt-in trace recorder in `fen` (timestamped input and output to a local
  file, off by default).
- Record through the real herdr path, in labeled test sessions only: Claude
  Code and Codex, covering empty/placeholder, typing, word wrap, Shift+Enter,
  Backspace joins, Left/Right/Home/End, Up/Down in multi-line text, slash and
  `@` popups, `!` mode, vim mode, Chinese IME text, typing while the agent
  streams with a transcript above, Ctrl+C clear, narrow and wide terminals.
- Choose the shadow emulator by replaying every trace to identical grids.
- Write the Claude Code and Codex profiles from the traces.

**Phase 1: composer-aware engine (about 2 weeks)**

- `internal/predict`: emulator wrapper, locator, parser with self-check, edit
  model, layout, reconciliation, renderer.
- `echo_ack_v1` in the cloud terminal service and the client, with fallback for
  services without it.
- Tests: profile golden tests (parse → render must reproduce every recorded
  frame); trace replays with injected 250 ms latency asserting which keystrokes
  displayed immediately and the final screen; randomized edit sequences against
  a reference layout for the convergence invariant; barrier and divergence
  tests.
- Acceptance on the Mac with real sessions: in Claude Code and Codex, ≥95% of
  composer edits (including Shift+Enter, wraps and Backspace joins) render
  locally; no residual artifacts once confirmed; no prediction in dialogs,
  permission prompts or password prompts; prediction disables itself cleanly
  when a profile's self-check fails.

**Phase 2**

- Profiles for Kimi, Pi, OpenCode, Cursor and Grok.
- Optional submit prediction: on Enter, clear the composer locally when the
  profile marks it safe (no popup open, not a slash command).
- The same engine for the macOS app's built-in terminal, if it carries cloud
  sessions.

## Risks

- **Profile drift with agent releases.** The self-check turns layout changes
  into "prediction off", never into wrong pixels. Profile failures are counted
  per agent version so updates can be shipped quickly.
- **Width disagreements** for rare code points: only unambiguous ranges are
  predicted.
- **Transcript rows above a growing composer**: growth into transcript rows is
  shown only when the profile can derive them from the shadow grid.
- **Terminals without synchronized output** may tear briefly during a paint;
  each paint is one `write`.
