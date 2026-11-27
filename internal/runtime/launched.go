package runtime

import (
	"os"
	"path/filepath"
)

// Launched reports whether a cloud session was ever launched: Launch records it
// before starting the terminal.
func (runtime *LocalRuntime) Launched(session string) bool {
	if !sessionIDPattern.MatchString(session) {
		return false
	}
	_, err := os.Stat(filepath.Join(runtime.Home, ".local/fennec/launches", session+".json"))
	return err == nil
}
