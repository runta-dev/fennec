// Package macsign signs code with the Fennec signing identity for the
// standalone distribution (cmd/cua-dist). It follows cmd/cua-package's
// rules: the identity is looked up, never created; a signature that would
// wait on a Keychain dialog is stopped and reported; a real identity signs
// with the hardened runtime and a secure timestamp.
package macsign

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// DefaultIdentity is the development certificate's name
// (scripts/create-development-identity.sh).
const DefaultIdentity = "Fennec Local Development"

var (
	identityLine = regexp.MustCompile(`^\s*\d+\)\s+([0-9A-F]{40})\s+"([^"]+)"`)
	fingerprint  = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)
)

// Identity is a resolved signing identity.
type Identity struct {
	// Name is what the caller asked for; Hash the certificate's SHA-1, or
	// "-" for ad-hoc signing.
	Name, Hash string
	Keychain   string
	// Team is the certificate's team ("" for a self-signed one).
	Team string
}

// AdHoc reports ad-hoc signing.
func (id Identity) AdHoc() bool { return id.Hash == "-" }

// Label names the identity for logs and manifests.
func (id Identity) Label() string {
	if id.AdHoc() {
		return "ad-hoc"
	}
	if id.Team != "" {
		return id.Name + " (team " + id.Team + ")"
	}
	return id.Name
}

// Resolve finds the identity's certificate in the keychain and checks that
// it signs without a prompt. A self-signed development certificate is
// valid for signing although not trusted for distribution.
func Resolve(name, keychain string) (Identity, error) {
	id := Identity{Name: name, Keychain: keychain}
	switch {
	case name == "-":
		id.Hash = "-"
		return id, nil
	case fingerprint.MatchString(name):
		id.Hash = strings.ToUpper(name)
	default:
		out, err := exec.Command("/usr/bin/security", "find-identity", "-p", "codesigning", keychain).Output()
		if err != nil {
			return id, fmt.Errorf("security find-identity: %w", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if m := identityLine.FindStringSubmatch(line); m != nil && m[2] == name {
				id.Hash = m[1]
				break
			}
		}
		if id.Hash == "" {
			return id, fmt.Errorf("no signing identity %q in %s; create it with scripts/create-development-identity.sh (this command never creates one)", name, keychain)
		}
	}
	team, err := probe(id)
	if err != nil {
		return id, err
	}
	id.Team = team
	return id, nil
}

// probe signs a scratch copy of a small binary with a deadline and answers
// the identity's team.
func probe(id Identity) (string, error) {
	dir, err := os.MkdirTemp("", "macsign-probe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile("/usr/bin/true")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "probe")
	if err := os.WriteFile(p, data, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "--force", "--keychain", id.Keychain, "--sign", id.Hash, p).CombinedOutput()
	if ctx.Err() != nil {
		return "", errors.New("signing waited for a Keychain authorisation; allow codesign to use the Fennec signing key once (see skills/fennec-macos-development/SKILL.md) and run again")
	}
	if err != nil {
		return "", fmt.Errorf("codesign: %v: %s", err, out)
	}
	info, _ := exec.Command("/usr/bin/codesign", "-dv", p).CombinedOutput()
	for _, line := range strings.Split(string(info), "\n") {
		if t, ok := strings.CutPrefix(line, "TeamIdentifier="); ok && t != "not set" {
			return t, nil
		}
	}
	return "", nil
}

// AppleEvents is the hardened-runtime entitlement for sending Apple Events.
const AppleEvents = "com.apple.security.automation.apple-events"

// Sign signs path (a Mach-O file or a bundle, without --deep) with the
// identity. identifier, when set, is the code's identifier; entitlements
// are boolean hardened-runtime entitlements to grant.
func Sign(path string, id Identity, identifier string, entitlements ...string) error {
	args := []string{"--force"}
	if !id.AdHoc() {
		args = append(args, "--options", "runtime", "--timestamp", "--keychain", id.Keychain)
	}
	if len(entitlements) > 0 && !id.AdHoc() {
		f, err := os.CreateTemp("", "macsign-ent-*.plist")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err := f.Write(Entitlements(entitlements)); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		args = append(args, "--entitlements", f.Name())
	}
	if identifier != "" {
		args = append(args, "--identifier", identifier)
	}
	args = append(args, "--sign", id.Hash, path)
	if out, err := exec.Command("/usr/bin/codesign", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign %s: %v: %s", path, err, out)
	}
	return nil
}

// VerifyDeep checks a bundle's signature and everything nested in it.
func VerifyDeep(path string) error {
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", path).CombinedOutput(); err != nil {
		return fmt.Errorf("the signature of %s does not verify: %v: %s", path, err, out)
	}
	return nil
}

// Entitlements is a property list granting each key.
func Entitlements(keys []string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n<plist version=\"1.0\"><dict>\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "<key>%s</key><true/>\n", k)
	}
	b.WriteString("</dict></plist>\n")
	return []byte(b.String())
}
