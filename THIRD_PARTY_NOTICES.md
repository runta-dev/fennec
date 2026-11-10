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

## xterm.js terminal renderer

- `@xterm/xterm` 5.5.0, MIT; license: `native/macos/UI/vendor/xterm-LICENSE`.
- `@xterm/addon-fit` 0.10.0, MIT; license: `native/macos/UI/vendor/addon-fit-LICENSE`.
- Distributed locally in the app; no CDN scripts are loaded.
