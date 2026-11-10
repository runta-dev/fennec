package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateRoot prevents automatic whole-device or whole-home synchronization.
// Resolve symlinks first so an alias cannot bypass the boundary.
func ValidateRoot(root, home string) error {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("project root must be a directory")
	}
	if filepath.Dir(canonical) == canonical {
		return fmt.Errorf("filesystem root cannot be synchronized")
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("cannot verify home directory boundary")
	}
	canonicalHome, err = filepath.Abs(canonicalHome)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(canonical, canonicalHome)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("home directory or its ancestor cannot be synchronized")
	}
	// macOS volume roots are not necessarily ancestors of HOME.
	if filepath.Dir(canonical) == "/Volumes" || canonical == "/Volumes" {
		return fmt.Errorf("volume root cannot be synchronized")
	}
	return nil
}
