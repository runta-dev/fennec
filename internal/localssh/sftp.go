package localssh

import (
	"context"
	"io"
	"os"
	"time"

	ssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	crypto "golang.org/x/crypto/ssh"
)

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
		if requireApproval {
			approval, done := context.WithTimeout(ctx, 2*time.Minute)
			allowed := broker.approve(approval, "SFTP: read and write files accessible to the current user", crypto.FingerprintSHA256(session.PublicKey()))
			done()
			if !allowed {
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
		server, err := sftp.NewServer(session, sftp.WithServerWorkingDirectory(home))
		if err != nil {
			session.Exit(1)
			return
		}
		defer server.Close()
		stopIO := context.AfterFunc(ctx, func() { server.Close(); session.Close() })
		defer stopIO()
		err = server.Serve()
		if err != nil && err != io.EOF {
			session.Exit(1)
		} else {
			session.Exit(0)
		}
	}
}
