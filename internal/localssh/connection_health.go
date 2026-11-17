package localssh

import (
	"bytes"
	"context"
	"encoding/hex"
	"sync"

	server "github.com/gliderlabs/ssh"
	"golang.org/x/crypto/ssh"
)

const (
	registerConnectionHealth = "connection-health-register@fennec"
	probeConnectionHealth    = "connection-health-probe@fennec"
)

type connectionHealthTarget struct {
	ctx server.Context
	key []byte
}

// Health queries use a separate authenticated byte stream. They never queue
// behind the target connection's bulk data or global-request approval handler.
// Session IDs bind probes to an exact SSH connection, not merely an online Mac.
func connectionHealth(parent context.Context, paired func(server.PublicKey) bool) server.RequestHandler {
	var mu sync.Mutex
	targets := make(map[string]connectionHealthTarget)
	return func(ctx server.Context, _ *server.Server, request *ssh.Request) (bool, []byte) {
		key, _ := ctx.Value(server.ContextKeyPublicKey).(server.PublicKey)
		if !request.WantReply || parent.Err() != nil || ctx.Err() != nil || !paired(key) {
			return false, nil
		}
		switch request.Type {
		case registerConnectionHealth:
			if len(request.Payload) != 0 {
				return false, nil
			}
			id := ctx.SessionID()
			mu.Lock()
			if _, ok := targets[id]; ok {
				mu.Unlock()
				return true, nil
			}
			if len(targets) >= 256 {
				mu.Unlock()
				return false, nil
			}
			targets[id] = connectionHealthTarget{ctx, append([]byte(nil), key.Marshal()...)}
			mu.Unlock()
			context.AfterFunc(ctx, func() { mu.Lock(); delete(targets, id); mu.Unlock() })
			return true, nil
		case probeConnectionHealth:
			var target struct{ Session []byte }
			if ssh.Unmarshal(request.Payload, &target) != nil || len(target.Session) == 0 || len(target.Session) > 64 {
				return false, nil
			}
			mu.Lock()
			entry, ok := targets[hex.EncodeToString(target.Session)]
			mu.Unlock()
			return ok && entry.ctx.Err() == nil && bytes.Equal(entry.key, key.Marshal()), nil
		}
		return false, nil
	}
}
