package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/control"
	"fennec/internal/runta"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// hcRemote runs herdr-core's `hc` with these arguments on the cloud computer.
// The desktop window (desktop/, herdr-app's terminal core) speaks to
// herdr-core through `hc connect`'s standard input and output; on a Mac,
// `fen hc ...` is that `hc`.
func hcRemote(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return `exec "$(command -v hc || printf %s "$HOME/.local/bin/hc")" ` + strings.Join(quoted, " ")
}

type hcOffer struct {
	Socket string `json:"socket"`
}

// hcCommand is `fen hc ARGS`: standard input and output pass through
// unchanged until either side closes.
func hcCommand(args []string, input io.Reader, output, _ io.Writer) error {
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
	if err := client.Call(opening, "hc.open", map[string]any{"args": args}, &offer); err != nil {
		return err
	}
	if filepath.Dir(offer.Socket) != filepath.Dir(client.Socket) {
		return fmt.Errorf("invalid herdr-core socket")
	}
	conn, err := (&net.Dialer{}).DialContext(opening, "unix", offer.Socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stopConn := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopConn()
	go func() {
		io.Copy(conn, input)
		if unix, ok := conn.(*net.UnixConn); ok {
			unix.CloseWrite()
		}
	}()
	_, err = io.Copy(output, conn)
	return err
}

// openHC is the control method `hc.open` ({args}): runs `hc ARGS` on the
// signed-in user's current cloud computer over an SSH connection of its own and
// hands its standard streams to one local connection. Terminals are
// interactive: unlike transfers they must not share the resident connection's
// paced bulk stream, where any busy terminal delays every keystroke's echo.
// This is how the Herdr app reaches its other machines (`hc connect` over SSH).
func (s *daemonServices) openHC(_ context.Context, raw json.RawMessage) (any, error) {
	var params struct {
		Args []string `json:"args"`
	}
	if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &params) != nil {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid herdr-core command"}
	}
	if len(params.Args) == 0 {
		params.Args = []string{"connect"}
	}
	if len(params.Args) > 64 {
		return nil, &control.Fault{Code: "invalid_params", Message: "Invalid herdr-core command"}
	}
	for _, a := range params.Args {
		if len(a) > 4096 || strings.ContainsRune(a, 0) {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid herdr-core command"}
		}
	}
	_, token, _, signedIn := s.account.Session()
	s.mu.Lock()
	runtimeID, life := s.config.RuntimeID, s.ctx
	s.mu.Unlock()
	if !signedIn {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Fennec"}
	}
	if runtimeID == "" || life == nil || life.Err() != nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
	}
	directory := filepath.Join(s.home, ".config/fennec/ipc")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	var name [8]byte
	if _, err := rand.Read(name[:]); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "hc-"+hex.EncodeToString(name[:])+".sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, err
	}
	go serveHC(life, listener.(*net.UnixListener), path, func(ctx context.Context, fresh bool) (*ssh.Client, error) {
		return hcClient(ctx, runta.NewClient(s.home, token), runtimeID+"\x00"+token, runtimeID, filepath.Join(s.home, ".config", "fennec", "cloud"), fresh)
	}, hcRemote(params.Args))
	return hcOffer{Socket: path}, nil
}

// serveHC accepts the one connection the offer is for, unclaimed offers
// expiring after ten seconds, and relays it to `hc` on the cloud computer.
// One SSH connection to the cloud computer serves every hc command: each is a
// channel on it, so a window's terminals and file drops skip the handshake
// (two to three seconds). A new account, cloud computer or a dead connection
// dials again.
var hcSSH struct {
	mu     sync.Mutex
	key    string
	client *ssh.Client
}

func hcClient(ctx context.Context, api *runta.Client, key, runtimeID, cloud string, fresh bool) (*ssh.Client, error) {
	hcSSH.mu.Lock()
	defer hcSSH.mu.Unlock()
	if hcSSH.client != nil && (fresh || hcSSH.key != key) {
		hcSSH.client.Close()
		hcSSH.client = nil
	}
	if hcSSH.client != nil {
		return hcSSH.client, nil
	}
	client, err := api.Connect(ctx, runtimeID, cloud)
	if err != nil {
		return nil, err
	}
	// The cached connection outlives the request that dialed it; keepalives
	// find a dead one so the next command dials again.
	go func() {
		for {
			time.Sleep(30 * time.Second)
			if _, _, err := client.SendRequest("keepalive@fennec", true, nil); err != nil {
				client.Close()
				hcSSH.mu.Lock()
				if hcSSH.client == client {
					hcSSH.client = nil
				}
				hcSSH.mu.Unlock()
				return
			}
		}
	}()
	hcSSH.key, hcSSH.client = key, client
	return client, nil
}

func serveHC(life context.Context, listener *net.UnixListener, path string, dial func(context.Context, bool) (*ssh.Client, error), command string) {
	defer os.Remove(path)
	listener.SetDeadline(time.Now().Add(10 * time.Second))
	conn, err := listener.Accept()
	listener.Close()
	if err != nil {
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(life)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	client, err := dial(ctx, false)
	if err != nil {
		return
	}
	session, err := client.NewSession()
	if err != nil {
		// A cached connection the cloud computer dropped: dial once more.
		if client, err = dial(ctx, true); err != nil {
			return
		}
		if session, err = client.NewSession(); err != nil {
			return
		}
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		return
	}
	session.Stdout = conn
	session.Stderr = io.Discard
	if err := session.Start(command); err != nil {
		return
	}
	// Session.Wait does not wait for input; the local side's half-close ends it.
	go func() {
		io.Copy(stdin, conn)
		stdin.Close()
	}()
	session.Wait()
}
