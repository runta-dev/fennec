// Package browsersite holds the browser evaluation set's pages and task
// definitions (bench/sets/browser.md). Every page, brand and text here is
// our own and fictional; nothing copies or imitates a real site.
package browsersite

import "embed"

// FS holds site/ (the pages bench-pages serves) and tasks.json (the task
// definitions: instruction, setup and grader of each task).
//
//go:embed site tasks.json
var FS embed.FS
