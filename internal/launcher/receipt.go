package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// LaunchReceipt intentionally excludes tokens, prompts, environment and file data.
type LaunchReceipt struct {
	RuntimeID string `json:"runtime_id"`
	Address   string `json:"address"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
}

func SaveReceipt(directory string, receipt LaunchReceipt) error {
	if !filepath.IsAbs(directory) || !Supported[receipt.Agent] {
		return fmt.Errorf("invalid receipt location or agent")
	}
	if _, err := AttachArgs(receipt.RuntimeID, receipt.Address, receipt.SessionID, directory); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".launch-")
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
	return os.Rename(file.Name(), filepath.Join(directory, receipt.SessionID+".json"))
}
func ReadReceipt(directory, sessionID string) (LaunchReceipt, error) {
	if !regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`).MatchString(sessionID) {
		return LaunchReceipt{}, fmt.Errorf("invalid session ID")
	}
	file := filepath.Join(directory, sessionID+".json")
	info, err := os.Lstat(file)
	if err != nil {
		return LaunchReceipt{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return LaunchReceipt{}, fmt.Errorf("invalid receipt file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return LaunchReceipt{}, err
	}
	var receipt LaunchReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.SessionID != sessionID {
		return LaunchReceipt{}, fmt.Errorf("receipt identity mismatch")
	}
	if _, err = AttachArgs(receipt.RuntimeID, receipt.Address, receipt.SessionID, directory); err != nil {
		return LaunchReceipt{}, err
	}
	return receipt, nil
}
