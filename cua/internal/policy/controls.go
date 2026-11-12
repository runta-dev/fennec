package policy

import "fmt"

// Safety controls: the user's stop and resume, and recording. They are
// reached only from the CLI and Fennec's UI, never through an agent tool.
const (
	ControlStop   = "stop"
	ControlResume = "resume"
	ControlRecord = "record"
)

// Controls lists the safety controls.
var Controls = []string{ControlStop, ControlResume, ControlRecord}

// OwnerOnly decides whether a safety control may be answered for a peer
// running as uid peer (as getpeereid reports it): only the daemon's own
// user (uid own) may, never a member of a shared socket group.
func OwnerOnly(peer, own uint32) error {
	if peer == own {
		return nil
	}
	return fmt.Errorf("stop, resume and recording are answered only for the daemon's own user (uid %d); this connection runs as uid %d", own, peer)
}

// Halt is the user's stop. It ends every call in flight first, a running
// script included, and only then a recording in progress, so a recording
// never keeps a script's last input as the user's; then it shows the
// stopped state. Register OnState with the kill switch
// (sched.Stopper.OnState), which calls it once the calls are cancelled.
type Halt struct {
	// Recording ends a recording in progress and keeps what it recorded.
	Recording func()
	// Indicator, when set, shows stop and resume.
	Indicator *Indicator
}

// OnState answers a stop (stopped true) or a resume.
func (h *Halt) OnState(stopped bool) {
	if stopped && h.Recording != nil {
		h.Recording()
	}
	if h.Indicator != nil {
		h.Indicator.Stopped(stopped)
	}
}
