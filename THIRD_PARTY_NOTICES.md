# Third-party notices

Fennec.app carries this file and the licence texts below in `Contents/Resources/licenses/`; Settings, Licenses opens them.

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

- Source: open-source Chromium snapshot builds from the public `chromium-browser-snapshots` bucket, pinned in `cua/internal/chromium/pin.go` (macOS arm64 r1743750, commit `2c8bee4ceb39a8f0ebf674b3327138b5e0ebea57`, 160.0.8159.0; Fennec ships for Apple silicon only), verified by SHA-256 before use. Bundled in Fennec.app as `Contents/Helpers/Chromium.app`. Not Google Chrome and not Chrome for Testing.
- License: BSD-3-Clause (Copyright The Chromium Authors), plus the licences of the third-party components Chromium includes. Snapshot builds ship only a sample `chrome://credits`. `cua/internal/credits` therefore generates the notices an official macOS build carries, from the pinned commit and the dependencies its `DEPS` pins, applying the rules of Chromium's `tools/licenses/licenses.py` in Go. The result has 473 components, with SHA-256 `085e2ec9c87be3a957497969189e3c8e44534ba622bf833b31617aec32e7d711` (pinned in `pin.go`). `cua/cmd/cua-package` stages Chromium's `LICENSE` and the notices as `licenses/Chromium-LICENSE.txt`, `licenses/Chromium-credits.html` and `licenses/Chromium-credits.txt`, and copies them into `Chromium.app/Contents/Resources`. It also puts the page in place of the sample `chrome://credits` resource.
- Modified only by `cua-package`: the `chrome://credits` resource in `resources.pak` is replaced with the generated notices; the cookie key's Keychain item name in the framework changes from "Chromium Safe Storage" to "Fennec Safe Storage"; and the app is re-signed with Fennec's signing identity. A staging made with `-allow-missing-credits` (development only) carries no notices and must not be distributed.
- The DevTools protocol client in `cua/internal/cdp` is Fennec's own code; no chromedp or cdproto code is included.
