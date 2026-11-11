package terminalstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fennec/internal/sessionid"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func OfferLocal(ctx context.Context, transport *Transport, directory string, opening Frame, existing ...*Session) (string, error) {
	if !sessionid.Valid(opening.ID) {
		return "", fmt.Errorf("invalid stream identity")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("terminal socket directory must be private")
	}
	path := filepath.Join(directory, "terminal-"+opening.ID)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return "", err
	}
	listener.SetDeadline(time.Now().Add(30 * time.Second))
	go func() {
		defer listener.Close()
		stop := context.AfterFunc(ctx, func() { listener.Close() })
		defer stop()
		connection, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer connection.Close()
		listener.Close()
		operation, cancel := context.WithCancel(ctx)
		defer cancel()
		closeConnection := context.AfterFunc(operation, func() { connection.Close() })
		defer closeConnection()
		connection.SetReadDeadline(time.Now().Add(5 * time.Second))
		scanner := bufio.NewScanner(connection)
		scanner.Buffer(make([]byte, 4096), 262144)
		var hello Frame
		if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &hello) != nil || hello.Type != "connect" || hello.ID != opening.ID {
			return
		}
		connection.SetReadDeadline(time.Time{})
		opening.Rows, opening.Columns = hello.Rows, hello.Columns
		opening.Term = hello.Term
		opening.Display = hello.Display
		opening.HookBridge = hello.HookBridge
		if opening.HookBridge {
			if err := transport.Ready(operation); err != nil {
				return
			}
			if !transport.Supports("hook_bridge_v1") {
				json.NewEncoder(connection).Encode(Frame{Type: "error", ID: opening.ID, Message: "Update Fennec to use local cloud hooks"})
				return
			}
		}
		var session *Session
		if len(existing) > 0 {
			session = existing[0]
			err = session.Send(Frame{Type: "resize", Rows: hello.Rows, Columns: hello.Columns})
		} else {
			session, err = transport.Launch(opening)
		}
		encoder := json.NewEncoder(connection)
		if err != nil {
			encoder.Encode(Frame{Type: "error", ID: opening.ID, Message: "terminal operation outcome unknown; inspect session before retrying"})
			return
		}
		defer session.Close()
		go func() {
			defer cancel()
			for scanner.Scan() {
				var frame Frame
				if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.ID != opening.ID {
					return
				}
				if err := session.Send(frame); err != nil {
					return
				}
				if frame.Type == "close" {
					return
				}
			}
		}()
		for {
			frame, err := session.Next(operation)
			if err != nil {
				encoder.Encode(Frame{Type: "error", ID: opening.ID, Message: "terminal connection lost; reconnect to existing session"})
				return
			}
			if encoder.Encode(frame) != nil {
				return
			}
			if frame.Type == "exit" {
				select {
				case <-operation.Done():
				case <-time.After(time.Second):
				}
				return
			}
			if frame.Type == "error" {
				return
			}
		}
	}()
	return path, nil
}
