package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/account"
	"fennec/internal/byoc"
	"fennec/internal/cloudstate"
	"fennec/internal/cloudtarget"
	"fennec/internal/runta"
	"os"
	hostruntime "runtime"

	"golang.org/x/crypto/ssh"
)

// resolvedComputer identifies the cloud computer a preparation reached. ID is
// carried as the runtime ID by every session path; Owner scopes local
// preferences, the Mac connector's trusted devices, and fingerprints.
type resolvedComputer struct {
	ID    string
	Owner string
}

// cloudComputer is how preparation and its launch broker reach one cloud
// computer. Everything after a verified SSH connection (terminal service,
// agents, credentials, models) is shared; only finding, trusting and
// reconnecting to the machine differ between a Runta and a self-hosted one.
type cloudComputer interface {
	// Scope keys local fingerprints and preparation records for this
	// principal. It never leaves the Mac.
	Scope() string
	// Directory holds the device key and pinned host key used by the
	// OpenSSH launch paths.
	Directory() string
	// Resolve finds the computer (allocating or starting it when the
	// provider can), calls resolved as soon as its identity is verified, and
	// returns a connection authenticated against the pinned host key.
	Resolve(ctx context.Context, progress func(stage, id, status string), resolved func(resolvedComputer) error) (*ssh.Client, resolvedComputer, error)
	// Authorize re-verifies ownership and refreshes the pinned SSH material
	// before an OpenSSH launch path uses Directory.
	Authorize(ctx context.Context, id string) error
	// Reconnect replaces the resident connection after re-verifying the
	// computer.
	Reconnect(ctx context.Context, id string) (*ssh.Client, error)
}

// errPairedComputerChanged means the pairing on disk no longer names the
// machine this session epoch was paired with.
var errPairedComputerChanged = errors.New("the paired cloud computer changed; pair it again in Fennec")

// newCloudComputer returns the computer for principal. retainSSH lets the
// daemon's Runta connection survive daemon replacement (macOS only).
func newCloudComputer(home string, principal account.Principal, retainSSH bool) (cloudComputer, error) {
	if token, isRunta := principal.RuntaToken(); isRunta {
		return newRuntaComputer(home, token, retainSSH), nil
	}
	if principal.IsBYOC() {
		return selfHostedComputer{home: home, machine: principal.Machine}, nil
	}
	return nil, errors.New("no signed-in account or paired cloud computer")
}

// runtaComputer is the user's shared Runta-managed cloud computer.
type runtaComputer struct {
	home   string
	token  string
	client *runta.Client
}

func newRuntaComputer(home, token string, retainSSH bool) runtaComputer {
	client := runta.NewClient(home, token)
	if retainSSH && hostruntime.GOOS == "darwin" {
		client.RetainSSHHome = home
	}
	return runtaComputer{home: home, token: token, client: client}
}

func (c runtaComputer) Scope() string     { return c.token }
func (c runtaComputer) Directory() string { return cloudtarget.RuntaDir(c.home) }

func (c runtaComputer) Resolve(ctx context.Context, progress func(stage, id, status string), resolved func(resolvedComputer) error) (*ssh.Client, resolvedComputer, error) {
	lifecycleStore := cloudstate.NewScoped(c.home, runta.ActiveEnvironment(c.home).Key())
	client := *c.client
	client.SharedAllowed = lifecycleStore.Check
	client.CreationGeneration = lifecycleStore.Generation
	var hint struct {
		RuntimeID string `json:"runtime_id"`
	}
	if data, err := os.ReadFile(runta.TakeoverPreferencesPath(c.home)); err == nil && len(data) <= 8192 {
		_ = json.Unmarshal(data, &hint)
	}
	var computer resolvedComputer
	connection, err := client.ConnectResolved(ctx, hint.RuntimeID, c.Directory(), func(resolveCtx context.Context) (string, error) {
		runtime, err := client.EnsureShared(resolveCtx)
		if err != nil {
			return "", err
		}
		computer = resolvedComputer{ID: runtime.ID, Owner: runtime.Owner}
		if err := resolved(computer); err != nil {
			return "", err
		}
		switch runtime.Status {
		case "shutdown", "stopped":
			err = client.Start(resolveCtx, runtime.ID)
		case "paused", "suspended":
			err = client.Resume(resolveCtx, runtime.ID)
		}
		if err != nil {
			return "", err
		}
		if runtime.Status != "running" || runtime.Degraded || !runtime.SSHEnabled {
			err = client.WaitReady(resolveCtx, runtime.ID, func(r runta.Runtime) { progress("starting", r.ID, r.Status) })
		}
		if err != nil {
			return "", err
		}
		progress("connecting", runtime.ID, "")
		return runtime.ID, nil
	})
	return connection, computer, err
}

func (c runtaComputer) Authorize(ctx context.Context, id string) error {
	_, err := c.client.PrepareOwnedSSH(ctx, id, c.Directory())
	return err
}

func (c runtaComputer) Reconnect(ctx context.Context, id string) (*ssh.Client, error) {
	return c.client.ConnectOwned(ctx, id, c.Directory())
}

// selfHostedComputer is a paired BYOC computer. It is never allocated,
// started or deleted by Fennec; its owner manages its lifecycle.
type selfHostedComputer struct {
	home    string
	machine string
}

func (c selfHostedComputer) owner() string     { return "byoc:" + c.machine }
func (c selfHostedComputer) Scope() string     { return c.owner() }
func (c selfHostedComputer) Directory() string { return byoc.Dir(c.home) }

// pairing loads the pairing record and checks it still names this epoch's
// machine, so an unpaired or re-paired computer is never used by a stale
// session.
func (c selfHostedComputer) pairing() (byoc.Pairing, error) {
	p, err := byoc.Load(c.home)
	if err != nil {
		return byoc.Pairing{}, err
	}
	if p.MachineID != c.machine {
		return byoc.Pairing{}, errPairedComputerChanged
	}
	return p, nil
}

func (c selfHostedComputer) Resolve(ctx context.Context, progress func(stage, id, status string), resolved func(resolvedComputer) error) (*ssh.Client, resolvedComputer, error) {
	p, err := c.pairing()
	if err != nil {
		return nil, resolvedComputer{}, err
	}
	computer := resolvedComputer{ID: p.MachineID, Owner: c.owner()}
	if err := resolved(computer); err != nil {
		return nil, computer, err
	}
	progress("connecting", computer.ID, "")
	connection, err := byoc.Connect(ctx, c.home, p)
	return connection, computer, err
}

func (c selfHostedComputer) Authorize(_ context.Context, id string) error {
	if id != c.machine {
		return errPairedComputerChanged
	}
	_, err := c.pairing()
	return err
}

func (c selfHostedComputer) Reconnect(ctx context.Context, id string) (*ssh.Client, error) {
	if id != c.machine {
		return nil, errPairedComputerChanged
	}
	p, err := c.pairing()
	if err != nil {
		return nil, err
	}
	return byoc.Connect(ctx, c.home, p)
}
