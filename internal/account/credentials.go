package account

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// ScopedCredentials multiplexes per-endpoint tokens inside the single Keychain
// item managed by fennec-credentials so switching between production and
// integration preserves both sessions without recompiling the signed helper or
// triggering additional Keychain ACL prompts.
type ScopedCredentials struct {
	mu    sync.Mutex
	Base  Credentials
	Scope func() string
}

const defaultProductionScope = "https://api.runta.com"

type scopedCredentialStore struct {
	Scoped bool              `json:"_fennec_scoped_v1"`
	Tokens map[string]string `json:"tokens"`
}

func (s *ScopedCredentials) currentScope() string {
	if s.Scope != nil {
		if v := strings.TrimRight(strings.TrimSpace(s.Scope()), "/"); v != "" {
			return v
		}
	}
	return defaultProductionScope
}

func decodeScopedTokens(raw string) map[string]string {
	tokens := map[string]string{}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return tokens
	}
	if strings.HasPrefix(trimmed, "{") {
		var envelope scopedCredentialStore
		if json.Unmarshal([]byte(trimmed), &envelope) == nil && envelope.Scoped {
			for k, v := range envelope.Tokens {
				if k != "" && v != "" {
					tokens[k] = v
				}
			}
			return tokens
		}
	}
	tokens[defaultProductionScope] = raw
	return tokens
}

func encodeScopedTokens(tokens map[string]string) (string, bool) {
	clean := map[string]string{}
	for k, v := range tokens {
		if k != "" && v != "" {
			clean[k] = v
		}
	}
	if len(clean) == 0 {
		return "", false
	}
	if len(clean) == 1 && clean[defaultProductionScope] != "" && !strings.HasPrefix(strings.TrimSpace(clean[defaultProductionScope]), "{") {
		return clean[defaultProductionScope], true
	}
	data, err := json.Marshal(scopedCredentialStore{Scoped: true, Tokens: clean})
	if err != nil {
		return "", false
	}
	return string(data), true
}

func (s *ScopedCredentials) Read(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.Base.Read(ctx)
	if err != nil {
		return "", err
	}
	token := decodeScopedTokens(raw)[s.currentScope()]
	if token == "" {
		return "", ErrNotFound
	}
	return token, nil
}

func (s *ScopedCredentials) Write(ctx context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens := map[string]string{}
	if raw, err := s.Base.Read(ctx); err == nil {
		tokens = decodeScopedTokens(raw)
	} else if !isNotFoundErr(err) {
		return err
	}
	tokens[s.currentScope()] = token
	encoded, ok := encodeScopedTokens(tokens)
	if !ok {
		return fmt.Errorf("encode scoped credentials failed")
	}
	return s.Base.Write(ctx, encoded)
}

func (s *ScopedCredentials) Delete(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.Base.Read(ctx)
	if err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		return err
	}
	tokens := decodeScopedTokens(raw)
	delete(tokens, s.currentScope())
	encoded, ok := encodeScopedTokens(tokens)
	if !ok {
		return s.Base.Delete(ctx)
	}
	return s.Base.Write(ctx, encoded)
}

func isNotFoundErr(err error) bool {
	return err == ErrNotFound
}

// FileCredentials stores tokens in an explicit 0600 file only when opted in via
// FENNEC_CREDENTIALS_FILE for isolated integration/E2E testing without touching
// the user's login Keychain.
type FileCredentials struct{ Path string }

func (f FileCredentials) Read(_ context.Context) (string, error) {
	info, err := os.Lstat(f.Path)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return "", fmt.Errorf("invalid isolated credential file")
	}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", ErrNotFound
	}
	return string(raw), nil
}

func (f FileCredentials) Write(_ context.Context, token string) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".cred-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(token); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}

func (f FileCredentials) Delete(_ context.Context) error {
	if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
