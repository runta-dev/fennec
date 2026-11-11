package localssh

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// PairRuntime creates the private client key inside the runtime and imports only
// its public key to this device. The private key never crosses to the Mac.
func PairRuntime(ctx context.Context, client *ssh.Client, directory, deviceID string, owner ...string) error {
	if _, err := DeviceAlias("machine", deviceID); err != nil {
		return err
	}
	script := `set -eu
umask 077
mkdir -p "$HOME/.ssh/fennec"
key="$HOME/.ssh/fennec/` + deviceID + `"
if [ ! -f "$key" ]; then ssh-keygen -q -t ed25519 -N '' -f "$key"; fi
ssh-keygen -y -f "$key"
`
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(script)
	type response struct {
		data []byte
		err  error
	}
	done := make(chan response, 1)
	go func() { data, err := session.Output("sh -s"); done <- response{data, err} }()
	var data []byte
	select {
	case r := <-done:
		if r.err != nil {
			return r.err
		}
		data = r.data
	case <-ctx.Done():
		session.Close()
		return ctx.Err()
	}
	if len(data) > 4096 {
		return fmt.Errorf("oversized pairing public key")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey(data)
	if err != nil || len(options) > 0 || len(strings.TrimSpace(string(rest))) > 0 || key.Type() != ssh.KeyAlgoED25519 {
		return fmt.Errorf("invalid pairing public key")
	}
	return addPairingKey(ctx, directory, key, owner...)
}

func addPairingKey(ctx context.Context, directory string, key ssh.PublicKey, owner ...string) error {
	return withPairingLock(ctx, directory, func() error {
		if len(owner) > 0 {
			current, err := os.ReadFile(filepath.Join(directory, "owner"))
			if err != nil || string(current) != ownerDigest(owner[0]) {
				return fmt.Errorf("pairing owner changed")
			}
		}
		path := filepath.Join(directory, "authorized_keys")
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		for _, line := range strings.Split(string(existing), "\n") {
			if strings.TrimSpace(line) == canonical {
				return nil
			}
		}
		// Merge under the pairing lock before atomically publishing the full list.
		temporary, err := os.CreateTemp(directory, "pairing-*")
		if err != nil {
			return err
		}
		defer os.Remove(temporary.Name())
		if _, err = temporary.Write(append(append(existing, '\n'), []byte(canonical+"\n")...)); err != nil {
			temporary.Close()
			return err
		}
		if err = temporary.Close(); err != nil {
			return err
		}
		return os.Rename(temporary.Name(), path)
	})
}

func withPairingLock(ctx context.Context, directory string, action func() error) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "pairing.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return action()
}

// InstallAlias adds only a Fennec-owned Include and generated device file. Existing
// user configuration is preserved; the Include is appended, never prepended.
func InstallAlias(ctx context.Context, client *ssh.Client, alias, address, deviceID, hostKey string, defaultDevice bool) error {
	_, _, err := AliasConfig(alias, address, deviceID, hostKey, defaultDevice)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(AliasRegistration{Alias: alias, Address: address, Device: deviceID, HostKey: hostKey, Default: defaultDevice})
	if err != nil {
		return err
	}
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(string(payload))
	done := make(chan error, 1)
	go func() { done <- session.Run(`"$HOME/.local/fennec/bin/fennec-terminal" register-alias`) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		session.Close()
		return ctx.Err()
	}
}
