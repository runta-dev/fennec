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
