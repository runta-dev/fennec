package project

import _ "embed"

// Receiver validates the complete manifest and archive before publishing a
// fresh cloud directory; existing snapshots are never overwritten.
//
//go:embed assets/receive.py
var Receiver string
