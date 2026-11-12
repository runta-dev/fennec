package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const SeatbeltBinary = "/usr/bin/sandbox-exec"

// Available reports whether macOS Seatbelt enforcement is available on this host.
func Available() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	info, err := os.Stat(SeatbeltBinary)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0
}

// BuildProfile compiles a parameterized Apple Seatbelt (SBPL) profile and its
// -D key=value bindings for Policy. Paths are passed via -D parameters rather
// than raw string concatenation to prevent S-expression injection.
func BuildProfile(policy Policy, home string) (profile string, params []string) {
	mode := NormalizeMode(policy.Mode)
	if mode == ModeFull {
		return "", nil
	}

	var b strings.Builder
	var defs []string
	addParam := func(prefix, path string) string {
		key := fmt.Sprintf("%s_%d", prefix, len(defs))
		defs = append(defs, "-D", key+"="+path)
		return key
	}

	b.WriteString("(version 1)\n")
	b.WriteString("(deny default)\n")
	// Process lifecycle, IPC, system inspection, and standard Mach services needed by CLI tools.
	b.WriteString("(allow process-exec)\n")
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow process-info* (target same-sandbox))\n")
	b.WriteString("(allow signal (target same-sandbox))\n")
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow ipc-posix-shm*)\n")
	b.WriteString("(allow ipc-posix-sem*)\n")
	b.WriteString("(allow iokit-open)\n")
	b.WriteString("(allow pseudo-tty)\n")

	// Default read access to standard filesystems, overridden below by protected path denials.
	b.WriteString("(allow file-read*)\n")
	b.WriteString("(allow file-ioctl)\n")

	// Safe character devices always writable even in read-only mode.
	b.WriteString("(allow file-write* (literal \"/dev/null\") (literal \"/dev/zero\") (literal \"/dev/random\") (literal \"/dev/urandom\") (literal \"/dev/tty\") (literal \"/dev/dtracehelper\"))\n")

	if mode == ModeWorkspace {
		writeRoots := append([]string{}, SystemTempRoots()...)
		seen := map[string]bool{}
		for _, r := range writeRoots {
			seen[r] = true
		}
		var gitProtectedRoots []string
		for _, raw := range policy.WritableRoots {
			if raw == "" {
				continue
			}
			for _, candidate := range []string{filepath.Clean(raw), canonicalOrClean(raw)} {
				if filepath.IsAbs(candidate) && candidate != "/" && !seen[candidate] {
					seen[candidate] = true
					writeRoots = append(writeRoots, candidate)
					gitProtectedRoots = append(gitProtectedRoots, candidate)
				}
			}
		}
		for _, root := range writeRoots {
			param := addParam("WRITABLE_ROOT", root)
			fmt.Fprintf(&b, "(allow file-write* (subpath (param %q)))\n", param)
		}
		// Protect .git/hooks and .git/config inside writable workspaces against sandbox escape.
		for _, root := range gitProtectedRoots {
			hooksParam := addParam("GIT_HOOKS", filepath.Join(root, ".git/hooks"))
			cfgParam := addParam("GIT_CONFIG", filepath.Join(root, ".git/config"))
			fmt.Fprintf(&b, "(deny file-write* (subpath (param %q)) (literal (param %q)))\n", hooksParam, cfgParam)
		}
	}

	// High-priority blocklist: sensitive credentials, SSH keys, Keychains, LaunchAgents, and Fennec IPC/state.
	for _, protected := range ProtectedPaths(home) {
		param := addParam("PROTECTED", protected)
		fmt.Fprintf(&b, "(deny file-read* file-write* (subpath (param %q)))\n", param)
	}
	if home != "" {
		for _, rc := range []string{".zshrc", ".zshenv", ".zprofile", ".bashrc", ".bash_profile", ".profile"} {
			rcPath := canonicalOrClean(filepath.Join(home, rc))
			param := addParam("SHELL_RC", rcPath)
			fmt.Fprintf(&b, "(deny file-write* (literal (param %q)))\n", param)
		}
	}

	// Network policy.
	if policy.Network {
		b.WriteString("(allow network*)\n")
	} else {
		b.WriteString("(deny network*)\n")
		b.WriteString("(allow network-outbound (literal \"/private/var/run/mDNSResponder\"))\n")
	}

	return b.String(), defs
}

// Command wraps `/bin/sh -c shellCmd` inside macOS Seatbelt when available and
// Mode is not "full". Returns the configured *exec.Cmd and the effective sandbox
// mode ("read-only", "workspace", or "full").
func Command(ctx context.Context, policy Policy, home, workdir, shellCmd string) (*exec.Cmd, string) {
	mode := NormalizeMode(policy.Mode)
	if mode == ModeFull || !Available() {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", shellCmd)
		cmd.Dir = chooseWorkdir(policy, home, workdir)
		if !Available() {
			return cmd, ModeFull
		}
		return cmd, ModeFull
	}
	profile, params := BuildProfile(policy, home)
	args := make([]string, 0, len(params)+6)
	args = append(args, "-p", profile)
	args = append(args, params...)
	args = append(args, "--", "/bin/sh", "-c", shellCmd)
	cmd := exec.CommandContext(ctx, SeatbeltBinary, args...)
	cmd.Dir = chooseWorkdir(policy, home, workdir)
	return cmd, mode
}

func chooseWorkdir(policy Policy, home, preferred string) string {
	if preferred != "" {
		if info, err := os.Stat(preferred); err == nil && info.IsDir() {
			return preferred
		}
	}
	for _, root := range policy.WritableRoots {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return root
		}
	}
	if home != "" {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			return home
		}
	}
	return os.TempDir()
}
