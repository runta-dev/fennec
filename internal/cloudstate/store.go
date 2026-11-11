// Package cloudstate persists intentional cloud deletion across daemon restarts.
package cloudstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrDisabled = errors.New("cloud computer creation requires an explicit user action")

type Record struct {
	Owner    string `json:"owner"`
	Runtime  string `json:"runtime"`
	Request  string `json:"request"`
	Revision uint64 `json:"revision"`
	Deleted  bool   `json:"deleted"`
	Rejected bool   `json:"rejected,omitempty"`
}

// One daemon owns mutations; readers always observe a complete atomic record.
type Store struct {
	Directory string
	mu        sync.Mutex
}

func New(home string) *Store {
	// Local uninstall must not erase an unresolved remote deletion or cause
	// reinstall to provision a replacement without a new user decision.
	return &Store{Directory: filepath.Join(home, ".local/state/fennec-cloud-lifecycle")}
}
func (s *Store) path(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return filepath.Join(s.Directory, hex.EncodeToString(sum[:])+".json")
}
func valid(r Record) bool {
	return r.Owner != "" && r.Runtime != "" && r.Request != "" && !(r.Deleted && r.Rejected)
}
func (s *Store) Read(owner string) (Record, error) {
	if owner == "" {
		return Record{}, fmt.Errorf("cloud owner required")
	}
	path := s.path(owner)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Record{}, nil
	}
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return Record{}, fmt.Errorf("invalid cloud lifecycle record")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if json.Unmarshal(raw, &r) != nil || !valid(r) || r.Owner != owner {
		return Record{}, fmt.Errorf("cloud lifecycle identity mismatch")
	}
	return r, nil
}
func (s *Store) Check(owner string) error {
	r, err := s.Read(owner)
	if err != nil {
		return err
	}
	if r.Request != "" {
		return ErrDisabled
	}
	return nil
}

// Begin must be durable before issuing DELETE or allowing service teardown.
func (s *Store) Begin(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !valid(r) || r.Deleted || r.Rejected {
		return fmt.Errorf("pending deletion identity required")
	}
	old, err := s.Read(r.Owner)
	if err != nil {
		return err
	}
	if old.Request != "" {
		if old.Rejected && old.Owner == r.Owner && old.Runtime == r.Runtime && old.Request != r.Request {
			return s.write(r)
		}
		if old.Rejected {
			return fmt.Errorf("review a new deletion preview")
		}
		if old.Owner == r.Owner && old.Runtime == r.Runtime && old.Request == r.Request && old.Revision == r.Revision {
			return nil
		}
		return fmt.Errorf("another cloud deletion requires resolution")
	}
	return s.write(r)
}

// Confirm records verified deletion only, never merely an accepted DELETE RPC.
func (s *Store) Confirm(owner, runtime, request string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.Read(owner)
	if err != nil {
		return err
	}
	if !valid(old) || old.Runtime != runtime || old.Request != request {
		return fmt.Errorf("cloud deletion identity changed")
	}
	old.Deleted = true
	old.Rejected = false
	return s.write(old)
}

// AllowCreation is an explicit user decision after confirmed deletion. Unknown
// outcomes remain blocked; retries must continue checking the original runtime.
func (s *Store) AllowCreation(owner, runtime, request string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.Read(owner)
	if err != nil {
		return err
	}
	if !old.Deleted || old.Runtime != runtime || old.Request != request {
		return fmt.Errorf("confirm cloud deletion before creating another computer")
	}
	if err = os.Remove(s.path(owner)); err != nil {
		return err
	}
	return syncDirectory(s.Directory)
}
func (s *Store) write(r Record) error {
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Directory, ".cloud-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), s.path(r.Owner)); err != nil {
		return err
	}
	return syncDirectory(s.Directory)
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Reject permits a newly confirmed preview only after a definitive refusal.
// A timeout or failed read must never call this method.
func (s *Store) Reject(owner, runtime, request string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.Read(owner)
	if err != nil {
		return err
	}
	if !valid(old) || old.Runtime != runtime || old.Request != request || old.Deleted {
		return fmt.Errorf("cloud deletion identity changed")
	}
	old.Rejected = true
	return s.write(old)
}
