package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

// HelperCredentials delegates only the platform Keychain primitive. The signed
// helper remains responsible for verifying its caller before returning secrets.
type HelperCredentials struct{ Path string }

func (h HelperCredentials) operation(ctx context.Context, operation, token string) (string, error) {
	if !filepath.IsAbs(h.Path) {
		return "", fmt.Errorf("absolute credential helper path required")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	data, _ := json.Marshal(struct {
		Operation string `json:"operation"`
		Token     string `json:"token,omitempty"`
	}{operation, token})
	command := exec.CommandContext(ctx, h.Path)
	command.Stdin = bytes.NewReader(data)
	output, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("credential helper unavailable")
	}
	if err = command.Start(); err != nil {
		return "", fmt.Errorf("credential helper unavailable")
	}
	result, readErr := io.ReadAll(io.LimitReader(output, 65537))
	if readErr != nil || len(result) > 65536 {
		command.Process.Kill()
	}
	waitErr := command.Wait()
	if readErr != nil || waitErr != nil || len(result) > 65536 {
		return "", fmt.Errorf("credential helper failed")
	}
	var response struct {
		Status int    `json:"status"`
		Token  string `json:"token"`
	}
	if json.Unmarshal(result, &response) != nil {
		return "", fmt.Errorf("invalid credential helper response")
	}
	if response.Status == -25300 {
		return "", ErrNotFound
	}
	if response.Status != 0 {
		return "", fmt.Errorf("keychain status %d", response.Status)
	}
	return response.Token, nil
}
func (h HelperCredentials) Read(ctx context.Context) (string, error) {
	return h.operation(ctx, "read", "")
}
func (h HelperCredentials) Write(ctx context.Context, token string) error {
	_, err := h.operation(ctx, "write", token)
	return err
}
func (h HelperCredentials) Delete(ctx context.Context) error {
	_, err := h.operation(ctx, "delete", "")
	return err
}
