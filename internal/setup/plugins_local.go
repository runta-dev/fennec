package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func validateModelPlugins(plugins []ModelPlugin) error {
	seen := map[string]bool{}
	for _, plugin := range plugins {
		if !isAllowedModelPlugin(plugin.Package) || !exactVersion.MatchString(plugin.Version) || seen[plugin.Package] {
			return fmt.Errorf("unsupported or duplicate model plugin")
		}
		seen[plugin.Package] = true
	}
	return nil
}

func ReconcileLocalModelPlugins(ctx context.Context, home string, plugins []ModelPlugin) error {
	added := false
	err := reconcileLocalModelPlugins(ctx, home, plugins, func(ctx context.Context, args ...string) error {
		added = true
		return exec.CommandContext(ctx, "magpie", args...).Run()
	})
	if err != nil || !added {
		return err
	}
	// Reload the supervised gateway process when a new plugin was installed so
	// /v1/models immediately exposes the newly added plugin's models.
	_ = exec.CommandContext(ctx, "/usr/bin/pkill", "-f", "/magpie serve").Run()
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			break
		}
		resp, reqErr := client.Get("http://127.0.0.1:3425/v1/models")
		if reqErr == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

func reconcileLocalModelPlugins(ctx context.Context, home string, plugins []ModelPlugin, run func(context.Context, ...string) error) error {
	if err := validateModelPlugins(plugins); err != nil {
		return err
	}
	for _, plugin := range plugins {
		if err := ctx.Err(); err != nil {
			return err
		}
		installed, err := modelPluginInstalled(home, plugin)
		if err != nil {
			return err
		}
		if installed {
			continue
		}
		step, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err = run(step, "plugin", "add", plugin.Package+"@"+plugin.Version)
		cancel()
		if err != nil {
			return fmt.Errorf("model plugin installation failed")
		}
		installed, err = modelPluginInstalled(home, plugin)
		if err != nil || !installed {
			return fmt.Errorf("model plugin installation verification failed")
		}
	}
	return nil
}

func modelPluginInstalled(home string, plugin ModelPlugin) (bool, error) {
	root := filepath.Join(home, ".config/magpie")
	path := filepath.Join(root, "plugins.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return false, fmt.Errorf("invalid model plugin manifest")
	}
	data, err := os.ReadFile(path)
	var manifest struct {
		Plugins []struct {
			Spec string `json:"spec"`
		} `json:"plugins"`
	}
	if err != nil || json.Unmarshal(data, &manifest) != nil {
		return false, fmt.Errorf("invalid model plugin manifest")
	}
	configured := false
	for _, entry := range manifest.Plugins {
		if entry.Spec == plugin.Package+"@"+plugin.Version {
			configured = true
		}
	}
	if !configured {
		return false, nil
	}
	found := false
	err = filepath.WalkDir(filepath.Join(root, "plugins"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "package.json" || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		var actual struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err == nil && json.Unmarshal(data, &actual) == nil && actual.Name == plugin.Package && actual.Version == plugin.Version {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return found, err
}
