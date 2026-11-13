// Package benchpage holds the benchmark results page that cua-bench serves
// (cua-bench web) and exports (cua-bench export).
package benchpage

import _ "embed"

// Index is the single-file results page; it draws data.json beside it.
//
//go:embed index.html
var Index []byte
