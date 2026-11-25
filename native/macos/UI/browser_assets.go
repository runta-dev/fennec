package uiassets

import "embed"

// BrowserAssets shares the native panel's bundled xterm distribution.
//
//go:embed browser.html browser.js browser.css terminal-links.js vendor/xterm.js vendor/xterm.css vendor/addon-fit.js icons/*.svg
var BrowserAssets embed.FS

// Translations is the shared UI catalog, for copy the daemon itself writes
// (decode it with internal/localization).
//
//go:embed translations.json
var Translations []byte
