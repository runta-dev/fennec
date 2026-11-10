package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"fennec/internal/syncengine"
)

// syncCommand observes or flushes one existing Fennec session. It never creates
// a session, expands scope, resolves conflicts or terminates another sync.
func syncCommand(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: fennec sync list|status|flush|pause|resume|remove [--state-dir PATH SESSION]")
	}
	operation := arguments[0]
	if operation != "status" && operation != "flush" && operation != "pause" && operation != "resume" && operation != "list" && operation != "remove" {
		return fmt.Errorf("sync supports list, status, flush, pause, resume or remove")
	}
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(output)
	state := flags.String("state-dir", "", "Fennec-managed Mutagen data directory")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if operation == "list" {
		if *state != "" || flags.NArg() != 0 {
			return fmt.Errorf("sync list takes no arguments")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		entries, err := syncengine.ListProjects(filepath.Join(home, ".config/fennec/projects"))
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(entries)
	}
	name := flags.Arg(0)
	manager := syncengine.Manager{DataDirectory: *state}
	if *state == "" && flags.NArg() == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		registered, err := syncengine.FindProject(filepath.Join(home, ".config/fennec/projects"), cwd)
		if err != nil {
			return err
		}
		name = registered.Session
		manager.DataDirectory = registered.DataDirectory
		manager.SSHDirectory = registered.SSHDirectory
	} else if !filepath.IsAbs(*state) || flags.NArg() != 1 {
		return fmt.Errorf("absolute --state-dir and one existing session required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if operation == "remove" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		registry := filepath.Join(home, ".config/fennec/projects")
		err = syncengine.WithRegistryLock(ctx, registry, func() error {
			if err := manager.Remove(ctx, name); err != nil {
				return err
			}
			return syncengine.Unregister(registry, manager.DataDirectory, name)
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]any{"removed": name, "files_preserved": true})
	}
	var session syncengine.Session
	if operation == "pause" || operation == "resume" {
		s, err := manager.SetPaused(ctx, name, operation == "pause")
		if err != nil {
			return err
		}
		session = s
	} else if operation == "flush" {
		s, err := manager.Flush(ctx, name)
		if err != nil {
			return err
		}
		session = s
	} else {
		sessions, err := manager.Inspect(ctx, name)
		if err != nil {
			return err
		}
		if len(sessions) != 1 {
			return fmt.Errorf("one existing session required")
		}
		session = sessions[0]
	}
	ready := session.Ready()
	if err := json.NewEncoder(output).Encode(struct {
		Ready   bool               `json:"ready"`
		Session syncengine.Session `json:"session"`
	}{ready, session}); err != nil {
		return err
	}
	if operation == "flush" && !ready {
		return fmt.Errorf("sync not confirmed: inspect conflicts, connection and scan status")
	}
	return nil
}
