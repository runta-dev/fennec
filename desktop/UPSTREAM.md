# herdr-app in Fennec's desktop window

Fennec's desktop window is built on [herdr-app](https://github.com/herdrdev/herdr-app)
(the Herdr desktop app), Apache-2.0, by its authors. Its terminal core (libghostty-vt
through `ghostty-sys`, the GPUI terminal view, the herdr-core client) and its GPUI
patches are kept exactly as herdr-app ships them, so terminals draw and behave as in
Herdr. Fennec writes the window around them: projects, requests, sessions, and the
small modules the terminal core calls (`src/settings.rs`, `src/mods.rs`,
`src/keys.rs`, `src/core_bin.rs`).

- Source: herdr-app `317e673f1f1568b876cbaa0b41dabeb9153ecbea` (v0.1.16)
- License: [LICENSE](LICENSE) (Apache-2.0), with herdr-app's
  [THIRD-PARTY-NOTICES](THIRD-PARTY-NOTICES) and [licenses/](licenses/).
- Ghostty: submodule `vendor/ghostty` at the commit herdr-app pins.
- herdr-core: submodule `vendor/herdr-core` at v0.1.21 (Apache-2.0), built
  unchanged and bundled in Fennec.app for local shell tabs.

`src/settings.rs` carries herdr-app's palettes, color derivation and font handling,
modified for Fennec (system appearance, no settings.json, Fennec's colors); its header
says so. Every other Fennec file is new.

## Kept verbatim

Never edit or reformat these here (`scripts/fmt` formats Fennec's own files only): fix them upstream, or take herdr-app's fix with
`scripts/herdr-core update <herdr-app checkout>`. `scripts/herdr-core check
<checkout>` lists any that differ.

```verbatim
src/terminal.rs
src/terminal
src/vt.rs
src/hc.rs
src/links.rs
src/sprites.rs
src/spawn.rs
src/bench.rs
src/process_cwd.rs
src/win32_keys.rs
crates/ghostty-sys
vendor/gpui-pre
vendor/gpui-pre-apple
vendor/gpui-pre-macos
vendor/gpui-pre-linux
vendor/gpui-pre-wgpu
vendor/gpui-kit
vendor/taffy
vendor/zed-xim
vendor/xim-ctext
assets/fonts
distribution/core-version
```

The terminal chrome icon in `assets/icons/terminal.svg` is copied verbatim
from Herdr's `mods/workspace-chrome/terminal.svg` (Apache-2.0). Terminal padding
is 14 px horizontally and 12 px vertically, matching the installed Herdr
`layouts/main.json`; the surrounding Fennec chrome follows the desktop design.

Monochrome Pi, OpenCode, Cursor and xAI SVGs are copied from the installed
Herdr icon library and tinted with the active text color so they remain visible
in both appearances; their LobeHub license is in `licenses/LobeHub-icons.txt`.

## Program identity and attention

The Ink Dark palette and derived chrome tokens match the pinned upstream.
`assets/programs/` copies its `defaults/icons/` and workspace spinner. The program
catalog and bundled notification audio are copied into `internal/desktopactivity/assets/`.
The Go adapter follows upstream `programs.rs` resolution and `model.rs` attention
rules, with native event delivery, shared `herdr.seen` acknowledgements and clickable
notifications. Shell titles never determine agent identity. The daemon is local;
recognition does not require cloud availability or model inference.

Desktop notification sounds have their own switch in desktop settings. Optional
custom files are configured by `~/.config/fennec/desktop-sounds.json` (`done`,
`blocked`, paths relative to the Fennec configuration directory or absolute).
Missing or unplayable custom files fall back to the bundled Herdr sounds.

The context menus use Herdr's 31 px rows, 14 px icons, 5 px padding and theme
roles. `assets/icons/context-rename.svg` is its workspace-chrome `square-pen.svg`.
Projects, sessions and terminal tabs accept right-click or Control-click; menu
items retain the clicked target independently of the current selection.

Local graphical shells clear inherited `NO_COLOR` before entering the user's
login shell, including when an existing core was started by a noninteractive
process. Agent routing still follows the menubar preference. Uploaded project
artwork is clipped by the image renderer itself, using the same corner radius
as the surrounding project mark.
