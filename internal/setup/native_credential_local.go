package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/text/unicode/norm"
)

// NativeCredentialLocation is where the Mac agent keeps one credential file:
// a Keychain item, a file, or both (Claude Code falls back to the file when
// it has no Keychain item). It never holds the credential itself.
type NativeCredentialLocation struct {
	Name     string
	File     string
	Lock     string // the agent's own lock file, held while writing the file
	Service  string
	Account  string
	ReadOnly bool // stored where Fennec does not write (Codex's own keyring)
}

// ErrNativeCredentialChanged reports that the Mac copy changed between the
// decision and the write; nothing was written and the next change decides.
var ErrNativeCredentialChanged = errors.New("native credential changed during update")

// ErrNativeKeychainDenied reports that macOS refused the Keychain update.
// Fennec never retries in a way that could prompt the user again.
var ErrNativeKeychainDenied = errors.New("Keychain update not allowed")

// NativeCredentialStore reads and writes the Mac agents' own credentials.
type NativeCredentialStore struct {
	Source NativeSource
	// KeychainWrite stores a Keychain item; tests replace it.
	KeychainWrite func(ctx context.Context, service, account string, data []byte) error
	// KeychainModified returns an item's modification stamp without reading
	// its secret; tests replace it.
	KeychainModified func(ctx context.Context, service, account string) (string, error)
}

// NewNativeCredentialStore uses the same environment and Keychain reader as
// preparation, so it reads exactly what a preparation sync would send.
func NewNativeCredentialStore(ctx context.Context, home string) NativeCredentialStore {
	env := nativeSourceEnvironment(ctx)
	return NativeCredentialStore{
		Source:           NativeSource{Home: home, Env: func(k string) string { return env[k] }, Keychain: readNativeKeychain},
		KeychainWrite:    writeNativeKeychain,
		KeychainModified: nativeKeychainModified,
	}
}

func claudeKeychainService(s NativeSource, dir string) string {
	service := "Claude Code-credentials"
	if s.Env("CLAUDE_CONFIG_DIR") != "" {
		sum := sha256.Sum256([]byte(norm.NFC.String(dir)))
		service += "-" + hex.EncodeToString(sum[:])[:8]
	}
	return service
}

func codexKeychainAccount(dir string) string {
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		canonical = dir
	}
	sum := sha256.Sum256([]byte(canonical))
	return "cli|" + hex.EncodeToString(sum[:])[:16]
}

// Locations lists where each agent keeps its credential on this Mac.
func (st NativeCredentialStore) Locations(agents []string) []NativeCredentialLocation {
	s := st.Source
	if s.Env == nil {
		s.Env = func(string) string { return "" }
	}
	var out []NativeCredentialLocation
	for _, agent := range agents {
		switch agent {
		case "claude":
			dir := s.dir("CLAUDE_CONFIG_DIR", ".claude")
			location := NativeCredentialLocation{Name: "claude/.credentials.json", File: filepath.Join(dir, ".credentials.json")}
			if s.Keychain != nil {
				location.Service, location.Account = claudeKeychainService(s, dir), nativeKeychainAccount(s.Env("USER"))
			}
			out = append(out, location)
		case "codex":
			dir := s.dir("CODEX_HOME", ".codex")
			location := NativeCredentialLocation{Name: "codex/auth.json", File: filepath.Join(dir, "auth.json"), Lock: filepath.Join(dir, "auth.lock")}
			config := map[string]any{}
			if b, err := readNativeFile(filepath.Join(dir, "config.toml")); err == nil {
				_ = toml.Unmarshal(b, &config)
			}
			mode, _ := config["cli_auth_credentials_store"].(string)
			if (mode == "keyring" || mode == "auto") && s.Keychain != nil {
				location.Service, location.Account = "Codex Auth", codexKeychainAccount(dir)
				location.ReadOnly = true
			}
			if mode == "keyring" || mode == "ephemeral" {
				location.File, location.Lock = "", ""
			}
			out = append(out, location)
		case "pi":
			out = append(out, NativeCredentialLocation{Name: "pi/auth.json", File: filepath.Join(s.dir("PI_CODING_AGENT_DIR", ".pi/agent"), "auth.json")})
		case "kimi":
			out = append(out, NativeCredentialLocation{Name: kimiCredentialFile, File: filepath.Join(s.dir(kimiHomeEnv, kimiHomeDefault), "credentials", "kimi-code.json")})
		case "opencode":
			out = append(out, NativeCredentialLocation{Name: "opencode/data/opencode/auth.json", File: filepath.Join(s.dir("XDG_DATA_HOME", ".local/share"), "opencode/auth.json")})
		}
	}
	return out
}

// WatchPaths lists the files whose changes may mean a credential changed: the
// agents' auth files and, for Keychain items, the login keychain database.
func (st NativeCredentialStore) WatchPaths(locations []NativeCredentialLocation) []string {
	var paths []string
	keychain := false
	for _, location := range locations {
		if location.File != "" {
			paths = append(paths, location.File)
		}
		keychain = keychain || location.Service != ""
	}
	if keychain {
		paths = append(paths, filepath.Join(st.Source.Home, "Library/Keychains/login.keychain-db"))
	}
	return paths
}

// Modified returns a cheap stamp of the location's current state that changes
// whenever its credential changes: the Keychain item's modification date
// (attributes only, never the secret, so it never prompts) and the file's
// signature. Reading the credential is left for when the stamp changes.
func (st NativeCredentialStore) Modified(ctx context.Context, location NativeCredentialLocation) string {
	var stamp strings.Builder
	if location.Service != "" && st.KeychainModified != nil {
		modified, err := st.KeychainModified(ctx, location.Service, location.Account)
		if err != nil {
			modified = "absent"
		}
		stamp.WriteString(modified)
	}
	if location.File != "" {
		signature := statSignature(location.File)
		fmt.Fprintf(&stamp, "|%t:%d:%d:%d:%d", signature.exists, signature.inode, signature.size, signature.modified, signature.chg)
	}
	return stamp.String()
}

// Gather returns the copy a preparation sync would send for name, or nil when
// the Mac has none. It reads exactly as preparation does (same Keychain
// access, same validation), so it prompts no more than preparation.
func (st NativeCredentialStore) Gather(ctx context.Context, name string) ([]byte, error) {
	agent := NativeCredentialAgent(name)
	if agent == "" {
		return nil, fmt.Errorf("unsupported native credential file")
	}
	request, err := st.Source.Gather(ctx, []string{agent})
	if err != nil {
		return nil, err
	}
	encoded := request.Files[name]
	if encoded == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(encoded)
}

// Read returns the Mac copy the agent itself reads and where it lives, or
// os.ErrNotExist when the agent keeps none. The Keychain item comes first,
// like the agents and Gather.
func (st NativeCredentialStore) Read(ctx context.Context, name string) ([]byte, NativeCredentialLocation, error) {
	locations := st.Locations([]string{NativeCredentialAgent(name)})
	if len(locations) != 1 {
		return nil, NativeCredentialLocation{}, os.ErrNotExist
	}
	location := locations[0]
	if location.Service != "" && st.Source.Keychain != nil {
		data, err := st.Source.Keychain(ctx, location.Service, location.Account)
		if err == nil && len(data) > 0 {
			location.File, location.Lock = "", ""
			return data, location, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, location, err
		}
	}
	location.Service, location.Account, location.ReadOnly = "", "", false
	if location.File == "" {
		return nil, location, os.ErrNotExist
	}
	data, err := readNativeFile(location.File)
	return data, location, err
}

// Write replaces the Mac copy with data only if it still equals expected.
// Files are replaced atomically under the agent's own lock when it has one;
// Keychain items are updated in place, keeping their access control.
func (st NativeCredentialStore) Write(ctx context.Context, location NativeCredentialLocation, expected, data []byte) error {
	if location.ReadOnly {
		return fmt.Errorf("native credential store is read-only")
	}
	if location.Service != "" {
		if st.Source.Keychain == nil || st.KeychainWrite == nil {
			return fmt.Errorf("native Keychain unavailable")
		}
		current, err := st.Source.Keychain(ctx, location.Service, location.Account)
		if err != nil || !bytes.Equal(current, expected) {
			return ErrNativeCredentialChanged
		}
		return st.KeychainWrite(ctx, location.Service, location.Account, data)
	}
	if location.File == "" {
		return os.ErrNotExist
	}
	if location.Lock != "" {
		lock, err := os.OpenFile(location.Lock, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := flockContext(ctx, lock); err != nil {
			return err
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	info, err := os.Lstat(location.File)
	if err != nil || !info.Mode().IsRegular() {
		return ErrNativeCredentialChanged
	}
	err = atomicCredential(location.File, data, credentialWrite{path: location.File, previous: expected, existed: true})
	if errors.Is(err, errCredentialChanged) {
		return ErrNativeCredentialChanged
	}
	if err == nil && info.Mode().Perm() != 0600 {
		_ = os.Chmod(location.File, info.Mode().Perm())
	}
	return err
}

func flockContext(ctx context.Context, lock *os.File) error {
	for {
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

var keychainModifiedPattern = regexp.MustCompile(`"mdat"<timedate>=0x([0-9A-F]+)`)

// nativeKeychainModified reads only the item's attributes. macOS shows these
// without the access check that guards the secret, so it never prompts.
func nativeKeychainModified(ctx context.Context, service, account string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", os.ErrNotExist
	}
	step, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := []string{"find-generic-password", "-s", service}
	if account != "" {
		args = append(args, "-a", account)
	}
	output, err := exec.CommandContext(step, "/usr/bin/security", args...).Output()
	if err != nil {
		return "", os.ErrNotExist
	}
	match := keychainModifiedPattern.FindSubmatch(output)
	if match == nil {
		return "", fmt.Errorf("Keychain item has no modification date")
	}
	return string(match[1]), nil
}

// securityLineLimit is the longest command line `security -i` reads intact;
// longer lines are cut, so they are never sent.
const securityLineLimit = 4000

var keychainNamePattern = regexp.MustCompile(`^[A-Za-z0-9 ._|@-]{1,128}$`)

// writeNativeKeychain updates a generic-password item the way the agents
// themselves create it, through /usr/bin/security, so the item's access
// list (which trusts /usr/bin/security) allows it without a prompt. The
// secret goes through stdin, never argv. The result is read back and
// compared; a failed write leaves the previous value.
func writeNativeKeychain(ctx context.Context, service, account string, data []byte) error {
	if runtime.GOOS != "darwin" {
		return os.ErrNotExist
	}
	if !keychainNamePattern.MatchString(service) || !keychainNamePattern.MatchString(account) {
		return fmt.Errorf("invalid Keychain item name")
	}
	line := fmt.Sprintf("add-generic-password -U -a \"%s\" -s \"%s\" -X %s\n", account, service, hex.EncodeToString(data))
	if len(line) > securityLineLimit {
		return fmt.Errorf("native credential too large for a Keychain update")
	}
	step, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(step, "/usr/bin/security", "-i")
	command.Stdin = strings.NewReader(line)
	command.Stdout, command.Stderr = nil, nil
	if err := command.Run(); err != nil {
		return ErrNativeKeychainDenied
	}
	current, err := readNativeKeychain(ctx, service, account)
	if err != nil || !bytes.Equal(current, data) {
		return ErrNativeKeychainDenied
	}
	return nil
}
