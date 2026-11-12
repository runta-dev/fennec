// Package vm drives the tart macOS VM that fennec-cua's desktop tests run in,
// so no test ever clicks or types on a developer's own desktop.
package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Defaults for the development VM.
const (
	DefaultName  = "fennec-cua-dev"
	DefaultImage = "ghcr.io/cirruslabs/macos-tahoe-base:latest"
	// The cirruslabs base images ship this account with passwordless sudo,
	// automatic login to the desktop and tart's guest agent.
	DefaultUser = "admin"
)

// VM is one tart VM.
type VM struct {
	Name string
	User string
	// Tart is the tart binary; empty means "tart" from PATH.
	Tart string
}

// New returns the default development VM.
func New() *VM {
	return &VM{Name: DefaultName, User: DefaultUser}
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

// Start boots the VM without a window and waits until its guest agent
// answers. The VM keeps running after Start returns; Stop shuts it down.
func (v *VM) Start(ctx context.Context) (string, error) {
	if v.agentReady(ctx) {
		return v.Name, nil
	}
	bin := v.bin()
	if running, _ := v.running(ctx); !running {
		cmd := exec.Command(bin, "run", "--no-graphics", v.Name)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Start(); err != nil {
			return "", err
		}
		_ = cmd.Process.Release()
	}
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		if v.agentReady(ctx) {
			return v.Name, nil
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "", errors.New("the VM's guest agent did not answer within 8 minutes")
}

func (v *VM) bin() string {
	if v.Tart != "" {
		return v.Tart
	}
	return "tart"
}

func (v *VM) running(ctx context.Context) (bool, error) {
	out, err := v.tart(ctx, "list")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == v.Name {
			return strings.HasSuffix(strings.TrimSpace(line), "running"), nil
		}
	}
	return false, nil
}

// agentReady reports whether tart exec reaches the guest.
func (v *VM) agentReady(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, v.bin(), "exec", v.Name, "/usr/bin/true").Run() == nil
}

// Stop shuts the VM down.
func (v *VM) Stop(ctx context.Context) error {
	_, err := v.tart(ctx, "stop", v.Name)
	return err
}

// Session runs commands in the VM through tart exec and its guest agent,
// a virtio channel that needs no network or SSH.
type Session struct {
	vm *VM
}

// Connect returns a session on a running VM.
func (v *VM) Connect(ctx context.Context) (*Session, error) {
	if !v.agentReady(ctx) {
		return nil, errors.New("the VM is not running or its guest agent does not answer")
	}
	return &Session{vm: v}, nil
}

// Close is a no-op; each command is its own exec.
func (s *Session) Close() error { return nil }

// Run runs a shell command as the VM's admin user.
func (s *Session) Run(ctx context.Context, command string, stdin io.Reader, stdout, stderr io.Writer) error {
	args := []string{"exec"}
	if stdin != nil {
		args = append(args, "-i")
	}
	args = append(args, s.vm.Name, "/bin/sh", "-c", command)
	cmd := exec.CommandContext(ctx, s.vm.bin(), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
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

// Clone makes a copy-on-write clone of the VM (or an OCI image) named src
// as v. It fails when v already exists.
func (v *VM) Clone(ctx context.Context, src string) error {
	_, err := v.tart(ctx, "clone", src, v.Name)
	return err
}

// Delete removes the VM. A running VM is stopped first.
func (v *VM) Delete(ctx context.Context) error {
	if running, _ := v.running(ctx); running {
		_ = v.Stop(ctx)
	}
	_, err := v.tart(ctx, "delete", v.Name)
	return err
}

// SetDisplay sets the VM's display, such as "1920x1080px". It takes effect
// at the next boot.
func (v *VM) SetDisplay(ctx context.Context, display string) error {
	_, err := v.tart(ctx, "set", v.Name, "--display", display)
	return err
}

// ExecCommand is the host command line that runs command in the VM through
// tart exec, with stdin attached when interactive. A child process started
// from it, such as an MCP server on stdio, runs inside the VM while its
// pipes stay on the host.
func (v *VM) ExecCommand(interactive bool, command string) (string, []string) {
	args := []string{"exec"}
	if interactive {
		args = append(args, "-i")
	}
	return v.bin(), append(args, v.Name, "/bin/sh", "-c", command)
}

// GrantUserTCC grants a per-user TCC service to a client in the VM user's
// own TCC database: the services the system database does not hold, such as
// Automation (kTCCServiceAppleEvents, with target as the receiving app's
// bundle id), Contacts (kTCCServiceAddressBook) and Reminders. client is a
// bundle id or an absolute executable path; target is empty for services
// without one. Like GrantTCC it needs SIP disabled.
func (s *Session) GrantUserTCC(ctx context.Context, service, client, target string) error {
	typ := 0
	if strings.HasPrefix(client, "/") {
		typ = 1
	}
	if target == "" {
		target = "UNUSED"
	}
	q := func(x string) string { return "'" + strings.ReplaceAll(x, "'", "''") + "'" }
	sql := fmt.Sprintf("INSERT OR REPLACE INTO access (service, client, client_type, auth_value, auth_reason, auth_version, indirect_object_identifier_type, indirect_object_identifier, flags) VALUES (%s, %s, %d, 2, 3, 1, 0, %s, 0);",
		q(service), q(client), typ, q(target))
	db := `"$HOME/Library/Application Support/com.apple.TCC/TCC.db"`
	if _, err := s.Output(ctx, "sudo sqlite3 "+db+" "+Quote(sql)); err != nil {
		return err
	}
	_, err := s.Output(ctx, "killall tccd || true")
	return err
}

// ApproveScreenCapture stops macOS 15 and later from asking, about once a
// month, whether a client may keep capturing the screen without the system
// picker: a modal alert that would cover the desktop in the middle of a
// test. clients are executable paths or bundle ids; their next alert is
// moved to the year 2100.
func (s *Session) ApproveScreenCapture(ctx context.Context, clients ...string) error {
	plist := `"$HOME/Library/Group Containers/group.com.apple.replayd/ScreenCaptureApprovals.plist"`
	var b strings.Builder
	b.WriteString(`mkdir -p "$HOME/Library/Group Containers/group.com.apple.replayd"`)
	for _, c := range clients {
		k := ":" + strings.ReplaceAll(c, ":", `\:`)
		fmt.Fprintf(&b, "; /usr/libexec/PlistBuddy -c %s %s 2>/dev/null", Quote("Delete "+k), plist)
		fmt.Fprintf(&b, "; /usr/libexec/PlistBuddy -c %s -c %s -c %s -c %s -c %s %s",
			Quote("Add "+k+" dict"),
			Quote("Add "+k+`:kScreenCaptureApprovalLastAlerted date "Fri Jan 1 00:00:00 UTC 2100"`),
			Quote("Add "+k+`:kScreenCapturePrivacyHintDate date "Fri Jan 1 00:00:00 UTC 2100"`),
			Quote("Add "+k+":kScreenCapturePrivacyHintPolicy integer 2592000"),
			Quote("Add "+k+":kScreenCaptureApprovalLastUsed date \""+time.Now().UTC().Format("Mon Jan 2 15:04:05 UTC 2006")+"\""),
			plist)
	}
	_, err := s.Output(ctx, b.String())
	return err
}

// Shutdown shuts the guest down from inside, so apps, preferences and the
// page cache are saved, and waits until the VM has stopped. tart stop may
// end the VM before that.
func (v *VM) Shutdown(ctx context.Context) error {
	s := &Session{vm: v}
	_ = s.Run(ctx, "sudo shutdown -h now", nil, io.Discard, io.Discard)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if running, err := v.running(ctx); err == nil && !running {
			return nil
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return v.Stop(ctx)
}
