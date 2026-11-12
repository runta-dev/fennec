// Package vm drives the tart macOS VM that fennec-cua's desktop tests run in,
// so no test ever clicks or types on a developer's own desktop.
package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Defaults for the development VM.
const (
	DefaultName  = "fennec-cua-dev"
	DefaultImage = "ghcr.io/cirruslabs/macos-tahoe-base:latest"
	// The cirruslabs base images ship this account with passwordless sudo
	// and automatic login to the desktop.
	DefaultUser     = "admin"
	DefaultPassword = "admin"
)

// VM is one tart VM.
type VM struct {
	Name     string
	User     string
	Password string
	// Tart is the tart binary; empty means "tart" from PATH.
	Tart string
}

// New returns the default development VM.
func New() *VM {
	return &VM{Name: DefaultName, User: DefaultUser, Password: DefaultPassword}
}

func (v *VM) tart(ctx context.Context, args ...string) (string, error) {
	bin := v.Tart
	if bin == "" {
		bin = "tart"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", errors.New("tart is not installed: get tart.tar.gz from https://github.com/cirruslabs/tart/releases")
	}
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tart %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// Exists reports whether the VM has been created.
func (v *VM) Exists(ctx context.Context) (bool, error) {
	out, err := v.tart(ctx, "list", "--quiet")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == v.Name {
			return true, nil
		}
	}
	return false, nil
}

// Create clones the VM from image. The first clone downloads the image
// (tens of GB); later clones are copy-on-write.
func (v *VM) Create(ctx context.Context, image string) error {
	if ok, err := v.Exists(ctx); err != nil || ok {
		return err
	}
	if image == "" {
		image = DefaultImage
	}
	_, err := v.tart(ctx, "clone", image, v.Name)
	return err
}

// Start boots the VM without a window and waits until SSH answers. The VM
// keeps running after Start returns; Stop shuts it down.
func (v *VM) Start(ctx context.Context) (string, error) {
	if ip, err := v.IP(ctx); err == nil && ip != "" {
		if v.sshReady(ip) {
			return ip, nil
		}
	}
	bin := v.Tart
	if bin == "" {
		bin = "tart"
	}
	cmd := exec.Command(bin, "run", "--no-graphics", v.Name)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		if ip, err := v.IP(ctx); err == nil && ip != "" && v.sshReady(ip) {
			return ip, nil
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", errors.New("the VM did not come up within 4 minutes")
}

// IP is the VM's address.
func (v *VM) IP(ctx context.Context) (string, error) {
	return v.tart(ctx, "ip", "--wait", "1", v.Name)
}

// Stop shuts the VM down.
func (v *VM) Stop(ctx context.Context) error {
	_, err := v.tart(ctx, "stop", v.Name)
	return err
}

func (v *VM) sshReady(ip string) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "22"), 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// Session is an SSH connection to the VM.
type Session struct {
	client *ssh.Client
}

// Connect opens an SSH connection. The VM is a throwaway local guest, so its
// host key is not pinned.
func (v *VM) Connect(ctx context.Context) (*Session, error) {
	ip, err := v.IP(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            v.User,
		Auth:            []ssh.AuthMethod{ssh.Password(v.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(ip, "22"), cfg)
	if err != nil {
		return nil, err
	}
	return &Session{client: client}, nil
}

// Close ends the connection.
func (s *Session) Close() error { return s.client.Close() }

// Run runs a shell command and returns its combined output.
func (s *Session) Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) error {
	sess, err := s.client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	sess.Stdin, sess.Stdout, sess.Stderr = stdin, stdout, stderr
	done := make(chan error, 1)
	go func() { done <- sess.Run(command) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return ctx.Err()
	}
}

// Output runs a command and returns its stdout.
func (s *Session) Output(ctx context.Context, command string) (string, error) {
	var out, errb bytes.Buffer
	if err := s.Run(ctx, command, nil, &out, &errb); err != nil {
		return out.String(), fmt.Errorf("%s: %v: %s", command, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Push copies local files or directories to dest in the VM through tar.
func (s *Session) Push(ctx context.Context, dest string, paths ...string) error {
	tarCmd := exec.CommandContext(ctx, "tar", append([]string{"-czf", "-", "--no-xattrs"}, paths...)...)
	stream, err := tarCmd.StdoutPipe()
	if err != nil {
		return err
	}
	var terr bytes.Buffer
	tarCmd.Stderr = &terr
	if err := tarCmd.Start(); err != nil {
		return err
	}
	var errb bytes.Buffer
	runErr := s.Run(ctx, "mkdir -p "+Quote(dest)+" && tar -xzf - -C "+Quote(dest), stream, io.Discard, &errb)
	waitErr := tarCmd.Wait()
	if runErr != nil {
		return fmt.Errorf("unpack in VM: %v: %s", runErr, strings.TrimSpace(errb.String()))
	}
	if waitErr != nil {
		return fmt.Errorf("tar: %v: %s", waitErr, strings.TrimSpace(terr.String()))
	}
	return nil
}

// Quote quotes a string for the VM's shell.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// InGUI wraps a command so it runs in the logged-in user's GUI session,
// where the accessibility API and the window server are reachable.
func InGUI(command string) string {
	return "sudo launchctl asuser \"$(id -u)\" sudo -u \"$(id -un)\" /bin/sh -c " + Quote(command)
}

// GrantTCC grants Accessibility and Screen Recording to a client: a bundle id
// (client type 0) or an absolute executable path (client type 1). It edits
// the system TCC database, which needs SIP disabled in the VM, and restarts
// tccd so the grant takes effect.
func (s *Session) GrantTCC(ctx context.Context, client string) error {
	typ := 0
	if strings.HasPrefix(client, "/") {
		typ = 1
	}
	if sip, err := s.Output(ctx, "csrutil status"); err == nil && strings.Contains(sip, "enabled.") {
		return errors.New("SIP is enabled in the VM; TCC grants need it disabled (boot the VM with tart run --recovery and run csrutil disable)")
	}
	var sql strings.Builder
	for _, service := range []string{"kTCCServiceAccessibility", "kTCCServiceScreenCapture", "kTCCServicePostEvent", "kTCCServiceListenEvent"} {
		fmt.Fprintf(&sql, "INSERT OR REPLACE INTO access (service, client, client_type, auth_value, auth_reason, auth_version, flags) VALUES ('%s', '%s', %d, 2, 4, 1, 0);", service, strings.ReplaceAll(client, "'", "''"), typ)
	}
	db := "/Library/Application Support/com.apple.TCC/TCC.db"
	if _, err := s.Output(ctx, "sudo sqlite3 "+Quote(db)+" "+Quote(sql.String())); err != nil {
		return err
	}
	_, err := s.Output(ctx, "sudo killall tccd || true")
	return err
}
