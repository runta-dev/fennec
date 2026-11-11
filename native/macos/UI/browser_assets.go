package uiassets

import "embed"

// BrowserAssets shares the native panel's bundled xterm distribution.
//
//go:embed browser.html browser.js browser.css terminal-links.js vendor/xterm.js vendor/xterm.css vendor/addon-fit.js icons/*.svg
var BrowserAssets embed.FS
