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
  the version in the agent's own startup banner (empty when attaching to a
  running session, whose banner is gone; version numbers in the conversation
  are never taken), the terminal size, keys typed (mouse and focus reports
  counted apart), characters and input method commits (one commit is one
  key however many characters it holds), predictions, confirmations,
  rollbacks with their causes (`wrong` or `missing`, the key's name, `:busy`
  when the agent was producing output), keys typed with no recognised
  composer, and the keys that waited for the agent by name (`up`, `tab`,
  `ctrl+k`, `text/` for a `/` menu, ...). A key predicted again after a
  rollback is counted once. It never holds typed text or screen content.
  `fen predict-stats [--json] [--all] [--since <24h|date>]` groups them by
  agent version; sessions of `record-composer` (`FEN_PREDICT_STATS=test`)
  are left out unless `--all`. A new version whose sessions are "never
  locked" means its profile no longer matches the layout.
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
reads is never taken for typing in its middle (it is predicted as a whole when
its end arrives, up to 1 MiB), and a trailing space the screen cannot show is
kept from the model when confirming a state.

OpenCode's rules, from the recordings: a `┃` bar with the text three columns
right of it, and width = right end of the `╹▀▀▀` edge − bar − 4 (70 on the
100-column home screen, 91 in a session). Rows are: pad, text, pad, status,
edge, footer. Words wrap as in Grok (a space that does not fit starts the next
row hidden). On the home screen the logo, composer and footer form one block
centered vertically, so each added row moves either the block's top up or its
bottom down (top = (rows+1−height)/2, taking the top where the logo starts);
in a session the composer is anchored to the bottom. An empty draft shows a
gray (#808080) placeholder. A paste of three or more lines, or more than 150
characters, becomes `[Pasted ~N lines] ` (N counts lines without a trailing
break), which Backspace deletes whole; it is predicted.

### Editing keys and menus (recorded 2026-11-16)

Labelled test sessions (no prompt submitted) recorded the editing keys,
the `/` and `@` menus, Tab, history and pastes on each agent; the replays
(`TestEditingKeysRecordedSessions`, `TestMenuRecordedSessions`,
`TestPasteRecordedSessions`) show every prediction exactly and no rollback.

| Key | Claude Code | Codex | OpenCode 2.3.0 | Grok Build 1.0.77 |
| --- | --- | --- | --- | --- |
| Ctrl+A/E, Ctrl+K/U/W, Alt+Backspace | predicted | predicted | predicted (logical line) | predicted (logical line) |
| Home/End | visual row | logical line | whole draft | logical line |
| Ctrl+B/F | predicted | predicted | predicted | no effect in Grok: waits |
| Up/Down inside a multi-row draft | predicted | waits | predicted (visual row) | predicted (visual row) |
| Up/Down on the first/last row | waits (history) | waits | waits (history) | waits |
| Tab | no change unless a popup may be open | waits | no change unless a popup may be open | waits (switches view) |
| Typing in the `/` popup | predicted | predicted | predicted | waits (inline completion) |
| Typing in the `@` popup | predicted | predicted | predicted | predicted |
| Up/Down/Tab/Enter in a popup | waits | waits | waits | waits |
| Multi-line paste | chip predicted | chip predicted | chip predicted | 4+ lines: waits (chip with a preview popup) |
| Enter (submit) | predicted | predicted | waits (no session-view submit recorded) | waits (no submit recorded) |

Word deletes and word moves wait when letters-and-digits words and
whitespace-separated words disagree (punctuation), as the agents differ
there. A popup is assumed open while the draft is a slash command without a
space, or the word before the cursor starts with `@`.

Claude Code's popup takes the place of the notification row above the box;
the box is recognised under it as long as the draft opens a popup, and edits
that would change the box's height wait. OpenCode's popup sits over the rows
above the box (and over the home screen's logo, so the block top found before
is reused); its rows close with a second bar, which ends the box. Grok draws
the rest of the best slash command after the cursor (`/he` shows `/help`), so
any key in a slash command waits.

Two engine fixes came from these recordings: a key with no visible effect
(Grok's Ctrl+B) used to resolve when the keys typed behind it were drawn,
and they were then applied a second time; now the server is first compared
with the draft before that key plus the keys behind it. And while only an
unpredictable key is pending, nothing is painted (the agent's newer frame was
covered by the last confirmed draft).

The Codex menu recording is cut before `@x`: from there the mention popup
listed the recording user's other tasks, and history recalled a real prompt.
Recordings that show popups or history need that check before they are
committed.

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
| Pi (pi-tui) | With `PI_HARDWARE_CURSOR=1`, which Fennec now sets | Profile from real Pi 1.5.2 sessions (`pi-typing/edit/paste/cjk-100x30`): typing 195/195 and editing keys 206/207 predicted exactly; wraps with pi-tui's own algorithm (`piWrap`, checked against pi-tui's JavaScript) |
| Kimi (pi-tui in a box) | No: a drawn reverse-video cursor; the hidden terminal cursor is not reliably at the insertion point | Profile from real Kimi Code 2.6.2 sessions (`kimi-*-100x30`): the composer is found by its border and drawn cursor, predictions draw the cursor themselves (`Paint.DrawnCursor`, the terminal cursor stays hidden); editing keys 207/207 exact |
| Cursor | No (cursor parked below the composer) | Not predictable with this design |

### Chinese input and other sizes (recorded 2026-11-16)

`record-composer -scenarios cjk` commits Chinese phrases the way an input
method does (one write per phrase; the preedit never reaches the terminal)
and was recorded on all four agents at 80x24, 100x30 and 160x45, with the
typing and edit scenarios at 80x24 and 160x45. The first replay found
mispredictions on every agent; the fixes:

- OpenCode, Grok and Codex break rows between CJK characters (no hidden
  space), but never before closing punctuation (`，`, `。`, `）`, ...) or
  after opening punctuation; the wraps model this, and the parse also
  reads a row boundary next to CJK text without a separator. Claude Code
  moves a CJK run as one word, as modelled.
- A typed line break before a word that would not have fitted looks like a
  soft wrap. Claude Code's multi-line hint decides it (a reading without a
  line break is rejected while the hint shows); elsewhere a break before
  ASCII text after CJK is read as a line break, and between CJK characters
  as a wrap.
- When several prefixes of the pending edits explain the screen (a
  character typed and deleted again), a longer one is confirmed only once
  the cloud acknowledged its edits.
- Keys typed while no composer is recognised (Codex's startup animation)
  form a barrier when the composer is recognised again, until the agent has
  answered them.

All twelve CJK recordings and the typing and edit recordings of Claude
Code, OpenCode and Grok at both sizes now replay with every prediction
exact and no rollback. Codex still mispredicts the first keys typed during
its startup animation (recordings at 80x24 and 160x45 not yet committed).

## Scrolling the transcript

`herdr-core attach` switches the user's terminal to the alternate screen,
which has no scrollback, and the agents (Claude Code, Codex, OpenCode, Grok,
Pi; not Kimi) run full screen with all-motion mouse tracking and scroll
their transcript themselves. Every wheel step, and every pointer move over
the window, therefore goes to the cloud, and each wheel step makes the
agent redraw the whole screen. `cmd/profile-scroll` measures this with a
stand-in pager through the real `herdr-core attach`: a 40-step gesture (one
step per 16 ms, three lines each, 100x30) returns about 512 KB when the
program redraws per step and 436 KB when it redraws at most every 16 ms;
Herdr re-encodes each full redraw at about 13 KB and does not merge frames.

The client coalesces mouse reports (`internal/launcher/mouse_coalesce.go`):
a packet of mouse reports is sent at once when no earlier one is
unanswered; otherwise it waits for the cloud's acknowledgement (at most
400 ms; without acknowledgements, 50 ms windows) and goes out with the
others as one packet with no typing gap, so the cloud writes it to the
agent in one piece. Wheel steps, clicks and drags are never dropped; of
pointer moves with no button held only the latest is kept; other input
first sends what is held. For a program that redraws at most once per
frame this cut the measured gesture from 436 KB to 51 KB (one redraw per
round trip); a program that redraws per step gains nothing.
`fen predict-stats` shows mouse reports and the packets they went out in.

Codex 0.169.0, measured on a resumed conversation (`profile-scroll
-address`): it redraws the whole screen for every wheel step even when the
steps arrive together (about 4.5 KB each through Herdr, mostly cursor
positioning), so coalescing does not reduce it; a 40-step gesture is about
180 KB. Between steps it also emits runs of synchronized frames that change
nothing (pen reset, cursor to where it is); `NoopFrameFilter` in the stream
server drops those that repeat the frame just before them, held across
reads because Herdr's writes do not follow frame boundaries: about three
quarters of the frames of a gesture, but only some 6% of its bytes. The cloud
host's egress is limited to about 128 KB/s (the QoS scheduler in
`internal/traffic` keeps interactive output first within it, at 112 KB/s), so
such a gesture takes over 1.5 s to arrive: bytes per gesture are what matter.

The stream server therefore merges output that waits for the link
(`internal/terminalstream/conflate.go`). Reading the attachment and sending
are separate: the reader keeps reading, and what has not been sent yet waits
in order. Segments that only change cells on the alternate screen (text,
cursor moves, colours the screen model keeps, erases, scrolls, the scroll
region, cursor visibility; checked token by token) merge into one target
screen; anything else (titles, mode changes, bells, other SGR) waits as raw
bytes in its place. When the sender has credit it takes everything waiting:
a single segment goes as it was, several go as the difference between what
the client shows and the latest screen (`predict.DiffScreen`, verified by
replaying it onto a copy, and used only when smaller). Without a backlog the
output is unchanged; after a resize the next difference repaints every cell.
Replaying all 55 recordings with a sender that falls behind at random, the
client shows exactly the real screen after every send; a Codex-like 40-step
gesture drawn while one send per round trip goes out shrinks from 254 KB to
under 8 KB.

### Measured on the real link (2026-11-16)

Merging alone did not make Codex scrolling fast on the real link. The setup:
two labelled Fennec test sessions on the Mac, attached in tmux with
`FEN_PREDICT_TRACE`: A resumes an old Codex conversation and scrolls (40
wheel steps in about 80 ms, sent in bursts of five; `tmux send-keys` costs
about 120 ms a call, so one step per call would be far slower than a
trackpad); B is an idle Claude Code session in which a key is typed before
and 0.3, 1.3 and 2.3 s into each of A's scrolls. Nothing is submitted.
Arrival is the last output A receives after the first step; echo is the
first complete frame in which B's composer shows the key. Per-frame
timestamps come from the client's trace.

With the 4 KiB terminal output wire window (`outputWireWindow`), output
reached the Mac at a steady 4 KB per 250 ms, about 17 KB/s, a seventh of the
host's 128 KB/s: credit for each frame returns only after the Mac consumed
it, so a stream has at most 4 KiB of encoded output in flight per round trip
(a 1 KiB chunk is about 0.9 KiB after gzip, base64 and JSON). Raising the
window to 32 KiB (about 128 KB/s times 250 ms):

| | 4 KiB window | 32 KiB window |
| --- | ---: | ---: |
| Codex 40-step scroll, last output after the first step | 8.0–11.6 s (3 runs) | 1.1–2.9 s (6 runs) |
| Output per scroll | 48–76 KB | 76–79 KB |
| Claude Code echo while A's output was arriving | 389–861 ms (12 keys) | 198–339 ms (12 keys) |

A single session with the 4 KiB window measured 4.0–5.4 s per scroll earlier
the same day; the two-session 4 KiB run also had one reconnection, so its
upper values are pessimistic. Echo improved too: with 4 KiB, scrolling back
to the bottom (60 steps) was still arriving 12 s later, so B's keys waited
behind it; with 32 KiB the backlog drained within a few round trips and B's
echo stayed at the idle round trip (about 200–340 ms). Merging keeps the
larger window from filling the link with stale frames.

This is one pair of runs on one connection at one time of day. It does not
contradict `docs/ssh-backlog-investigation.md`, which found that about 32 KiB
of unsent TCP data delayed pongs under paced bulk load; that concerned
lossy-link backlog of file/socket output, whose window stays 16 KiB. Under
packet loss a 32 KiB terminal window can add the same kind of delay; the
`profile-ssh-latency` comparison should include it.

A second pass the same evening (32, then 64 KiB, same method) was much
noisier: echo in B rose to 0.4–2.4 s at 32 KiB and 0.2–1.7 s at 64 KiB,
including keys typed before a scroll, so the link itself was slower then;
scrolls arrived in 1.0–4.9 s and 1.1–3.0 s. The planned 16 and 48 KiB runs
were lost when the sweep died. At 64 KiB the window exceeds the
bandwidth-delay product at this round trip, so the extra 32 KiB can only
queue ahead of other output; no run showed a gain worth that.

### The window follows the round trip

A fixed window suits one round trip: 32 KiB is about the bandwidth-delay
product at 250 ms, twice what a user at 80 ms needs (who then queues
16 KiB ahead of their echo) and too little at 400 ms. Each terminal stream
therefore sizes its own window from its measured round trip:

    window = clamp(1.5 x 160 KiB/s x minRTT, 8 KiB, 64 KiB)

- 160 KiB/s is the application budget (`traffic.DefaultConfig`), increased
  from 112 KiB/s at the user's request. It exceeds the observed 128 KiB/s
  host cap; it is not a measured path capacity. The 1.5 gain covers credit
  returning in bursts and round-trip jitter. The new budget needs paired
  latency and throughput validation.
- A sample is the time from handing a frame to SSH until the peer has
  credited all of it. Only frames sent while nothing else of the stream was
  in flight are sampled, so a stream's own queue cannot inflate them;
  otherwise sustained output would raise the samples, the window with them,
  and the queue again.
- minRTT is the minimum over the last 10 s, so a slower path is followed
  once its old minimum expires, while output of other streams queued ahead
  in the pacer rarely raises it.
- The window starts at 16 KiB until the first sample and never stops an
  idle stream: one frame always goes when nothing is in flight.
- Socket streams keep their fixed 16 KiB window
  (`docs/ssh-backlog-investigation.md`).

| Round trip | Window |
| --- | ---: |
| 20 ms | 8 KiB (floor) |
| 80 ms | 19 KiB |
| 220 ms | 53 KiB |
| 250 ms | 60 KiB |
| 400 ms and above | 64 KiB (cap) |

Historical measurement with the previous 112 KiB/s budget, using the same
two-session method right after the second pass
(2 runs, 12 scrolls, 24 keys; the round trip was about 200–250 ms, so the
window was about 33–41 KiB): scrolls arrived in 1.0–3.3 s (median about
1.3 s), as fast as the fixed 32 and 64 KiB windows. Echo in B was 194–428 ms
in the first run; in the second, 5 of the 9 keys typed during a scroll took
1.0–1.35 s while the 3 typed before one stayed at 200–230 ms, the same kind
of delay the 32 and 64 KiB windows showed in the second pass. Scrolling is
as fast as with either fixed window; echo during a scroll is not yet
reliably at the idle round trip, and where that delay comes from is not yet
measured (candidates: A's frames ahead in the pacer, the TCP backlog in
`docs/ssh-backlog-investigation.md`). These runs cannot
show the gain for short round trips, which needs a user near the region.

### Merging by time and hyperlinks

With a window near the bandwidth-delay product the sender is rarely blocked,
so the conflator, which merged only output piling up behind it, sent almost
every Codex redraw. `cmd/profile-scroll` now records the real attachment
output of a gesture (Codex resumed in a throwaway Herdr terminal on the
cloud, wheel reports only) and replays it through the conflator and a
modelled sender and link (`terminalstream.SimulateSender`: per-chunk window
credit, first-in first-out link, credit a round trip later). Every recorded
agent session replays to the exact screen at every interval and window.

It showed two things. A take at most once per 16 ms alone saved little,
because about 72 KB of the 180 KB Codex draws for 40 steps (40 %) could not
be merged at all: those frames contain OSC 8 hyperlinks (the transcript
links to documentation), which the screen model consumed and ignored. So:

- `predict.Screen` keeps OSC 8 links as xterm.js does: printed cells carry
  the open link (`Cell.Link`, `Screen.PenLink`), SGR does not end it, erased
  cells have none, DECSC/DECRC keep it, and a link over `MaxLinkBytes` is
  dropped. `DiffScreen` emits OSC 8 where the link changes and leaves the
  target's link open; `SameScreen` compares links.
- The conflator treats a well-formed OSC 8 within that bound and without
  control characters as a cell change; titles and other strings still go
  raw in their place.
- The sender takes at most once per `outputMergeInterval` (16 ms, one
  display frame; xterm.js renders at most once per animation frame, so no
  visible frame is lost). Output after a quiet interval goes at once, so a
  single echo is not delayed. While output waits for the interval it counts
  as queued for echo acknowledgements, so no acknowledgement overtakes it.

40-step Codex scroll, wheel reports coalesced per round trip as the client
sends them, 220 ms round trip, 128 KiB/s, the live window (54 KB):

| | Bytes sent | Last output arrives |
| --- | ---: | ---: |
| Before | 104 KB | 1.01 s |
| Merging by time only | about 81 KB | 0.95 s |
| Merging by time and hyperlinks | 24 KB | 0.81 s |

The arrival floor is about 0.79 s: the client sends the gesture's wheel
reports once per round trip and Codex draws the last step about 0.68 s in.
The gain is mainly the link: a gesture occupies it for about 0.2 s instead
of 0.8 s, which leaves room for other sessions' echo. With wheel reports one
per event 16 ms apart, each frame is a separate full redraw and little is
saved (124 to 121 KB). This is one transcript, heavy with links; Claude Code
was not measured.

### State sequences

Agents draw more than cells. Replaying every recorded session (55
recordings of typing, editing, menus and pastes) showed what kept frames on
the alternate screen from merging: Codex's cursor style (`CSI Ps SP q`) and
spinner window title (OSC 0); OpenCode's cursor colour and pointer shape
(OSC 12, 22) and cursor style; Grok's SGR 59 and a kitty graphics deletion
(`ESC _ G a=d,d=I,i=1,q=2`) in almost every frame; Pi's kitty delete-all.
None of these change cells; they assign terminal state.

The conflator now keeps, for each merged run, the last token of each state
key and sends them after the difference, in the order of their last
occurrence (`stateKey`). If every kept token is an assignment whose effect
does not depend on earlier state, the client ends with the same state: for
every piece of state the last assignment in the run is still the last one
sent, even when keys share state (OSC 0 sets the title and the icon name,
OSC 2 the title). Supported: cursor style, OSC 0/1/2, OSC 10/11/12/22 with
one value (not a query) and their resets 110/111/112, and kitty deletions
that ask for no reply (repeating one changes nothing; placements, which
would make the order matter, are not merged). SGR 58/59 (underline colour)
is now part of the screen's pen. Modes stay raw: some terminals switch mouse
modes off only when the reset mode is the active one, so their effect
depends on earlier state; so do the kitty keyboard stack (`CSI > u`,
`CSI < u`), queries, clipboard and Herdr's OSC 7501. The recorded-session
test checks both the screen and the folded state assignments after every
take.

Share of alternate-screen output that merges, all recordings:

| Agent | Before | After |
| --- | ---: | ---: |
| Claude Code | 98.7% | 98.7% |
| Codex | 89.2% | 99.8% |
| Grok | 9.8% | 99.5% |
| OpenCode | 84.3% | 99.7% |
| Pi | 90.1% | 99.7% |

What remains is mode toggles and OSC 7501, each under 1%. Kimi draws on the
main screen with scrollback and is not merged.

### Landed, on the real link (2026-11-16)

The two-session test (40-step Codex scrolls in A, keys in a Claude Code
session B), main before these changes (`d655852`, 21:23) against main with
them (`3617737`, installed as 0.3.11162136-dev, 21:36), about 220 ms round
trip:

| | Before (2 runs, 11 scrolls) | After (3 runs, 17 scrolls) |
| --- | ---: | ---: |
| Bytes per scroll, median | 79 KB (57–81) | 27 KB (21–66; 15 of 17 under 37) |
| Last output after the first step, median | 2.3 s (0.96–8.0) | 0.90 s (0.54–4.2) |
| B's echo, run by run | 0.23–3.6 s; 0.26–4.1 s | 0.18–0.83 s; 0.20–3.4 s; 0.17–0.32 s |

The before runs include a reconnection (its second run), so its upper
values are pessimistic. In the second after run echo rose steadily to
3.4 s for keys typed before a scroll as well, so the link itself was slow
then; the third run kept every key at the idle round trip (0.17–0.32 s)
while A scrolled. Two scrolls of the first after run still sent 58 and
66 KB; why is not yet known.

Herdr Core 0.1.18 has no scrolling of its own while attached (`--history N`
only prints history to the main screen before switching to the alternate
screen); Fennec has used the same attach since its first commit.

## Checking profiles after an agent release

`cmd/record-composer` (run on the Mac, which has fen and tmux) records the
standard scenarios (`typing`, `edit`, `menu`, `paste`) for each agent in a
labelled test session (`fen launch`, a private tmux session with
`FEN_PREDICT_TRACE`, `fen stop`), and replays every recording with
`predict.Replay`, the harness the trace tests use:

    go build -o /tmp/record-composer ./cmd/record-composer
    /tmp/record-composer [-agents grok] [-scenarios edit,menu] [-size 120x40] [-save]

It prints, per agent version and scenario, the keys predicted, wrong
predictions, rollbacks, the keys that waited, and the committed recording's
numbers with any key that now waits more often. A profile that no longer
matches shows as `DRIFT` (exit status 1): either the composer is never
recognised, or a shown prediction differs from what the agent drew.

No scenario sends Enter or a typed line break (checked before recording;
Codex's Tab with a draft is left out, as it may submit), so no prompt and no
inference is used; the only Enter confirms Claude Code's folder trust
dialog. Every recording is scanned for the home path, user and host names,
other sessions' names, and letters the scenarios never type (from history
or mention popups); only clean recordings are saved with `-save`, as
`testdata/traces/<agent>-<scenario>-<cols>x<rows>`. `-replay <dir> -agent
<agent>` replays one recorded directory anywhere.

## Risks

- **Profile drift with agent releases.** The self-check turns layout changes
  into "prediction off", never into wrong pixels. Profile failures are counted
  per agent version (`fen predict-stats`), and `cmd/record-composer` checks a
  new release against the profiles in minutes.
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
