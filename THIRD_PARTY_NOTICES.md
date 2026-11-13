# Third-party notices

## Magpie

Source: https://github.com/yetone/magpie
Revision: 1615e5a149acbaed6e9395ddb9775f96d95e4728
License: MIT, reproduced in `docs/licenses/magpie-MIT.txt` and the app bundle.

Copied unchanged from `internal/gui/assets/icons/`:
- `claudecode-color.svg`
- `codex-color.svg`

Compact panel colors and layout metrics are adapted from `internal/gui/assets/app.css`: 46px toolbar, 40px agent rows, 18px agent icons, 12.5px navigation and restrained separators. Fennec retains Runta orange and its own product behavior. Agent brand marks identify the corresponding products; no affiliation is implied.

Additional Agent SVGs copied from Magpie at revision
`218b38c5962137e8384d078b7e5457fdfc33dc2c`:
`pi.svg`, `kimi.svg`, `claude-color.svg`, `opencode.svg`, `devin.svg`,
`xai.svg`, `geminicli-color.svg`, `cursor.svg`, `copilot-color.svg`.
Detection behavior references `internal/agent/agent.go` (Detected/binAt),
`kimi.go`, `devin.go` and `grok.go`; Fennec uses its own metadata-only detector.
Auto-updater background staging, non-`.app` payload staging, and quiet header pill interaction in `internal/updater/` and `native/macos/UI/updater.js` are adapted from Magpie's updater design under the MIT license.

## xterm.js terminal renderer

- `@xterm/xterm` 5.5.0, MIT; license: `native/macos/UI/vendor/xterm-LICENSE`.
- `@xterm/addon-fit` 0.10.0, MIT; license: `native/macos/UI/vendor/addon-fit-LICENSE`.
- Distributed locally in the app; no CDN scripts are loaded.

## Chromium (fennec-cua's agent browser)

- Source: open-source Chromium snapshot builds from the public `chromium-browser-snapshots` bucket, pinned in `cua/internal/chromium/pin.go` (macOS arm64 r1743750, commit `2c8bee4ceb39a8f0ebf674b3327138b5e0ebea57`; macOS x64 r1743759, commit `e085decaed8260097cb09cf29dd090433cc6040c`; both 160.0.8159.0), each verified by SHA-256 before use. Not Google Chrome and not Chrome for Testing.
- License: BSD-3-Clause (Copyright The Chromium Authors), plus the licences of the third-party components Chromium includes. `cua/cmd/cua-package` stages Chromium's `LICENSE` from the pinned commit and the build's `chrome://credits` page as `licenses/Chromium-LICENSE.txt` and `licenses/Chromium-credits.html`, and copies both into `Chromium.app/Contents/Resources`.
- Distributed unmodified apart from re-signing with Fennec's signing identity. Snapshot builds carry no generated `chrome://credits`; such a staging is marked development-only and must not be distributed until a build with generated third-party notices is pinned.
- The DevTools protocol client in `cua/internal/cdp` is Fennec's own code; no chromedp or cdproto code is included.
