package installation

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// An installation moves the previous bundle into .fennec-update-*/Fennec.app
// and keeps it, so CLI clients still running from it keep their mapped
// binaries. Nothing removed those directories afterwards, nor the
// incoming.app of an installation that was interrupted: a Mac updated daily
// had 289 of them, 108 GB, in /Applications (2026-11-18). A stale
// incoming.app also made the Trash cleanup believe an update was running and
// skip forever.

// stagingActive is how recently a staging directory may have been written and
// still belong to an installation under way.
const stagingActive = 10 * time.Minute

// RunningExecutables lists the executable paths of running processes; a var
// so tests can replace it.
var RunningExecutables = func() []string {
	var out []byte
	if runtime.GOOS == "darwin" {
		out, _ = exec.Command("/bin/ps", "-axww", "-o", "comm=").Output()
	} else {
		entries, _ := os.ReadDir("/proc")
		var paths []string
		for _, entry := range entries {
			if target, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe")); err == nil {
				paths = append(paths, target)
			}
		}
		return paths
	}
	var paths []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); filepath.IsAbs(line) {
			paths = append(paths, line)
		}
	}
	return paths
}

func stagingDirs(parent string) []string {
	matches, _ := filepath.Glob(filepath.Join(parent, ".fennec-update-*"))
	return matches
}

func recentlyWritten(dir string, now time.Time) bool {
	recent := false
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, e := entry.Info(); e == nil && now.Sub(info.ModTime()) < stagingActive {
			recent = true
			return filepath.SkipAll
		}
		// The top two levels are enough: the work directory and its bundle.
		if entry.IsDir() && path != dir && filepath.Dir(path) != dir {
			return filepath.SkipDir
		}
		return nil
	})
	return recent
}

// UpdateInProgress reports an installation writing beside parent's bundle
// now: an incoming.app in a staging directory written within stagingActive.
func UpdateInProgress(parent string) bool {
	now := time.Now()
	for _, dir := range stagingDirs(parent) {
		if _, err := os.Lstat(filepath.Join(dir, "incoming.app")); err == nil && recentlyWritten(dir, now) {
			return true
		}
	}
	return false
}

// PruneStaging removes the staging directories beside parent's bundle that
// no running process executes from and that were not written within
// stagingActive. It returns the directories removed.
func PruneStaging(parent string) []string {
	dirs := stagingDirs(parent)
	if len(dirs) == 0 {
		return nil
	}
	running := RunningExecutables()
	now := time.Now()
	var removed []string
	for _, dir := range dirs {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || recentlyWritten(dir, now) {
			continue
		}
		inUse := false
		for _, path := range running {
			if strings.HasPrefix(path, dir+string(filepath.Separator)) {
				inUse = true
				break
			}
		}
		if inUse {
			continue
		}
		if os.RemoveAll(dir) == nil {
			removed = append(removed, dir)
		}
	}
	return removed
}
