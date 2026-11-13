package chromium

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TempKeychain is a throwaway keychain for checks and tests: a fresh home
// directory whose only user keychain is a new, unlocked
// login.keychain-db. A process started with Env sees that keychain as its
// default and search list (and not the user's login keychain), so
// Chromium creates its Safe Storage item there. Nothing of the user's
// Keychain is read or changed.
type TempKeychain struct {
	Home string
	Path string
	Env  []string // HOME=...
}

// NewTempKeychain creates one in dir (which must be empty or absent). Its
// password is random and passed to security(1) on stdin, never in argv.
func NewTempKeychain(dir string) (*TempKeychain, error) {
	home, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	kdir := filepath.Join(home, "Library", "Keychains")
	if err := os.MkdirAll(kdir, 0o700); err != nil {
		return nil, err
	}
	k := &TempKeychain{Home: home, Path: filepath.Join(kdir, "login.keychain-db"), Env: []string{"HOME=" + home}}
	var pw [16]byte
	_, _ = rand.Read(pw[:])
	if err := k.security(fmt.Sprintf("create-keychain -p %s %s\n", hex.EncodeToString(pw[:]), quoteArg(k.Path))); err != nil {
		return nil, err
	}
	if _, err := os.Stat(k.Path); err != nil {
		return nil, fmt.Errorf("the temporary keychain was not created: %w", err)
	}
	return k, nil
}

func quoteArg(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// security runs security(1) in interactive mode with the commands on
// stdin, as the temporary home.
func (k *TempKeychain) security(commands string) error {
	cmd := exec.Command("security", "-i")
	cmd.Env = append(os.Environ(), k.Env...)
	cmd.Stdin = strings.NewReader(commands)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("security: %v: %s", err, out)
	}
	if strings.Contains(string(out), "security: ") || strings.Contains(string(out), "error") {
		return fmt.Errorf("security: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

var svcePattern = regexp.MustCompile(`"svce"<blob>="([^"]*)"`)

// Services lists the services of the generic passwords in the keychain
// (attributes only; no secret is read).
func (k *TempKeychain) Services() ([]string, error) {
	cmd := exec.Command("security", "dump-keychain", k.Path)
	cmd.Env = append(os.Environ(), k.Env...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("security dump-keychain: %w", err)
	}
	var s []string
	for _, m := range svcePattern.FindAllStringSubmatch(string(out), -1) {
		s = append(s, m[1])
	}
	sort.Strings(s)
	return s, nil
}

// Close deletes the keychain and the home directory.
func (k *TempKeychain) Close() error {
	err := k.security("delete-keychain " + quoteArg(k.Path) + "\n")
	if rerr := os.RemoveAll(k.Home); rerr != nil {
		err = errors.Join(err, rerr)
	}
	return err
}
