# Fennec desktop window (native)

The desktop window described in [docs/desktop-app.md](../docs/desktop-app.md),
native on GPUI. Projects, requests and sessions are Fennec's; terminals are
[herdr-app](https://github.com/herdrdev/herdr-app)'s, kept unchanged (see
[UPSTREAM.md](UPSTREAM.md), Apache-2.0).

## Build

Needs Rust (stable, edition 2024), Zig 0.16 (builds libghostty-vt) and the
Ghostty submodule:

```sh
git submodule update --init --depth 1 desktop/vendor/ghostty
cd desktop && cargo build --release
```

Run the desktop suite with `cargo test --locked`. GPUI's test support is a
development-only dependency. The unchanged upstream terminal tests use Fennec's
default key bindings without personal overrides and a test-only extract of
Herdr's Edit-menu adapter; those menu tests do not validate the native shell's
production menus.

Linux also needs `pkg-config clang libclang-dev libxcb1-dev libxkbcommon-dev
libxkbcommon-x11-dev libwayland-dev libvulkan-dev mesa-vulkan-drivers
libfontconfig-dev libfreetype-dev` (herdr-app's CI list).

## Where it connects

- **Terminals:** herdr-core through `hc connect`. On a Mac, Fennec sets
  `FENNEC_HC` to a program that runs `fen hc connect`, which runs `hc connect`
  on the cloud computer over the daemon's resident connection (control method
  `hc.open`). On the cloud computer itself, the local `hc`.
- **Board:** the daemon's `board.call` on a Mac; `fennec-terminal board-call
  METHOD PATH` on the cloud computer (`FENNEC_BOARD_CMD` overrides the program).

Sessions are the core's terminals labeled `fennec.session`. Local shell tabs
run on this Mac's herdr-core (`FENNEC_LOCAL_HC`; Fennec.app bundles herdr-core,
`vendor/herdr-core` at v0.1.21, as `Contents/MacOS/hc` with `HC_SESSION=fennec`),
labeled `fennec.shell-of=<session>`.

## Design comparison

The native chrome follows `docs/design/desktop/src/style.css` and uses its SVG
paths. Terminal rendering stays upstream Herdr; the terminal icon is copied
verbatim from Herdr and padding follows Herdr's main layout (14 by 12 px).
The user's later decisions override the mockup: no organization switch, no
workspace switch, neutral editor focus borders, and menu-bar-sized new buttons.

For isolated rendering, set `FENNEC_DESKTOP_PREVIEW` to the absolute path of
`fixtures/design.json` and `FENNEC_DESKTOP_SHOT` to an output PNG. This mode
reads fixture responses and never connects to the daemon or creates terminals.
Set `FENNEC_PREVIEW_THEME=light|dark` and `FENNEC_LANGUAGE=en-US|zh-CN` as needed.
Write `project:fennec`, `request:42`, `settings:fennec:general` (or `queue`),
`knowledge:fennec`, or `menu:tags` to `<output.png>.request` to capture
that view. Fixtures prove visual layout only, not live runtime behavior.

## Keyboard preferences

`~/.config/fennec/desktop-keys.json` accepts Herdr's `global` and `terminal`
key-to-action maps. The map is read when the native window starts; it does not
change Herdr's configuration. Supported actions include new/close/previous/next
and numbered tabs, previous/next and numbered session groups (`space.*`),
clipboard/search and `send:` editing keys, `request.new` (Command-N, focuses
the New request box from anywhere), search (`mod.open:picker`, also Command-K),
and terminal zoom. Ctrl-B's prefix supports `s` for search and `.` for Fennec
settings; Escape cancels. Settings and Command-comma open the native app's own settings page. Its theme,
language, terminal font size and padding are persisted independently in
`~/.config/fennec/desktop-settings.json` via `desktop.settings.get/set`; changing
them does not modify menu bar preferences. Close-others asks for confirmation and
closes other shell tabs while keeping the group's agent anchor.

Fennec does not yet implement Herdr split panes, pane direction/swap/zoom,
layout switching, space creation/renaming, tab renaming, the command palette,
or Herdr's help surface. Their bindings are ignored, not swallowed
or silently assigned to different operations. Native macOS app quit/hide
shortcuts continue to follow the operating system.

A confirmed new tab selects its parent session, records its active tab and
transfers keyboard focus exactly once. Background board/session refreshes do
not reclaim focus. Terminal tabs share their header row with the window
controls and a back button to the queue. Double-clicking non-control
titlebar space zooms/restores the window. Re-activating a terminal window
restores focus to its active terminal, without stealing focus from project editors.

## Layout (FEN-106)

A title bar runs across the window: the traffic lights at its left and search
(Command-K) in its middle. Under it, a narrow rail on the left edge replaces the
sidebar, as in Discord. From the top: the local terminal (back to the last terminal shown,
or a new shell; more shells open as tabs), one icon per queue with its "needs
you" count, New queue, and Settings at the bottom. Right-click a queue icon to
rename or delete it. Right of the rail are the queue column (header, New
request box and request list, resizable) and the thread beside it.
A posted request shows as a card under the box, which keeps focus.

No shell opens by itself: a terminal opens from the rail or from a thread
(its agent's peek card or View session), and Command-T adds a shell tab. Closing the last terminal returns to the queue;
if its group has surviving shell tabs, an existing tab is promoted without
restarting its process.
