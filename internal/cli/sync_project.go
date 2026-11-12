package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fennec/internal/project"
	"fennec/internal/runta"
	"fennec/internal/setup"
	"fennec/internal/syncengine"
)

// syncProject accepts an explicitly selected project, not a guessed home folder.
// It never starts an agent and refuses to report ready if synchronization conflicts.
func syncProject(input io.Reader, output io.Writer) error {
	var request struct {
		Token     string `json:"token"`
		RuntimeID string `json:"runtime_id"`
		Directory string `json:"directory"`
		Device    string `json:"device"`
	}
	if err := json.NewDecoder(io.LimitReader(input, 65536)).Decode(&request); err != nil {
		return err
	}
	if request.Token == "" || request.RuntimeID == "" || request.Device == "" || !filepath.IsAbs(request.Directory) {
		return fmt.Errorf("authenticated runtime, device and absolute project directory required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Inspect local project before mutating remote state.
	manifest, err := project.ManifestGit(ctx, request.Directory)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	api := runta.NewClient(home, request.Token)
	user, err := api.CurrentUser(ctx)
	if err != nil {
		return err
	}
	computer, err := api.Get(ctx, request.RuntimeID)
	if err != nil {
		return err
	}
	if computer.Owner != user || computer.Environment["FENNEC_PURPOSE"] != "shared-v1" {
		return fmt.Errorf("cloud computer ownership mismatch")
	}
	if computer.Status != "running" || computer.Degraded {
		return fmt.Errorf("cloud computer not ready")
	}
	identity := filepath.Join(home, ".config/fennec/cloud")
	client, err := api.Connect(ctx, request.RuntimeID, identity)
	if err != nil {
		return err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	data, err := session.Output("printf '%s' \"$HOME\"")
	session.Close()
	if err != nil {
		return err
	}
	mapping, err := syncengine.MapProject(user, request.Device, manifest.Root, request.Directory, string(data))
	if err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err = setup.Exec(ctx, client, "umask 077\nmkdir -p -- "+quote(mapping.CloudDirectory)+"\n"); err != nil {
		return err
	}
	scope := sha256.Sum256([]byte(user + "\x00" + request.RuntimeID))
	state := filepath.Join(home, ".config/fennec/sync", hex.EncodeToString(scope[:16]))
	wrapper := filepath.Join(state, "ssh")
	alias, err := syncengine.ConfigureSSH(wrapper, request.RuntimeID, filepath.Join(identity, "runtime_key"), filepath.Join(identity, "known_hosts"))
	if err != nil {
		return err
	}
	binary, err := exec.LookPath("mutagen")
	if err != nil {
		for _, path := range []string{"/opt/homebrew/bin/mutagen", "/usr/local/bin/mutagen"} {
			if info, e := os.Stat(path); e == nil && info.Mode()&0111 != 0 {
				binary = path
				break
			}
		}
	}
	if binary == "" {
		return fmt.Errorf("Mutagen is not installed")
	}
	manager := syncengine.Manager{Binary: binary, DataDirectory: filepath.Join(state, "data"), SSHDirectory: wrapper}
	name, err := syncengine.SessionName(user, mapping.LocalRoot, alias+":"+mapping.CloudRoot)
	if err != nil {
		return err
	}
	registry := filepath.Join(home, ".config/fennec/projects")
	registration := syncengine.Registration{Root: mapping.LocalRoot, Session: name, DataDirectory: manager.DataDirectory, SSHDirectory: manager.SSHDirectory}
	var synced syncengine.Session
	err = syncengine.WithRegistryLock(ctx, registry, func() error {
		if err := syncengine.CheckOverlap(registry, registration); err != nil {
			return err
		}
		var err error
		synced, err = manager.Ensure(ctx, user, mapping.LocalRoot, alias+":"+mapping.CloudRoot)
		if err != nil {
			return err
		}
		// Register before flushing so timeouts/conflicts remain discoverable.
		if err = syncengine.Register(registry, registration); err != nil {
			return err
		}
		synced, err = manager.Flush(ctx, synced.Name)
		return err
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Mapping syncengine.ProjectMapping `json:"mapping"`
		Sync    syncengine.Session        `json:"sync"`
		Ready   bool                      `json:"ready"`
	}{mapping, synced, synced.Ready()})
}
