//go:build !darwin

package runtime

import (
	"io/fs"
	"time"
)

// Only the Mac answers ChatGPT's file requests; elsewhere the modification
// time stands in for a birth time the platform does not report.
func createdAt(info fs.FileInfo) time.Time { return info.ModTime() }
