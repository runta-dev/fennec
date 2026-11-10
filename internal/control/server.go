package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func Serve(ctx context.Context, path string, handlers map[string]Handler, ready func()) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute control socket required")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private control directory required")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("control daemon already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("control path is not a socket")
		}
		connection, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err == nil {
			connection.Close()
			return fmt.Errorf("control socket already active")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	if ready != nil {
		ready()
	}
	slots := make(chan struct{}, 32)
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !sameUser(connection) {
			connection.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			connection.Close()
			continue
		}
		go func() {
			defer func() { <-slots; connection.Close() }()
			closeOnStop := context.AfterFunc(ctx, func() { connection.Close() })
			defer closeOnStop()
			connection.SetReadDeadline(time.Now().Add(5 * time.Second))
			var request Request
			if json.NewDecoder(io.LimitReader(connection, 1<<20)).Decode(&request) != nil {
				return
			}
			response := Response{Version: Version, ID: request.ID}
			switch {
			case request.Version != Version:
				response.Error = &Fault{Code: "version_mismatch", Message: "client and daemon protocol versions differ"}
			case request.ID == "" || len(request.ID) > 64:
				response.Error = &Fault{Code: "invalid_request", Message: "request identity required"}
			case handlers[request.Method] == nil:
				response.Error = &Fault{Code: "method_not_found", Message: "unsupported control operation"}
			default:
				operation, cancel := context.WithTimeout(ctx, 30*time.Second)
				result, err := handlers[request.Method](operation, request.Params)
				cancel()
				if err != nil {
					var fault *Fault
					if errors.As(err, &fault) {
						response.Error = fault
					} else {
						response.Error = &Fault{Code: "internal", Message: "control operation failed"}
					}
				} else {
					response.Result, err = json.Marshal(result)
					if err != nil {
						response.Error = &Fault{Code: "internal", Message: "control response encoding failed"}
					}
				}
			}
			connection.SetWriteDeadline(time.Now().Add(5 * time.Second))
			json.NewEncoder(connection).Encode(response)
		}()
	}
}
