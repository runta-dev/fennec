package launcher

import (
	"fennec/internal/runta"
	"os"
	"path/filepath"
	"strings"
)

// SSHTransportOptions returns low-latency OpenSSH client flags, using fd-passing
// with TCP_NODELAY=1 when enabled so non-PTY ControlMaster (-N -M) and stream (-T)
// connections bypass Nagle buffering from the first packet.
func SSHTransportOptions(home string) []string {
	opts := []string{"-o", "IPQoS=ef"}
	if !runta.SSHNoDelayEnabled(home) {
		return opts
	}
	exe, err := os.Executable()
	if err != nil || !filepath.IsAbs(exe) || strings.ContainsAny(exe, "\x00\r\n%") {
		return opts
	}
	base := filepath.Base(exe)
	if base != "fen" && base != "fennec-daemon" {
		return opts
	}
	quoted := "'" + strings.ReplaceAll(exe, "'", "'\"'\"'") + "'"
	return append(opts, "-o", "ProxyUseFdpass=yes", "-o", "ProxyCommand="+quoted+" ssh-dial %h %p")
}
