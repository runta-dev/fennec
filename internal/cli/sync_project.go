package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fennec/internal/project"
	"fennec/internal/runta"
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
	session, err := openManagedCommand(ctx, home, user, request.RuntimeID)
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
	session, err = openManagedCommand(ctx, home, user, request.RuntimeID)
	if err != nil {
		return err
	}
	err = session.Run("umask 077\nmkdir -p -- " + quote(mapping.CloudDirectory) + "\n")
	session.Close()
	if err != nil {
		return err
	}
	state := managedSyncState(home, user, request.RuntimeID)
	wrapper := filepath.Join(state, "ssh")
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	alias, err := syncengine.ConfigureManagedSSH(wrapper, executable, user, request.RuntimeID)
	if err != nil {
		return err
	}
	binary, err := findMutagen()
	if err != nil {
		return err
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
		synced, err = manager.EnsureManaged(ctx, registry, user, mapping.LocalRoot, alias+":"+mapping.CloudRoot)
		if err != nil {
			return err
		}
		// EnsureManaged registers before flushing, retaining migration intent
		// and keeping timeouts/conflicts discoverable.
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
