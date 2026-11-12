package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var allowedPluginCredentialBase = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func GatherCredentialPayload(home string) (map[string]string, error) {
	files := map[string]string{}
	for _, name := range []string{"providers.json", "logins.json", "plugin-auth.json", "migrations.json"} {
		path := filepath.Join(home, ".config", "magpie", name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot inspect Magpie credential store")
		}
		if !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil, fmt.Errorf("invalid Magpie credential store")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("cannot open Magpie credential store")
		}
		data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		file.Close()
		if err != nil || len(data) > 4<<20 || !json.Valid(data) {
			return nil, fmt.Errorf("invalid Magpie credential JSON")
		}
		if name == "plugin-auth.json" {
			var entries map[string]json.RawMessage
			if json.Unmarshal(data, &entries) != nil {
				return nil, fmt.Errorf("invalid plugin credential store")
			}
			selected := map[string]json.RawMessage{}
			for id, value := range entries {
				base := strings.SplitN(id, "#", 2)[0]
				if allowedPluginCredentialBase.MatchString(base) {
					selected[id] = value
				}
			}
			if len(selected) == 0 {
				continue
			}
			data, _ = json.Marshal(selected)
		}
		files[name] = base64.StdEncoding.EncodeToString(data)
	}
	return files, nil
}

// Credentials travel only through the authenticated SSH stdin channel.
func MigrateMagpieCredentials(ctx context.Context, client *ssh.Client, home string) (int, error) {
	files, err := GatherCredentialPayload(home)
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(files)
	if err != nil {
		return 0, fmt.Errorf("cannot encode credential migration")
	}
	session, err := client.NewSession()
	if err != nil {
		return 0, err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(string(payload))
	session.Stdout = io.Discard
	session.Stderr = io.Discard
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	if err = session.Run("\"$HOME/.local/fennec/bin/fennec-terminal\" migrate-credentials"); err != nil {
		return 0, fmt.Errorf("credential migration unavailable; existing remote credentials preserved")
	}
	return len(files), nil
}
