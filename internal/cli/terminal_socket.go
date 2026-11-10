package cli

import (
	"bufio"
	"context"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func offerTerminal(ctx context.Context, transport *terminalstream.Transport, directory string, request cloudruntime.LocalLaunch) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	return offerTerminalFrame(ctx, transport, directory, terminalstream.Frame{Type: "launch", ID: request.ID, Launch: &request})
}

func offerTerminalFrame(ctx context.Context, transport *terminalstream.Transport, directory string, opening terminalstream.Frame) (string, error) {
	if !fenID.MatchString(opening.ID) {
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
		var hello terminalstream.Frame
		if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &hello) != nil || hello.Type != "connect" || hello.ID != opening.ID {
			return
		}
		connection.SetReadDeadline(time.Time{})
		opening.Rows, opening.Columns = hello.Rows, hello.Columns
		opening.Term = hello.Term
		opening.Display = hello.Display
		session, err := transport.Launch(opening)
		encoder := json.NewEncoder(connection)
		if err != nil {
			encoder.Encode(terminalstream.Frame{Type: "error", ID: opening.ID, Message: "terminal operation outcome unknown; inspect session before retrying"})
			return
		}
		defer session.Close()
		go func() {
			defer cancel()
			for scanner.Scan() {
				var frame terminalstream.Frame
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
				encoder.Encode(terminalstream.Frame{Type: "error", ID: opening.ID, Message: "terminal connection lost; reconnect to existing session"})
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
