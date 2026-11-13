// Package sandbox enforces macOS Seatbelt (sandbox-exec) process isolation and
// path boundaries for Fennec's local edge connector.
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	ModeReadOnly  = "read-only"
	ModeWorkspace = "workspace"
	ModeFull      = "full"

	PresetAsk  = "ask"
	PresetAuto = "auto"
	PresetFull = "full"
)

// Policy defines the technical boundary applied to commands and file operations
// arriving on the local Mac edge connector.
type Policy struct {
	Approval      string   `json:"approval,omitempty"` // "ask" | "auto" | "never"
	Mode          string   `json:"mode"`
	Network       bool     `json:"network"`
	WritableRoots []string `json:"writable_roots,omitempty"`
}

func DefaultPolicy() Policy {
	return Policy{
		Approval: PresetAsk,
		Mode:     ModeWorkspace,
		Network:  false,
	}
}

func ValidPreset(preset string) bool {
	switch strings.TrimSpace(strings.ToLower(preset)) {
	case "", PresetAsk, PresetAuto, PresetFull, "ask-for-approval", "approve-for-me", "full-access", "danger-full-access", ModeReadOnly, ModeWorkspace:
		return true
	default:
		return false
	}
}

func NormalizePreset(preset string) string {
	switch strings.TrimSpace(strings.ToLower(preset)) {
	case PresetAsk, "ask-for-approval", ModeReadOnly:
		return PresetAsk
	case PresetFull, "full-access", "danger-full-access":
		return PresetFull
	default:
		return PresetAuto
	}
}

// PolicyForPreset maps the three user-facing Codex-style permission modes
// ("ask", "auto", "full") onto concrete approval + Seatbelt boundaries.
func PolicyForPreset(preset string, writableRoots []string) Policy {
	switch NormalizePreset(preset) {
	case PresetAsk:
		return Policy{
			Approval:      PresetAsk,
			Mode:          ModeWorkspace,
			Network:       false,
			WritableRoots: writableRoots,
		}
	case PresetFull:
		return Policy{
			Approval:      "never",
			Mode:          ModeFull,
			Network:       true,
			WritableRoots: writableRoots,
		}
	default:
		return Policy{
			Approval:      PresetAuto,
			Mode:          ModeWorkspace,
			Network:       false,
			WritableRoots: writableRoots,
		}
	}
}

func ValidMode(mode string) bool {
	switch mode {
	case "", ModeReadOnly, ModeWorkspace, ModeFull, ModeEscalated:
		return true
	default:
		return false
	}
}

func NormalizeMode(mode string) string {
	trimmed := strings.TrimSpace(strings.ToLower(mode))
	switch trimmed {
	case ModeReadOnly:
		return ModeReadOnly
	case ModeFull, "full-access", "danger-full-access":
		return ModeFull
	case ModeEscalated:
		return ModeEscalated
	default:
		return ModeWorkspace
	}
}

// protectedMetadataPaths lists the read-only metadata inside a writable root.
func protectedMetadataPaths(root string) []string {
	var paths []string
	for _, name := range ProtectedMetadataNames {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err == nil || name == ".codex" {
			paths = append(paths, path)
		}
	}
	return paths
}

// NormalizeRoots validates, resolves symlinks where possible, deduplicates, and
// filters out unsafe root paths such as "/" or the user's entire home directory
// unless explicitly added.
func NormalizeRoots(roots []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, raw := range roots {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if !filepath.IsAbs(trimmed) || strings.ContainsAny(trimmed, "\x00\r\n") {
			return nil, fmt.Errorf("writable root must be an absolute path: %q", raw)
		}
		cleaned := canonicalOrClean(trimmed)
		if cleaned == "/" {
			return nil, fmt.Errorf("filesystem root '/' cannot be added as a workspace root; use Full access mode instead")
		}
		if !seen[cleaned] {
			seen[cleaned] = true
			out = append(out, cleaned)
		}
	}
	sort.Strings(out)
	return out, nil
}

func canonicalOrClean(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	// Resolve existing parent when leaf does not exist yet (for example SFTP upload target).
	parent := filepath.Dir(cleaned)
	if parent != cleaned {
		if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(resolvedParent, filepath.Base(cleaned))
		}
	}
	return cleaned
}

// ProtectedPaths returns high-sensitivity credential and control-plane paths on
// the local Mac that sandboxed edge commands must neither read nor modify.
func ProtectedPaths(home string) []string {
	if home == "" {
		return nil
	}
	base := canonicalOrClean(home)
	relatives := []string{
		".ssh",
		".gnupg",
		".aws",
		".azure",
		".kube",
		".config/gcloud",
		".config/gh",
		".config/fennec",
		".local/share/fennec",
		".local/state/fennec-cloud-lifecycle",
		"Library/Keychains",
		"Library/LaunchAgents",
	}
	seen := map[string]bool{}
	var out []string
	for _, rel := range relatives {
		for _, candidate := range []string{filepath.Join(home, rel), filepath.Join(base, rel)} {
			resolved := canonicalOrClean(candidate)
			if !seen[resolved] {
				seen[resolved] = true
				out = append(out, resolved)
			}
		}
	}
	return out
}

// SystemTempRoots returns canonical temporary directories permitted for scratch
// files in workspace mode.
func SystemTempRoots() []string {
	candidates := []string{"/tmp", "/private/tmp", os.TempDir()}
	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		for _, p := range []string{filepath.Clean(c), canonicalOrClean(c)} {
			if filepath.IsAbs(p) && p != "/" && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// GuardPath enforces Policy in user-space for non-exec subsystems such as SFTP.
func GuardPath(policy Policy, home, target string, write bool) error {
	mode := NormalizeMode(policy.Mode)
	if mode == ModeFull {
		return nil
	}
	if target == "" {
		return fmt.Errorf("path required")
	}
	absTarget := target
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(home, absTarget)
	}
	resolved := canonicalOrClean(absTarget)
	for _, protected := range ProtectedPaths(home) {
		if isWithin(resolved, protected) {
			return fmt.Errorf("sandbox denied access to protected path %s", protected)
		}
	}
	if !write {
		return nil
	}
	if mode == ModeReadOnly {
		return fmt.Errorf("sandbox is in read-only mode; file modification denied")
	}
	if mode == ModeEscalated {
		return nil
	}
	for _, rawRoot := range policy.WritableRoots {
		root := canonicalOrClean(rawRoot)
		for _, name := range ProtectedMetadataNames {
			if isWithin(resolved, filepath.Join(root, name)) {
				return fmt.Errorf("sandbox denied modification of protected %s metadata", name)
			}
		}
	}
	allowedRoots := append([]string{}, SystemTempRoots()...)
	for _, r := range policy.WritableRoots {
		allowedRoots = append(allowedRoots, canonicalOrClean(r))
	}
	permitted := false
	for _, root := range allowedRoots {
		if isWithin(resolved, root) {
			permitted = true
			break
		}
	}
	if !permitted {
		return fmt.Errorf("sandbox denied write outside authorized workspace roots")
	}
	return nil
}

func isWithin(target, root string) bool {
	if target == root {
		return true
	}
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// DetectSandboxViolation heuristically identifies Seatbelt / EPERM rejections
// from process exit status and stderr output so the audit log can highlight them.
func DetectSandboxViolation(mode string, exitCode int, stderr string) bool {
	if NormalizeMode(mode) == ModeFull || exitCode == 0 {
		return false
	}
	lower := strings.ToLower(stderr)
	for _, marker := range []string{
		"operation not permitted",
		"sandbox-exec:",
		"read-only file system",
		"network is down",
		"no route to host",
		"permission denied",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
