package localssh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"os/exec"
	"time"
)

// relayExecSlow is how long a relay command runs before its end is logged.
const relayExecSlow = 10 * time.Second

// logRelayExec records how a relay command ended, without its text (the audit
// log keeps that; the digest correlates the two). It separates a process that
// did not exit (blocked, for example on a macOS permission prompt) from one
// whose output streams stayed open after it exited (ErrWaitDelay: a background
// child holding stdout) and from a client that went away first.
func logRelayExec(command string, started time.Time, _ int, runErr error, clientGone bool, runCtxErr error) {
	elapsed := time.Since(started)
	end := "exited"
	switch {
	case errors.Is(runErr, exec.ErrWaitDelay):
		end = "exited; output held open by a child"
	case clientGone:
		end = "client went away first"
	case errors.Is(runCtxErr, context.DeadlineExceeded):
		end = "timed out"
	}
	if elapsed < relayExecSlow && end == "exited" {
		return
	}
	sum := sha256.Sum256([]byte(command))
	log.Printf("device relay exec %s: %s after %s (err %v)", hex.EncodeToString(sum[:4]), end, elapsed.Round(time.Millisecond), runErr)
}
