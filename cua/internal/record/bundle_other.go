//go:build !darwin || !cgo

package record

// BundleStem answers nothing off macOS.
func BundleStem(int) string { return "" }
