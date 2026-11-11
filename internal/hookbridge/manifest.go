package hookbridge

import (
	"bytes"
	"encoding/json"
	"fennec/internal/sessionid"
	"fmt"
	"os"
	"path/filepath"
)

func directory(home string) string { return filepath.Join(home, ".local/fennec/hooks") }
func ManifestPath(home, id string) (string, error) {
	if !sessionid.Valid(id) {
		return "", fmt.Errorf("invalid hook session")
	}
	return filepath.Join(directory(home), id+".json"), nil
}
func SaveBindings(home, id string, bindings []Binding) error {
	if err := ValidateBindings(bindings); err != nil {
		return err
	}
	path, err := ManifestPath(home, id)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private hook directory required")
	}
	raw, _ := json.Marshal(bindings)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		old, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(old, raw) {
			return fmt.Errorf("session hook bindings changed")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(raw)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func LoadBindings(home, id string) ([]Binding, error) {
	path, err := ManifestPath(home, id)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return nil, fmt.Errorf("invalid hook manifest")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var bindings []Binding
	if json.Unmarshal(raw, &bindings) != nil {
		return nil, fmt.Errorf("invalid hook manifest")
	}
	return bindings, ValidateBindings(bindings)
}
func Lookup(home, session, reference string) (Binding, error) {
	bindings, err := LoadBindings(home, session)
	if err != nil {
		return Binding{}, err
	}
	for _, binding := range bindings {
		if binding.ID == reference {
			return binding, nil
		}
	}
	return Binding{}, fmt.Errorf("hook is not bound to this session")
}
