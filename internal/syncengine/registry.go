package syncengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Registration struct {
	Root          string `json:"root"`
	Session       string `json:"session"`
	DataDirectory string `json:"data_directory"`
	SSHDirectory  string `json:"ssh_directory"`
}

func Register(directory string, entry Registration) error {
	if !filepath.IsAbs(entry.Root) || !filepath.IsAbs(entry.DataDirectory) || !strings.HasPrefix(entry.Session, "fennec-") {
		return fmt.Errorf("invalid project registration")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(entry.DataDirectory + "\x00" + entry.Session))
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".registration-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, hex.EncodeToString(digest[:])+".json"))
}
func FindProject(directory, cwd string) (Registration, error) {
	current, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return Registration{}, err
	}
	current, err = filepath.Abs(current)
	if err != nil {
		return Registration{}, err
	}
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return Registration{}, err
	}
	var found []Registration
	depth := 0
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			return Registration{}, fmt.Errorf("invalid project registry file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Registration{}, err
		}
		var entry Registration
		if json.Unmarshal(data, &entry) != nil {
			return Registration{}, fmt.Errorf("invalid project registration")
		}
		root, err := filepath.EvalSymlinks(entry.Root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Registration{}, err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if len(root) > depth {
			found = nil
			depth = len(root)
		}
		if len(root) == depth {
			found = append(found, entry)
		}
	}
	if len(found) != 1 {
		return Registration{}, fmt.Errorf("expected one registered project, found %d; specify sync session explicitly", len(found))
	}
	return found[0], nil
}

func ListProjects(directory string) ([]Registration, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return nil, err
	}
	entries := []Registration{}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			return nil, fmt.Errorf("invalid project registry file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var entry Registration
		if json.Unmarshal(data, &entry) != nil || !filepath.IsAbs(entry.Root) || !filepath.IsAbs(entry.DataDirectory) || !strings.HasPrefix(entry.Session, "fennec-") {
			return nil, fmt.Errorf("invalid project registration")
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Unregister removes only the local registration; never project files.
func Unregister(directory, state, session string) error {
	if !filepath.IsAbs(state) || !strings.HasPrefix(session, "fennec-") {
		return fmt.Errorf("invalid registration identity")
	}
	digest := sha256.Sum256([]byte(state + "\x00" + session))
	err := os.Remove(filepath.Join(directory, hex.EncodeToString(digest[:])+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
