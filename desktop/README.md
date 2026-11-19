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

Sessions are the core's terminals labeled `fennec.session`.
