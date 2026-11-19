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
Write `project:fennec`, `request:42`, `settings:fennec:general` (or `knowledge`,
`queue`, `agents`), or `menu:tags|session` to `<output.png>.request` to capture
that view. Fixtures prove visual layout only, not live runtime behavior.

## Keyboard preferences

`~/.config/fennec/desktop-keys.json` accepts Herdr's `global` and `terminal`
key-to-action maps. The map is read when the native window starts; it does not
change Herdr's configuration. Supported actions include new/close/previous/next
and numbered tabs, previous/next and numbered session groups (`space.*`),
clipboard/search and `send:` editing keys, sidebar visibility, the session
search picker, and terminal zoom. Ctrl-B's prefix supports `s` for search, `.` for Fennec settings and
`b` for the sidebar; Escape cancels. Settings and Command-comma open the same
menu bar Preferences page through the app's local URL handler. Close-others asks for confirmation and
closes other shell tabs while keeping the group's agent anchor.

Fennec does not yet implement Herdr split panes, pane direction/swap/zoom,
layout switching, space creation/renaming, tab renaming, the command palette,
or Herdr's help surface. Their bindings are ignored, not swallowed
or silently assigned to different operations. Native macOS app quit/hide
shortcuts continue to follow the operating system.

A confirmed new tab selects its parent session, records its active tab and
transfers keyboard focus exactly once. Background board/session refreshes do
not reclaim focus. Collapsed sidebars put the restore button and window controls into the existing
header; terminal tabs stay in that same row. Double-clicking non-control
titlebar space zooms/restores the window. Re-activating a terminal window
restores focus to its active terminal, without stealing focus from project editors.
