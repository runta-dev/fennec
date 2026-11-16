# Composer-aware predictive echo for cloud terminals

Status: implemented for Claude Code and Codex (`internal/predict`); see
Implementation status at the end. Surface: the `fen` attach client on the
Mac, plus two small capabilities in the cloud terminal service.

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
| Long text paste | Unpredictable | The agent's collapsed token (`[Pasted text #N +L lines]`) appears at once |
| Image paste | Unpredictable | `[Image #N]` appears at once; typing continues while it uploads |

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
- Pastes (bracketed): a 3-line paste is inserted inline as text. A 60-line
  paste becomes `[Pasted text #1 +59 lines]`, with `paste again to expand` in
  the footer. A pasted PNG path becomes `[Image #2]`: the number continued from
  the earlier text paste even though Ctrl+C had cleared it. The agent rendered
  each paste within about 30 ms of receiving it. The token formats come from the
  shipped bundle (`[Pasted text #N +L lines]`, `[Image #N]`,
  `[...Truncated text #N +L lines...]`).

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

### Input pipeline

Today the input goroutine takes transport credit before it reads stdin, and
`sendClipboardImage` uploads on the same goroutine. While a large paste or an
image is uploading, stdin is not read at all, so nothing could be predicted.
The pipeline is split:

1. **Reader**: always reads stdin, runs `terminalinput.Decoder` (detach
   handling unchanged) and hands each key to the engine immediately, so the
   screen updates within one local frame regardless of transport state.
2. **Ordered send queue**: the original bytes, image uploads and their
   sequence numbers, strictly in input order. It is bounded (1 MiB of text);
   when full, the reader stops predicting and applies backpressure as today.
3. **Sender**: drains the queue under the existing credit window.

Byte order on the wire is unchanged: a key typed after a paste or image still
reaches the agent after it, and Enter can never overtake an attachment. Only
the display is decoupled from the transport.

**Cadence preservation.** Keys that queued behind an upload, or that the
network bunched together, would reach the PTY back to back. Codex treats three
or more characters less than 8 ms apart as a paste burst
(`bottom_pane/paste_burst.rs`), and an Enter within 120 ms of a burst becomes a
newline instead of submit. Queued `fix it⏎` would then not be sent, and the
prediction would be wrong. Each input frame therefore carries the gap since the
previous key as measured on the Mac. The cloud terminal service writes queued
frames with that gap, capped at 50 ms per key and 1 s per backlog. Bracketed
pastes and image uploads are written immediately. This ships as capability
`input_cadence_v1` together with `echo_ack_v1`.

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
| Bracketed paste | `PasteText(text)`: inline insert or a collapsed token, per profile (see Paste) |
| Image paste shortcut with an image on the clipboard | `PasteImage`: image token at the cursor (see Paste) |
| Mouse, focus, key release | Neutral |
| Enter, Tab, Esc, other chords | Barrier |

A **barrier** sends the key, keeps everything already painted, and suspends new
predictions until the server's `echo_ack` covers it and the composer re-locks.
Edits typed in the meantime are queued and replayed on the post-barrier state,
so the first character after Enter appears one round trip after the submission
rather than one round trip after itself.

Mode triggers such as `/`, `@`, `!` and `#` at the start of an empty composer
are predicted as ordinary text. Any popups or prompt changes they cause arrive
from the server, and the composer is re-located on the next frame.

### Paste

Pastes are where the round trip hurts most: the agent can only render after
the entire paste or image has crossed the link (about 130 KB/s upstream). The
engine renders what the agent will show from the profile's paste rules, so the
composer updates instantly and the user can keep typing while bytes are still
uploading.

**Text paste.** A bracketed paste is one atomic `PasteText` edit. The profile
decides between inline text and a collapsed token:

| Agent | Inline | Collapsed token | Token numbering |
| --- | --- | --- | --- |
| Claude Code | Short pastes (observed: 3 lines inline) | `[Pasted text #N +L lines]`, L = newline count (observed: 60 lines → `+59 lines`) | Session-wide counter shared with images (observed `[Pasted text #1]`, then `[Image #2]` after Ctrl+C cleared the composer) |
| Codex | ≤ 1000 characters | `[Pasted Content C chars]`, C = character count | Suffix `#2`, `#3` for same-size pastes still in the draft (`next_large_paste_placeholder`); fully determined by the draft |

Claude Code's exact inline/collapse boundary and its truncation form for
pastes over 10,000 characters (`[...Truncated text #N +L lines...]` between
the first and last 500 characters) are confirmed by boundary traces in
Phase 0 before that rule is enabled. Inline pastes go through the normal
layout, including wrapping and growth. A collapsed token is an atomic element:
the cursor moves past it, and one Backspace deletes it whole, if the profile
says so.

**Image paste.** On the image paste shortcut, `fen` asks the native helper
whether the pasteboard holds an image. This is a type check, without encoding
the image. If it does, the engine inserts the profile's image token at the
cursor right away and the upload starts in the send queue:

- Claude Code: `[Image #N]` on the shared session counter. Codex:
  `[Image #K]`, renumbered within the draft after any remote image rows. Codex
  can also draw a thumbnail strip on supported terminals; that strip is server
  chrome the engine never predicts. It appears once the server confirms.
- While the upload is in flight, the token is drawn dim. It switches to its
  normal style when the server's composer shows it. The user can keep typing
  after it; those keys are predicted and queued behind the image.
- Enter pressed during the upload is a barrier as usual. It is sent after the
  image, so the message is submitted with the attachment.
- If the upload or validation fails (`paste_error`), the `PasteImage` edit is
  marked rejected and removed. The rest of the pending edits are replayed
  without it, the token disappears, and the existing notice is shown. Kimi's
  clipboard-bridge path joins in Phase 2.

**Session counters.** Claude Code's token number is not visible in an empty
composer. The engine keeps the highest token number it has seen in any server
frame of the session, plus the tokens it has predicted. After a fresh attach the
first predicted number can be wrong. Reconciliation therefore compares tokens by
kind and size (image, text with L lines, C chars) and adopts the server's
number with a repaint of those few cells, instead of treating it as a
divergence.

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
- The transcript above the composer is a viewport anchored to the composer
  block (for Claude Code: notification row, borders, text). A short
  transcript is top-aligned with a blank gap below it, which growth uses
  first; a full one is bottom-aligned and scrolls with the block (recorded in
  `testdata/traces/claude-transcript-100x30`). The profile remembers rows that
  scroll off the top, so a predicted shrink scrolls them back; a row it never
  saw is shown blank until the server's frame arrives.
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
  Pastes: Claude Code's inline/collapse boundary (by characters and by lines),
  pastes over 10,000 characters, a 100 KB paste end to end, image paste with a
  real screenshot, two images plus text in one draft, Backspace over a token,
  a failed image paste, and typing plus Enter queued behind an upload (Codex
  paste-burst behavior).
- Choose the shadow emulator by replaying every trace to identical grids.
- Write the Claude Code and Codex profiles from the traces.

**Phase 1: composer-aware engine (about 2 weeks)**

- `internal/predict`: emulator wrapper, locator, parser with self-check, edit
  model, layout, reconciliation, renderer.
- `echo_ack_v1` and `input_cadence_v1` in the cloud terminal service and the
  client, with fallback for services without them.
- Input pipeline split (reader, ordered send queue, sender), text and image
  paste prediction, and the native helper's image-type check.
- Tests: profile golden tests (parse → render must reproduce every recorded
  frame); trace replays with injected 250 ms latency asserting which keystrokes
  displayed immediately and the final screen; randomized edit sequences against
  a reference layout for the convergence invariant; barrier and divergence
  tests.
- Acceptance on the Mac with real sessions: in Claude Code and Codex, ≥95% of
  composer edits (including Shift+Enter, wraps and Backspace joins) render
  locally; a text paste or image token appears within one local frame of the
  paste, and typing after it stays instant while the upload is running; Enter
  after an image always submits with the image; no residual artifacts once
  confirmed; no prediction in dialogs,
  permission prompts or password prompts; prediction disables itself cleanly
  when a profile's self-check fails.

**Phase 2**

- Profiles for Kimi, Pi, OpenCode, Cursor and Grok.
- Optional submit prediction: on Enter, clear the composer locally when the
  profile marks it safe (no popup open, not a slash command).
- The same engine for the macOS app's built-in terminal, if it carries cloud
  sessions.

## Implementation status

Built:

- `internal/predict`: VT emulator (`screen.go`), key decoding including the
  Kitty protocol (`keys.go`), composer model (`state.go`), wrap-ansi and Codex
  wrapping (`wrap.go`), the Claude Code and Codex profiles, the engine
  (reconciliation, deferred edits, rendering, restore), and the trace
  recorder.
- The engine resolves ambiguous screens (spaces before a newline are
  invisible) with `Profile.Match`: the composer is locked when the engine's
  own confirmed or predicted state renders exactly as shown. Parsing alone is
  only needed to acquire the first lock.
- Keys typed behind an unpredictable key are deferred, not dropped: they are
  mapped against the server's state as soon as the barrier resolves.
- Enter is predicted (the composer clears) for Claude Code and Codex when the
  draft is not a slash command, shell input, or a mention being completed.
  `\` + Enter in Claude Code is predicted as a newline.
- Cloud terminal service: `echo_ack_v1` and `input_cadence_v1`, negotiated
  through `connect.features`; old clients never receive the new frames.
- Client: sequence numbers and typing gaps on input frames; the reconnecting
  input queue holds 256 packets so uploads never stop key reading; image
  prediction uses `fennec-clipboard --probe` (pasteboard types only).
- Setting `predictive_echo` (`""`/`adaptive`, `always`, `off`) through
  `settings.set`, overridden by `FEN_PREDICTIVE_ECHO`. `FEN_PREDICT_TRACE=<dir>`
  records sessions in the format the trace tests replay.
- Usage statistics: each attached session appends one line of counters to
  `~/.config/fennec/predict-stats.jsonl` (mode 0600, rotated at 2 MB, never
  sent anywhere; `FEN_PREDICT_STATS=off` disables it). A line holds the agent,
  the version its startup banner showed, the terminal size, and counts of
  keys, predictions, confirmations, rollbacks, expiries, keys typed with no
  recognised composer, and the keys that waited for the agent by name
  (`up`, `tab`, `ctrl+k`, `text/` for a `/` menu, ...). It never holds typed
  text or screen content. `fen predict-stats [--json]` groups them by agent
  version; a new version whose sessions are "never locked" means its profile
  no longer matches the layout.
- The macOS app's terminal runs `fen attach` in a PTY, so it uses the same
  engine with no extra work.

Verified on recordings: every recorded Claude Code keystroke (typing, word
wrap, Shift+Enter growth with the hint row, Backspace joins, inline and pill
pastes, the image pill with its automatic space, Ctrl+C) is predicted exactly
as Claude Code rendered it, and the engine replay with a 500 ms round trip
shows every prediction immediately and converges with no leftover cells.
Randomized runs (latency, dropped keys, both agents) keep every unpredicted
row identical to the server and converge exactly.

Claude Code with a full transcript is verified against a real session
(`testdata/traces/claude-transcript-100x30`, with the cloud's real echo
acknowledgements): typing that wraps twice, Shift+Enter and Backspace joins
scroll the transcript up and back; 220 of 228 key packets are predicted, each
prediction equals Claude Code's later frame on the whole screen, and there is
no rollback. Two acknowledgement rules came from these recordings: an
acknowledgement only means the agent produced some output, so a prediction is
judged 250 ms after it, and an unpredictable key (a barrier) is resolved only
when the composer has changed or that grace period has passed.

Typing while the agent works is verified on two more recordings with real
acknowledgements. `claude-streaming-100x30`: 70 keys typed during 14 s of
thinking and streaming (150 output frames); all 321 predicted key packets
match. `codex-streaming-100x30`: typing throughout a 34 s Codex task; all 173
match. Both have no pauses or rollbacks. Rules they added: Claude Code hides
the leading whitespace of a soft-wrapped row (a space typed after an exactly
full row is invisible and the cursor sits at column 0 of the next row), and
while a Codex task runs, a non-empty draft shows "tab to queue message" in
the footer's second row.

`opencode-streaming-100x30` adds OpenCode: 163 keys typed during 31 s of
streaming (771 output frames), with real acknowledgements. Of 196 predicted
key packets, 195 match a later OpenCode frame exactly. In the remaining one,
OpenCode drew that key together with the next one (it skipped the state
ending in a typed space), and its next frame continues the prediction. There
are no pauses or rollbacks. The replay harness counts such cases separately
as "merged" and fails if they exceed 5%.

Codex is verified against a real session recorded through `fen` and herdr
(`testdata/traces/codex-100x30`, Codex 0.169, 100×30): with the recording's
own timing and link latency, 113 of 116 key packets were predicted and every
prediction matched a frame Codex later drew. The recording found one layout
rule missing from the spec reading: the footer's "? for shortcuts" row exists
only while the draft is empty, so the composer moves down one row on the first
character and back up when the draft empties. Codex's startup starfield
animation (braille dots in the composer block, about 6 s) prevents a lock
until it stops, which is the safe outcome.

Grok's rules, from the recording: the box has a rounded border with `❯` at
column 4 and text from column 6 to columns-4 (width columns-10); the bottom
border (with the model) never moves and the box grows upward into blank rows;
words wrap, and a space that does not fit at the end of a row starts the next
row hidden and takes no width there. With the cursor at the end of an exactly
full row Grok hides the cursor and parks it there, and a space typed then
creates no row until the next character. The footer row shows a notice with
an empty draft and key hints otherwise. A paste of four or more lines becomes
a `[Pasted: N lines]` chip, after which Grok wraps differently and opens an
expand popup; such pastes and drafts are left to the server.

Two engine rules came from this recording: a bracketed paste split across
reads is one unpredictable paste (its middle is never taken for typing), and
a trailing space the screen cannot show is kept from the model when
confirming a state.

OpenCode's rules, from the recordings: a `┃` bar with the text three columns
right of it, and width = right end of the `╹▀▀▀` edge − bar − 4 (70 on the
100-column home screen, 91 in a session). Rows are: pad, text, pad, status,
edge, footer. Words wrap as in Grok (a space that does not fit starts the next
row hidden). On the home screen the logo, composer and footer form one block
centered vertically, so each added row moves either the block's top up or its
bottom down (top = (rows+1−height)/2, taking the top where the logo starts);
in a session the composer is anchored to the bottom. An empty draft shows a
gray (#808080) placeholder. A paste of three or more lines, or a long one,
becomes `[Pasted ~N lines] `, which Backspace deletes whole; such pastes are
left to the server.

OpenCode 2.3.0 hangs at startup when the user's inotify instances are
exhausted (reported upstream as anomalyco/opencode#64149). On the cloud
runtime they had been exhausted by `dbus-daemon` processes that Magpie's Go
dbus client started through `dbus-launch`. Fennec now sets
`DBUS_SESSION_BUS_ADDRESS=disabled:` for Magpie and the agents, and raises the
limit to 1024 during preparation.

Other agents, from reading their packages:

| Agent | Hardware cursor at the insertion point | Status |
| --- | --- | --- |
| OpenCode (OpenTUI) | Yes | Profile from two real OpenCode 2.3.0 sessions (`testdata/traces/opencode-100x30`, home screen, and `opencode-session-100x30`, session view): 202 and 224 predicted key packets, all match; see above |
| Grok (ratatui) | Yes | Profile from a real session (`testdata/traces/grok-100x30`, Grok Build 1.0.77): 202 of 202 predicted key packets match; see below |
| Pi | Only with `PI_HARDWARE_CURSOR=1` | Needs that setting in Fennec's Pi launch |
| Kimi | No (drawn reverse-video cursor) | Not predictable with this design |
| Cursor | No (cursor parked below the composer) | Not predictable with this design |

## Risks

- **Profile drift with agent releases.** The self-check turns layout changes
  into "prediction off", never into wrong pixels. Profile failures are counted
  per agent version so updates can be shipped quickly.
- **Width disagreements** for rare code points: only unambiguous ranges are
  predicted.
- **Transcript rows revealed by a shrink** that scrolled off before the
  profile saw them are blank at the top of the screen for one round trip.
- **Terminals without synchronized output** may tear briefly during a paint;
  each paint is one `write`.
- **Prediction does not shorten uploads.** It makes the screen instant, but a
  message with a large image is still submitted only after the upload
  finishes. Downscaling and recompressing clipboard images on the Mac before
  upload is tracked separately and complements this work.
