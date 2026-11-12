//go:build !darwin || !cgo

package record

// PlatformListen refuses: the event tap exists on macOS only.
var PlatformListen Listen = func(func(Input)) (func(), error) { return nil, ErrNoTap }
