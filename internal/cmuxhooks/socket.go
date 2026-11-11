package cmuxhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/sessionid"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var ErrBusy = errors.New("cmux hook relay already attached")

type Request struct {
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

func SocketPath(home, id string) (string, error) {
	if !sessionid.Valid(id) {
		return "", fmt.Errorf("invalid hook session")
	}
	return filepath.Join(home, ".local/fennec/cmux-hooks", id+".sock"), nil
}
func Listen(ctx context.Context, home, id string, handle func(context.Context, string, []byte) Response) (func(), error) {
	path, err := SocketPath(home, id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private hook directory required")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, err
	}
	if existing, e := os.Lstat(path); e == nil {
		if existing.Mode()&os.ModeSocket == 0 {
			lock.Close()
			return nil, fmt.Errorf("hook endpoint is not a socket")
		}
		if e = os.Remove(path); e != nil {
			lock.Close()
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		lock.Close()
		return nil, e
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		lock.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		lock.Close()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifetime, func() { listener.Close() })
	var once sync.Once
	closeListener := func() { once.Do(func() { cancel(); stop(); listener.Close(); lock.Close() }) }
	context.AfterFunc(lifetime, closeListener)
	slots := make(chan struct{}, 16)
	go func() {
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				connection.Close()
				continue
			}
			go func() {
				defer connection.Close()
				defer func() { <-slots }()
				connection.SetDeadline(time.Now().Add(2 * time.Second))
				var request Request
				decoder := json.NewDecoder(io.LimitReader(connection, PayloadLimit+1024))
				decoder.DisallowUnknownFields()
				if decoder.Decode(&request) != nil || !ValidEvent(request.Event) || len(request.Payload) > PayloadLimit || !json.Valid(request.Payload) {
					return
				}
				connection.SetDeadline(time.Now().Add(Timeout(request.Event) + time.Second))
				step, done := context.WithTimeout(lifetime, Timeout(request.Event))
				defer done()
				response := handle(step, request.Event, request.Payload)
				if len(response.Data) > PayloadLimit || !json.Valid(response.Data) || (response.Code != 0 && response.Code != 2) {
					response = Empty()
				}
				_ = json.NewEncoder(connection).Encode(response)
			}()
		}
	}()
	return closeListener, nil
}

// Invoke is fail-open only for cmux's notification/metadata integration. It does
// not change the behavior of other hooks or grant permission to run tools.
func Invoke(ctx context.Context, home, id, event string, input io.Reader) Response {
	if !ValidEvent(event) {
		return Empty()
	}
	path, err := SocketPath(home, id)
	if err != nil {
		return Empty()
	}
	payload, err := io.ReadAll(io.LimitReader(input, PayloadLimit+1))
	if err != nil || len(payload) > PayloadLimit || !json.Valid(payload) {
		return Empty()
	}
	connection, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "unix", path)
	if err != nil {
		return Empty()
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	connection.SetDeadline(time.Now().Add(Timeout(event) + time.Second))
	if json.NewEncoder(connection).Encode(Request{Event: event, Payload: payload}) != nil {
		return Empty()
	}
	var response Response
	if json.NewDecoder(io.LimitReader(connection, PayloadLimit+1024)).Decode(&response) != nil || len(response.Data) > PayloadLimit || !json.Valid(response.Data) || (response.Code != 0 && response.Code != 2) {
		return Empty()
	}
	return response
}
