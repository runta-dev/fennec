package localssh

import (
	"context"
	"errors"
	"fennec/internal/audit"
	"fennec/internal/sandbox"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	ssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	crypto "golang.org/x/crypto/ssh"
)

const macOSSFTPServer = "/usr/libexec/sftp-server"

// SFTP grants a whole file-transfer session as the current OS user. It is not
// represented as approval for a single path or as a restricted filesystem jail.
func fileTransfer(parent context.Context, paired func(ssh.PublicKey) bool, broker *Broker, requireApproval bool) ssh.SubsystemHandler {
	return func(session ssh.Session) {
		ctx, cancel := context.WithCancel(session.Context())
		defer cancel()
		watchPairing(ctx, cancel, func() bool { return paired(session.PublicKey()) }, time.Second)
		stop := context.AfterFunc(parent, cancel)
		defer stop()
		if !paired(session.PublicKey()) {
			session.Exit(126)
			return
		}
		policy := broker.effectivePolicy()
		mode := sandbox.NormalizeMode(policy.Mode)
		summary := "SFTP: file transfer session (" + mode + ")"
		decisionType := "auto_sandbox"
		// Codex parity: file access runs inside the same Seatbelt boundary
		// without review; only an unsandboxed transfer outside Full access asks.
		// Presets run SFTP inside the sandbox without prompts; a connector
		// without a preset, or without a sandbox, asks. Full access never asks.
		full := policy.Approval == "never"
		onRequest := policy.Approval == sandbox.PresetAsk || policy.Approval == sandbox.PresetAuto
		if requireApproval && !full && (!onRequest || (mode != sandbox.ModeFull && !sandbox.Available())) {
			approval, done := context.WithTimeout(ctx, 2*time.Minute)
			allowed, dec := broker.approveWithDecision(approval, "SFTP: read and write files accessible to the current user", crypto.FingerprintSHA256(session.PublicKey()), "sftp", "")
			decisionType = dec
			done()
			if !allowed {
				broker.recordAudit(audit.Entry{
					Kind:           "sftp",
					Command:        summary,
					SandboxMode:    mode,
					NetworkAllowed: policy.Network,
					Decision:       "user_denied",
					Blocked:        true,
					ExitCode:       126,
				})
				session.Exit(126)
				return
			}
		}
		if !paired(session.PublicKey()) {
			session.Exit(126)
			return
		}
		home, err := os.UserHomeDir()
		if err != nil {
			session.Exit(1)
			return
		}
		started := time.Now()
		// On macOS, run /usr/libexec/sftp-server inside Seatbelt when sandboxing is active
		// so SFTP file reads/writes obey the exact same kernel boundary as shell exec.
		if mode != sandbox.ModeFull && sandbox.Available() {
			if info, statErr := os.Stat(macOSSFTPServer); statErr == nil && info.Mode()&0111 != 0 {
				cmd, effMode := sandbox.Command(ctx, policy, home, home, macOSSFTPServer)
				var stderrPreview boundedCapture
				cmd.Stdin = session
				cmd.Stdout = session
				cmd.Stderr = io.MultiWriter(session.Stderr(), &stderrPreview)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				cmd.Cancel = func() error {
					if cmd.Process == nil {
						return nil
					}
					return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
				code := 0
				if runErr := cmd.Run(); runErr != nil {
					code = 1
					var exit *exec.ExitError
					if errors.As(runErr, &exit) && exit.ExitCode() >= 0 {
						code = exit.ExitCode()
					}
				}
				blocked := sandbox.DetectSandboxViolation(effMode, code, stderrPreview.String())
				if blocked {
					decisionType = "sandbox_blocked"
				}
				broker.recordAudit(audit.Entry{
					Kind:           "sftp",
					Command:        summary,
					Workdir:        cmd.Dir,
					SandboxMode:    effMode,
					NetworkAllowed: policy.Network,
					Decision:       decisionType,
					Blocked:        blocked,
					ExitCode:       code,
					DurationMS:     time.Since(started).Milliseconds(),
					Detail:         stderrPreview.String(),
				})
				session.Exit(code)
				return
			}
		}
		server, err := sftp.NewServer(session, sftp.WithServerWorkingDirectory(home))
		if err != nil {
			session.Exit(1)
			return
		}
		defer server.Close()
		stopIO := context.AfterFunc(ctx, func() { server.Close(); session.Close() })
		defer stopIO()
		err = server.Serve()
		code := 0
		if err != nil && err != io.EOF {
			code = 1
		}
		broker.recordAudit(audit.Entry{
			Kind:           "sftp",
			Command:        summary,
			Workdir:        home,
			SandboxMode:    sandbox.ModeFull,
			NetworkAllowed: true,
			Decision:       decisionType,
			ExitCode:       code,
			DurationMS:     time.Since(started).Milliseconds(),
		})
		session.Exit(code)
	}
}
