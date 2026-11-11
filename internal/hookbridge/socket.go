package hookbridge

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

var ErrBusy = errors.New("hook relay already attached")

const wireLimit = 2*PayloadLimit + 4096

func SocketPath(home, id string) (string, error) {
	if !sessionid.Valid(id) {
		return "", fmt.Errorf("invalid hook session")
	}
	return filepath.Join(directory(home), id+".sock"), nil
}
func Listen(ctx context.Context, home, id string, handle func(context.Context, Binding, []byte) Result) (func(), error) {
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
	if old, e := os.Lstat(path); e == nil {
		if old.Mode()&os.ModeSocket == 0 {
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
	var once sync.Once
	closeListener := func() { once.Do(func() { cancel(); listener.Close(); lock.Close() }) }
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
				decoder := json.NewDecoder(io.LimitReader(connection, wireLimit))
				decoder.DisallowUnknownFields()
				var request Request
				if decoder.Decode(&request) != nil || len(request.Input) > PayloadLimit {
					_ = json.NewEncoder(connection).Encode(Unregistered())
					return
				}
				binding, err := Lookup(home, id, request.BindingID)
				if err != nil {
					_ = json.NewEncoder(connection).Encode(Unregistered())
					return
				}
				connection.SetDeadline(time.Now().Add(binding.Wait() + time.Second))
				step, done := context.WithTimeout(lifetime, binding.Wait())
				defer done()
				result := handle(step, binding, request.Input)
				result = Normalize(binding, result)
				_ = json.NewEncoder(connection).Encode(result)
			}()
		}
	}()
	return closeListener, nil
}
func Invoke(ctx context.Context, home, session, reference string, input io.Reader) Result {
	payload, err := io.ReadAll(io.LimitReader(input, PayloadLimit+1))
	binding, lookupErr := Lookup(home, session, reference)
	if lookupErr != nil {
		return Unregistered()
	}
	if err != nil || len(payload) > PayloadLimit {
		return Failure(binding, "input exceeded the limit")
	}
	path, err := SocketPath(home, session)
	if err != nil {
		return Failure(binding, "has an invalid session")
	}
	connection, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "unix", path)
	if err != nil {
		return Failure(binding, "is unavailable")
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	connection.SetDeadline(time.Now().Add(binding.Wait() + time.Second))
	if json.NewEncoder(connection).Encode(Request{BindingID: reference, Input: payload}) != nil {
		return Failure(binding, "could not be delivered")
	}
	var result Result
	if json.NewDecoder(io.LimitReader(connection, wireLimit)).Decode(&result) != nil || !result.Valid() {
		return Failure(binding, "did not return a confirmed response")
	}
	return Normalize(binding, result)
}
