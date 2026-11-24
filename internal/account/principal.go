package account

import (
	"errors"
	"regexp"
)

// PrincipalKind names whom the daemon acts for. A Runta account and a
// self-hosted (BYOC) cloud computer are mutually exclusive: switching between
// them always starts a new session epoch, so results from the previous
// principal are fenced exactly as they are after sign-out.
type PrincipalKind string

const (
	// PrincipalRunta is a signed-in Runta account whose token authorizes the
	// Runta API and its managed cloud computer.
	PrincipalRunta PrincipalKind = "runta"
	// PrincipalBYOC is a paired, self-hosted cloud computer. It has no Runta
	// token; trust comes from the machine's pinned identity.
	PrincipalBYOC PrincipalKind = "byoc"
)

// StatusPaired is the account status while a self-hosted cloud computer is
// the active principal. StatusAuthorized remains the Runta-signed-in status.
const (
	StatusAuthorized = "authorized"
	StatusPaired     = "paired"
)

// Principal is the active identity for one session epoch. Token is set only
// for PrincipalRunta and, like the token it replaces, is daemon-internal: it
// must never enter logs, argv, or public responses. Machine is set only for
// PrincipalBYOC and identifies the paired computer.
type Principal struct {
	Kind    PrincipalKind
	Token   string
	Machine string
}

// RuntaToken returns the Runta API token when the principal is a Runta
// account. Code that calls the Runta API must use it rather than Token, so a
// self-hosted principal can never be sent to Runta with an empty credential.
func (p Principal) RuntaToken() (string, bool) {
	if p.Kind != PrincipalRunta || p.Token == "" {
		return "", false
	}
	return p.Token, true
}

// IsBYOC reports whether the principal is a paired self-hosted computer.
func (p Principal) IsBYOC() bool { return p.Kind == PrincipalBYOC && p.Machine != "" }

// ErrRuntaAccountRequired is returned by Runta-only operations (profile,
// billing, organizations, managed cloud lifecycle) under a BYOC principal.
var ErrRuntaAccountRequired = errors.New("a Runta account is required for this operation")

// Active reports whether a principal is in effect for this snapshot: a Runta
// account is signed in, or a self-hosted cloud computer is paired.
func (s Snapshot) Active() bool { return s.Status == StatusAuthorized || s.Status == StatusPaired }

var machineIdentity = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$`)

// ValidMachineID reports whether id is an acceptable BYOC machine identity.
func ValidMachineID(id string) bool { return machineIdentity.MatchString(id) }

func (m *Manager) principalLocked() (Principal, bool) {
	switch {
	case m.state.Status == StatusAuthorized && m.token != "":
		return Principal{Kind: PrincipalRunta, Token: m.token}, true
	case m.state.Status == StatusPaired && m.machine != "":
		return Principal{Kind: PrincipalBYOC, Machine: m.machine}, true
	}
	return Principal{}, false
}

// Pair makes the self-hosted computer machine the active principal under a
// fresh epoch. Any Runta session in effect is ended in memory, like a
// principal switch, but its stored credential is left in place: choosing the
// Runta account again restores it. Pairing trust (host key, device key) is
// established by the caller before Pair is invoked.
func (m *Manager) Pair(machine string) (uint64, error) {
	if !ValidMachineID(machine) {
		return 0, errors.New("invalid cloud computer identity")
	}
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == StatusPaired && m.machine == machine {
		return m.state.Epoch, nil
	}
	_, epoch := m.resetLocked(StatusPaired)
	m.machine = machine
	m.notifyLocked()
	return epoch, nil
}

// Unpair ends the BYOC principal for machine. It is a no-op when machine is
// not the paired computer, so a stale request cannot end a newer pairing.
func (m *Manager) Unpair(machine string) {
	m.credentialsMu.Lock()
	defer m.credentialsMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status != StatusPaired || m.machine != machine {
		return
	}
	m.resetLocked("signed_out")
}
