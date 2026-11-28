package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"time"

	"fennec/internal/commandstream"
	"fennec/internal/control"
	"fennec/internal/controlplane"
)

var cloudChannelID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// installChannels adds channels.setup, channels.info and channels.remove for
// Settings › Channels (FEN-228). A channel's secrets (bot token, Turnstile
// secret) go over the daemon's SSH connection straight to `fennec-terminal
// channel ...` on the cloud computer, which checks them with the service and
// keeps them in its 0600 config. They are never written to the board, which
// replicates to every Mac, nor kept here.
func installChannels(s *daemonServices, c *controlplane.Controller) {
	c.Additional["channels.setup"] = func(ctx context.Context, raw json.RawMessage) (any, error) {
		if len(raw) == 0 || len(raw) > 64<<10 || !json.Valid(raw) {
			return nil, &control.Fault{Code: "invalid_params", Message: "Invalid channel setup"}
		}
		return s.cloudChannel(ctx, "setup", raw)
	}
	for _, verb := range []string{"info", "remove"} {
		c.Additional["channels."+verb] = func(ctx context.Context, raw json.RawMessage) (any, error) {
			var params struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &params) != nil || !cloudChannelID.MatchString(params.ID) {
				return nil, &control.Fault{Code: "invalid_params", Message: "Invalid channel"}
			}
			return s.cloudChannel(ctx, verb+" "+params.ID, nil)
		}
	}
}

// cloudChannel runs `fennec-terminal channel <args>` on the cloud computer,
// with input on stdin, and returns its JSON output.
func (s *daemonServices) cloudChannel(ctx context.Context, args string, input []byte) (json.RawMessage, error) {
	if _, _, _, ok := s.account.Session(); !ok {
		return nil, &control.Fault{Code: "login_required", Message: "Sign in to Fennec"}
	}
	s.mu.Lock()
	channel := s.channel
	s.mu.Unlock()
	if channel == nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client, err := channel.borrowSSH(ctx)
	if err != nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true, Cause: err}
	}
	session, err := commandstream.NewSession(ctx, client)
	if err != nil {
		return nil, &control.Fault{Code: "not_ready", Message: "Connecting to the cloud computer", Retryable: true, Cause: err}
	}
	defer session.Close()
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	session.Stdin = bytes.NewReader(input)
	out, err := session.Output(`"$HOME/.local/fennec/bin/fennec-terminal" channel ` + args)
	if err != nil || len(out) > 1<<20 || !json.Valid(out) {
		// The cloud's error text may echo what was sent; don't relay it.
		return nil, &control.Fault{Code: "cloud_failed", Message: "The cloud computer could not save the channel", Cause: err}
	}
	return json.RawMessage(bytes.TrimSpace(out)), nil
}
