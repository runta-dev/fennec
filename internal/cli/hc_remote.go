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

// hcRemote quotes arguments for the resident command stream's remote shell.
func hcRemote(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return `core="$HOME/.local/bin/herdr-core"; [ -x "$core" ] || core="$(command -v herdr-core || command -v hc || printf %s "$HOME/.local/bin/hc")"; exec "$core" ` + strings.Join(quoted, " ")
}

type hcOffer struct {
	Socket   string `json:"socket"`
	Resident bool   `json:"resident"`
}

// hcCommand preserves core bytes, stderr and exit status over the existing
// authenticated resident channel. It never establishes an independent SSH client.
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
	var offer hcOffer
	if err := client.Call(opening, "hc.open", map[string]any{"args": args, "resident": true}, &offer); err != nil {
		return err
	}
	if !offer.Resident || filepath.Dir(offer.Socket) != filepath.Dir(client.Socket) {
		return fmt.Errorf("invalid resident core socket")
	}
	conn, err := (&net.Dialer{}).DialContext(opening, "unix", offer.Socket)
	if err != nil {
		return err
	}
	return runHCStream(ctx, conn, args, input, output, stderr)
}
func runHCStream(ctx context.Context, conn net.Conn, args []string, input io.Reader, output, stderr io.Writer) error {
	session := commandstream.NewStream(ctx, conn)
	defer session.Close()
	session.Stdin, session.Stdout, session.Stderr = input, output, stderr
	err := session.Run(hcRemote(args))
	var status *commandstream.ExitError
	if errors.As(err, &status) {
		return transferExit{code: status.Code}
	}
	return err
}

func (s *daemonServices) openHC(ctx context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		Args     []string `json:"args"`
		Resident bool     `json:"resident"`
	}
	if json.Unmarshal(raw, &params) != nil || !params.Resident || len(params.Args) == 0 || len(params.Args) > 64 {
		return nil, &control.Fault{Code: "invalid_params", Message: "Reconnect the desktop window"}
	}
	for _, a := range params.Args {
		if len(a) > 4096 || strings.ContainsRune(a, 0) {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid core command"}
		}
	}
	_, _, epoch, signedIn := s.account.Session()
	if !signedIn {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Fennec"}
	}
	s.mu.Lock()
	scope := transferScope{Owner: s.user, RuntimeID: s.config.RuntimeID}
	current := s.epoch == epoch
	s.mu.Unlock()
	if !current {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
	}
	request, _ := json.Marshal(scope)
	offered, err := s.openTransfer(ctx, request)
	if err != nil {
		return nil, err
	}
	offer := offered.(transferOffer)
	return hcOffer{Socket: offer.Socket, Resident: true}, nil
}
