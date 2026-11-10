package setup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func InstallTerminalBinary(ctx context.Context, client *ssh.Client, artifacts string) error {
	run := func(command string) ([]byte, error) {
		session, err := client.NewSession()
		if err != nil {
			return nil, err
		}
		defer session.Close()
		stop := context.AfterFunc(ctx, func() { session.Close() })
		defer stop()
		return session.Output(command)
	}
	architecture, err := run("uname -m")
	if err != nil {
		return err
	}
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[strings.TrimSpace(string(architecture))]
	if arch == "" {
		return fmt.Errorf("unsupported terminal service architecture")
	}
	data, err := os.ReadFile(filepath.Join(artifacts, "fennec-terminal-linux-"+arch))
	if err != nil {
		return fmt.Errorf("bundled Go terminal service unavailable: %w", err)
	}
	if len(data) > 32<<20 || len(data) < 4 || !bytes.Equal(data[:4], []byte{0x7f, 'E', 'L', 'F'}) {
		return fmt.Errorf("invalid terminal service artifact")
	}
	digest := sha256.Sum256(data)
	version := hex.EncodeToString(digest[:])
	transfer, err := sftp.NewClient(client, sftp.UseConcurrentWrites(true))
	if err != nil {
		return err
	}
	defer transfer.Close()
	stop := context.AfterFunc(ctx, func() { transfer.Close() })
	defer stop()
	home, err := transfer.RealPath(".")
	if err != nil {
		return err
	}
	root := path.Join(home, ".local/fennec/terminal", version)
	binary := path.Join(root, "fennec-terminal")
	installed := false
	if info, err := transfer.Stat(binary); err == nil && info.Size() == int64(len(data)) && info.Mode().Perm()&0111 != 0 {
		actual, err := run("sha256sum " + shellQuote(binary))
		fields := strings.Fields(string(actual))
		installed = err == nil && len(fields) > 0 && fields[0] == version
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	suffix := hex.EncodeToString(nonce[:])
	if !installed {
		if err := transfer.MkdirAll(root); err != nil {
			return err
		}
		if err := transfer.Chmod(root, 0700); err != nil {
			return err
		}
		staged := path.Join(root, ".upload-"+suffix)
		file, err := transfer.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return err
		}
		defer transfer.Remove(staged)
		_, copyErr := file.ReadFrom(bytes.NewReader(data))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		actual, err := run("sha256sum " + shellQuote(staged))
		fields := strings.Fields(string(actual))
		if err != nil || len(fields) == 0 || fields[0] != version {
			return fmt.Errorf("terminal service upload verification failed")
		}
		if err := transfer.Chmod(staged, 0700); err != nil {
			return err
		}
		if err := transfer.PosixRename(staged, binary); err != nil {
			return err
		}
	}
	bin := path.Join(home, ".local/fennec/bin")
	if err := transfer.MkdirAll(bin); err != nil {
		return err
	}
	if err := transfer.Chmod(bin, 0700); err != nil {
		return err
	}
	entry := path.Join(bin, "fennec-terminal")
	if target, err := transfer.ReadLink(entry); err == nil && target == binary {
		return nil
	}
	link := path.Join(bin, ".terminal-"+suffix)
	if err := transfer.Symlink(binary, link); err != nil {
		return err
	}
	defer transfer.Remove(link)
	return transfer.PosixRename(link, entry)
}
