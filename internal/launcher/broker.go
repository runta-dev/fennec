package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/hookbridge"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/sessionid"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// LaunchIntent contains no credentials. The broker retains login context and
// returns only a confirmed attach receipt, never the Runta token.
type LaunchIntent struct {
	Hooks     []hookbridge.Binding `json:"hooks,omitempty"`
	RequestID string               `json:"request_id"`
	Agent     string               `json:"agent"`
	Directory string               `json:"directory"`
	Arguments []string             `json:"arguments"`
	Version   string               `json:"version,omitempty"`
	Prepare   bool                 `json:"prepare,omitempty"`
	Cold      bool                 `json:"cold,omitempty"`
}
type BrokerReply struct {
	Receipt *LaunchReceipt `json:"receipt,omitempty"`
	Error   string         `json:"error,omitempty"`
	Plan    *LaunchPlan    `json:"plan,omitempty"`
}

type LaunchPlan struct {
	StreamID    string                    `json:"stream_id,omitempty"`
	Resume      bool                      `json:"resume,omitempty"`
	StreamPath  string                    `json:"stream_path,omitempty"`
	Launch      *cloudruntime.LocalLaunch `json:"launch,omitempty"`
	ControlPath string                    `json:"control_path,omitempty"`
	RuntimeID   string                    `json:"runtime_id"`
	SessionID   string                    `json:"session_id"`
	Agent       string                    `json:"agent"`
	Device      string                    `json:"device"`
	Script      string                    `json:"script"`
}

func (r LaunchIntent) Validate() error {
	if err := hookbridge.ValidateBindings(r.Hooks); err != nil {
		return err
	}
	if !sessionid.Valid(r.RequestID) || !Supported[r.Agent] || !filepath.IsAbs(r.Directory) || strings.ContainsAny(r.Directory, "\x00\r\n") {
		return fmt.Errorf("invalid launch intent")
	}
	size := 0
	for _, a := range r.Arguments {
		size += len(a)
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("invalid argument")
		}
	}
	if len(r.Arguments) > 256 || size > 32768 {
		return fmt.Errorf("launch arguments exceed limit")
	}
	return nil
}

// ServeBroker requires an application-owned 0700 directory. A lifetime lock
// permits crash recovery without stealing another broker's listener.
func ServeBroker(ctx context.Context, path string, launch func(context.Context, LaunchIntent) (LaunchReceipt, error)) error {
	return ServeBrokerReady(ctx, path, launch, nil)
}
func ServeBrokerReady(ctx context.Context, path string, launch func(context.Context, LaunchIntent) (LaunchReceipt, error), ready func() error) error {
	return ServeBrokerPrepared(ctx, path, launch, nil, ready)
}

func ServeBrokerPrepared(ctx context.Context, path string, launch func(context.Context, LaunchIntent) (LaunchReceipt, error), prepare func(context.Context, LaunchIntent) (LaunchPlan, error), ready func() error) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute socket path required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("broker directory must be private")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another launch broker owns this socket")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if existing, err := os.Lstat(path); err == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("broker path is not a socket")
		}
		probe, dialErr := net.DialTimeout("unix", path, time.Second)
		if dialErr == nil {
			probe.Close()
			return fmt.Errorf("existing launch broker is listening")
		}
		if !os.IsNotExist(dialErr) && !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return fmt.Errorf("cannot establish socket is stale: %w", dialErr)
		}
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	slots := make(chan struct{}, 4)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			defer conn.Close()
			operation, cancel := context.WithTimeout(ctx, 8*time.Minute)
			defer cancel()
			closeOnCancel := context.AfterFunc(operation, func() { conn.Close() })
			defer closeOnCancel()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var intent LaunchIntent
			if json.NewDecoder(io.LimitReader(conn, 65536)).Decode(&intent) != nil || intent.Validate() != nil {
				json.NewEncoder(conn).Encode(BrokerReply{Error: "invalid launch request"})
				return
			}
			conn.SetReadDeadline(time.Time{})
			if intent.Prepare {
				if prepare == nil {
					json.NewEncoder(conn).Encode(BrokerReply{Error: "launch preparation unavailable"})
					return
				}
				plan, err := prepare(operation, intent)
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err != nil {
					json.NewEncoder(conn).Encode(BrokerReply{Error: err.Error()})
					return
				}
				json.NewEncoder(conn).Encode(BrokerReply{Plan: &plan})
				return
			}
			receipt, err := launch(operation, intent)
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err != nil {
				message := err.Error()
				if len(message) > 1000 {
					message = message[:1000]
				}
				json.NewEncoder(conn).Encode(BrokerReply{Error: message})
				return
			}
			if receipt.SessionID != intent.RequestID || receipt.Agent != intent.Agent {
				json.NewEncoder(conn).Encode(BrokerReply{Error: "launch receipt mismatch"})
				return
			}
			json.NewEncoder(conn).Encode(BrokerReply{Receipt: &receipt})
		}()
	}
}

func RequestLaunch(ctx context.Context, path string, intent LaunchIntent) (LaunchReceipt, error) {
	if err := intent.Validate(); err != nil {
		return LaunchReceipt{}, err
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return LaunchReceipt{}, fmt.Errorf("Fennec launch broker unavailable")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err = json.NewEncoder(conn).Encode(intent); err != nil {
		return LaunchReceipt{}, err
	}
	var reply BrokerReply
	if err = json.NewDecoder(io.LimitReader(conn, 65536)).Decode(&reply); err != nil {
		return LaunchReceipt{}, fmt.Errorf("launch outcome unknown; inspect request %s before retrying", intent.RequestID)
	}
	if reply.Error != "" {
		return LaunchReceipt{}, fmt.Errorf("%s", reply.Error)
	}
	if reply.Receipt == nil || reply.Receipt.SessionID != intent.RequestID || reply.Receipt.Agent != intent.Agent {
		return LaunchReceipt{}, fmt.Errorf("invalid launch receipt")
	}
	return *reply.Receipt, nil
}
