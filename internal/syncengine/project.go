package syncengine

import (
	"crypto/sha256"
	"encoding/hex"
	"fennec/internal/project"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type ProjectMapping struct {
	ID             string `json:"id"`
	LocalRoot      string `json:"local_root"`
	LocalDirectory string `json:"local_directory"`
	CloudRoot      string `json:"cloud_root"`
	CloudDirectory string `json:"cloud_directory"`
}

// MapProject preserves cwd semantics without exposing the Mac's absolute path
// in the guest. A device namespace prevents two Macs merging unrelated checkouts.
func MapProject(user, device, root, cwd, remoteHome string) (ProjectMapping, error) {
	if user == "" || device == "" || !path.IsAbs(remoteHome) || strings.ContainsAny(remoteHome, "\x00\r\n") {
		return ProjectMapping{}, fmt.Errorf("authenticated user, device and guest home required")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ProjectMapping{}, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return ProjectMapping{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ProjectMapping{}, err
	}
	if err = project.ValidateRoot(canonical, home); err != nil {
		return ProjectMapping{}, err
	}
	directory, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return ProjectMapping{}, err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return ProjectMapping{}, err
	}
	relative, err := filepath.Rel(canonical, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ProjectMapping{}, fmt.Errorf("cwd must remain in project")
	}
	digest := sha256.Sum256([]byte(user + "\x00" + device + "\x00" + canonical))
	id := hex.EncodeToString(digest[:16])
	cloud := path.Join(remoteHome, ".local/fennec/workspaces", id)
	return ProjectMapping{ID: id, LocalRoot: canonical, LocalDirectory: directory, CloudRoot: cloud, CloudDirectory: path.Join(cloud, filepath.ToSlash(relative))}, nil
}
