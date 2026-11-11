package localssh

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type AliasRegistration struct {
	Alias   string `json:"alias"`
	Address string `json:"address"`
	Device  string `json:"device"`
	HostKey string `json:"host_key"`
	Default bool   `json:"default"`
}

func validAlias(s string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`).MatchString(s)
}

func configuredAliases(data []byte) []string {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && strings.EqualFold(fields[0], "Host") {
			return fields[1:]
		}
	}
	return nil
}

// RegisteredAlias returns only a name installed for this device, never a guessed hostname.
func RegisteredAlias(home, device string) string {
	fallback := "fennec-device-" + device
	if !regexp.MustCompile(`^[a-f0-9]{8,64}$`).MatchString(device) {
		return fallback
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "fennec", device+".conf"))
	if err != nil {
		return fallback
	}
	aliases := configuredAliases(data)
	owned := false
	for _, a := range aliases {
		if a == fallback {
			owned = true
		}
	}
	if owned && len(aliases) > 0 && validAlias(aliases[0]) {
		return aliases[0]
	}
	return fallback
}

// RegisterAlias serializes allocation on the shared machine. The config itself
// persists the chosen name, including collision suffixes, across reconnects.
func RegisterAlias(home string, r AliasRegistration) (string, error) {
	if _, _, err := AliasConfig(r.Alias, r.Address, r.Device, r.HostKey, r.Default); err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ssh", "fennec")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "aliases.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	files, err := filepath.Glob(filepath.Join(dir, "*.conf"))
	if err != nil {
		return "", err
	}
	used := map[string]bool{"machine": true}
	var old []string
	for _, file := range files {
		data, e := os.ReadFile(file)
		if e != nil {
			return "", e
		}
		names := configuredAliases(data)
		if filepath.Base(file) == r.Device+".conf" {
			old = names
			continue
		}
		for _, name := range names {
			used[strings.ToLower(name)] = true
		}
	}
	alias := r.Alias
	// Reuse this device's previously allocated suffix; migrate legacy ID aliases.
	if len(old) > 0 && (strings.EqualFold(old[0], r.Alias) || strings.HasPrefix(strings.ToLower(old[0]), strings.ToLower(r.Alias)+".")) && !used[strings.ToLower(old[0])] {
		alias = old[0]
	}
	for attempt := 0; used[strings.ToLower(alias)]; attempt++ {
		if attempt >= 64 {
			return "", fmt.Errorf("could not allocate a unique device alias")
		}
		var raw [4]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return "", err
		}
		length := 3
		if attempt >= 16 {
			length = 8
		}
		alias = r.Alias + "." + hex.EncodeToString(raw[:])[:length]
	}
	config, known, err := AliasConfig(alias, r.Address, r.Device, r.HostKey, r.Default)
	if err != nil {
		return "", err
	}
	// Retain old names so existing agent context keeps working.
	names := configuredAliases([]byte(config))
	for _, a := range old {
		found := false
		for _, n := range names {
			if strings.EqualFold(a, n) {
				found = true
			}
		}
		if !found && a != "machine" && validAlias(a) && !used[strings.ToLower(a)] {
			names = append(names, a)
		}
	}
	config = "Host " + strings.Join(names, " ") + config[strings.Index(config, "\n"):]
	if err = atomicAliasWrite(filepath.Join(dir, r.Device+".known_hosts"), []byte(known)); err != nil {
		return "", err
	}
	knownFiles, err := filepath.Glob(filepath.Join(dir, "*.known_hosts"))
	if err != nil {
		return "", err
	}
	var keys []byte
	for _, file := range knownFiles {
		b, e := os.ReadFile(file)
		if e != nil {
			return "", e
		}
		keys = append(keys, b...)
	}
	if err = atomicAliasWrite(filepath.Join(dir, "known_hosts"), keys); err != nil {
		return "", err
	}
	if err = atomicAliasWrite(filepath.Join(dir, r.Device+".conf"), []byte(config)); err != nil {
		return "", err
	}
	path := filepath.Join(home, ".ssh", "config")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	include := "Include ~/.ssh/fennec/*.conf"
	exists := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == include {
			exists = true
		}
	}
	if !exists {
		f, e := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			return "", e
		}
		_, e = fmt.Fprintln(f, "\n"+include)
		ce := f.Close()
		if e != nil {
			return "", e
		}
		if ce != nil {
			return "", ce
		}
	}
	return alias, nil
}
func atomicAliasWrite(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".alias-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
