# Get started checklist

Status: implemented for Fennec 0.5.7 (`native/macos/UI/get-started.js`,
`internal/cli/terminal_coverage.go`), with the differences listed under
"Implementation" below. The clean-account end-to-end test is pending.

## Goal

A person who has just signed in to Fennec on the Mac ends with their own
Codex CLI or Claude Code (whichever this Mac has) running on the cloud
computer from their usual terminal, and knows how to leave and return to it.
Nothing is required: every step can be skipped, the card can be hidden, and
Fennec works the same without it.

## Prior art: Magpie's Get started

Magpie (`internal/gui/assets/app.js`, `guideShown`/`guideCard`) shows a card
above its agent list until a model source exists and an agent is connected:

- numbered steps with a "1 of 2 done" count; an optional step doesn't count;
- each tick is computed from current state, never from a click, so a step
  undoes itself when its condition stops holding;
- the agents found on the computer are chips, and a click connects one in
  place; a chip that can't work yet is disabled with the reason as its tooltip;
- an × hides the card for good, saved in settings, and the card disappears on
  its own once the required steps hold.

Fennec keeps these rules. It adds one thing Magpie doesn't need: the last step
is verified by the user's own terminal, because a cloud selection without
terminal coverage looks done while `codex` still runs locally (see the
2026-11-18 incident in [agent-shims.md](agent-shims.md)).

## Where

At the top of the panel's Agents view, after the first-run setup stage closes
(`onboarding.js` hides when `agents_installed`). It never covers other UI and
never opens a window by itself.

## Steps

Each step is done or not from the state below; the card re-renders whenever
that state changes.

1. **Cloud computer ready.** Done when preparation reaches `agents_installed`.
   While it doesn't, the step shows the preparation stage the panel already
   shows, and a failed stage's own retry.
2. **Run an agent on the cloud computer.** Done when any agent this Mac has
   installed is selected for the cloud computer and its route is active
   (`activeTakeoverAgents`). Not done: chips for the installed, cloud-supported
   agents, Codex and Claude Code first, then the others; a click sends
   `toggle-takeover:<agent>` (Codex Desktop keeps its restart confirmation).
   A chip whose agent has no local sign-in to copy says so and offers the
   agent's sign-in instead. No supported agent installed: "Install Codex or
   Claude Code, and Fennec will list it here." with the two install links.
3. **Start it from your terminal.** Done when a cloud session of a selected
   agent has started since the selection (the session list). Not done, the
   step shows the command to copy (`codex`, `claude`) and "in a new terminal
   window", plus the agent's terminal coverage:
   - covered (`covered_by_function`, `covered_by_shim`, `covered_by_entry`):
     nothing more is shown;
   - not covered, or shadowed by the user's alias or function: what was found
     ("`codex` is an alias in your .zshrc"), and **Fix**, which runs the
     existing terminal-integration reconcile and checks again. Fennec never
     edits a file it may not edit (see agent-shims.md); for those it shows the
     line to add, with Copy;
   - unsupported shell: the line to add, with Copy.
   When it's done, the step says once how to leave and come back: "Ctrl+C
   detaches; the session keeps running. `fen` brings it back."
4. **Optional: Fennec on your iPhone.** Done once an iPhone has signed in
   with this Mac's pairing QR code (the Mac approves that pairing, so it
   knows). Not done: the QR code. Not counted in "n of 3 done".

The card hides itself once steps 1–3 are done, after showing them done for a
moment. It returns only if no agent is selected for the cloud computer any
more and the user hasn't hidden it.

## Data

- Preparation stage, installed agents, cloud selection and active routes, and
  sessions: already sent to the panel.
- Terminal coverage: new. The daemon runs `resolveInShell` (what
  `fen doctor agents` uses) for the selected agents only, after a selection
  change, after a reconcile and when the card opens, never more than once a
  minute, and sends `renderTerminalCoverage({agent: {coverage, kind, path}})`.
  It takes the user's login shell up to a few seconds; the step shows
  "Checking your terminal…" meanwhile.
- Hidden: a Fennec preference (`getStartedHidden`), not panel `localStorage`,
  so it survives a reinstall of the UI and is the same in every Fennec window.

## Copy

Short, plain, no internal terms (no "takeover", "shim", "route"). Every
string in English and Simplified Chinese in `translations.json`.

## Tests

- A UI test per state: each step done and not, chips enabled and disabled,
  each coverage result, hidden, and the card leaving when done.
- Go tests for coverage reporting: one agent, rate limit, unsupported shell.
- End to end on a clean macOS account: install Codex, sign in to Fennec,
  follow the card, run `codex` in a new Terminal window, confirm the session is
  on the cloud computer and the card is gone.

## Open questions

- Should step 2 suggest one agent ("Codex is installed: run it on the cloud
  computer") when only one is installed, instead of a one-chip list?

## Implementation (0.5.7)

- The card is `native/macos/UI/get-started.js`, loaded after `panel.js` and
  re-rendered after each panel render that changes its inputs. Preferences
  `get_started_hidden` and `get_started_done` are `settings.set` fields.
- `terminal.coverage` (`{agents?, force?}`, the selected agents by default)
  and `terminal.fix` (reconcile, then a forced check) are daemon methods; the
  panel calls them through the `terminal-coverage` and `terminal-fix` bridge
  actions and receives `renderTerminalCoverage`. A result is reused for a
  minute unless forced. An uncovered agent's reply carries the line to add
  for the user's shell.
- Differences from the design:
  - Step 4 is a link to the existing iPhone dialog without a done state. The
    Mac only starts the device authorization; approval happens in the browser,
    so Fennec cannot know whether an iPhone finished signing in.
  - An agent shadowed by the user's alias or function is explained but has no
    Fix: Fennec never overrides the user's aliases or functions
    ([agent-shims.md](agent-shims.md)), so Fix could not change the result.
  - Step 3 is done when a live cloud session of a selected agent exists,
    whether started from a terminal or from Fennec, not only one started
    after the selection.
  - Codex Desktop is not offered in step 2: the steps end in a terminal.
- Tests: `scripts/tests/get-started-state.cjs` (every state above, run by
  `make`), `internal/cli/terminal_coverage_test.go` (one agent, the selection
  default, rate limit, snippet per shell).

