package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/commandstream"
	"fennec/internal/control"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// hcRemote runs herdr-core's `hc` with these arguments on the cloud computer.
// The desktop window (desktop/, herdr-app's terminal core) speaks to
// herdr-core through `hc connect`'s standard input and output; on a Mac,
// `fen hc ...` is that `hc`, reaching the cloud computer's core over the
// resident connection.
func hcRemote(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return `exec "$(command -v hc || printf %s "$HOME/.local/bin/hc")" ` + strings.Join(quoted, " ")
}

// hcCommand is `fen hc ARGS`: standard streams pass through unchanged until
// either side closes, and hc's exit status is fen's.
func hcCommand(args []string, input io.Reader, output, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fen hc <hc arguments>")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	opening, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
	var offer transferOffer
	if err := client.Call(opening, "hc.open", struct{}{}, &offer); err != nil {
		return err
	}
	if filepath.Dir(offer.Socket) != filepath.Dir(client.Socket) {
		return fmt.Errorf("invalid herdr-core socket")
	}
	conn, err := (&net.Dialer{}).DialContext(opening, "unix", offer.Socket)
	if err != nil {
		return err
	}
	session := commandstream.NewStream(ctx, conn)
	defer session.Close()
	session.Stdin, session.Stdout, session.Stderr = input, output, stderr
	err = session.Run(hcRemote(args))
	var status *commandstream.ExitError
	if errors.As(err, &status) {
		return transferExit{code: status.Code}
	}
	return err
}

// openHC is the control method `hc.open`: a one-shot local stream to a
// command on the signed-in user's current cloud computer, scoped like
// transfer.open.
func (s *daemonServices) openHC(ctx context.Context, _ json.RawMessage) (any, error) {
	s.mu.Lock()
	scope := transferScope{Owner: s.user, RuntimeID: s.config.RuntimeID}
	s.mu.Unlock()
	if scope.Owner == "" || scope.RuntimeID == "" {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	return s.openTransfer(ctx, raw)
}
