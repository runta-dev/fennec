package desktopactivity

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

//go:embed assets/done.mp3
var doneSound []byte

//go:embed assets/request.mp3
var requestSound []byte

// Sound preferences belong to Desktop, never the menubar or a Herdr installation.
type Sounds struct {
	Enabled *bool  `json:"enabled"`
	Done    string `json:"done"`
	Blocked string `json:"blocked"`
}

func Play(home, kind string) {
	dir := filepath.Join(home, ".config", "fennec")
	var prefs Sounds
	if b, e := os.ReadFile(filepath.Join(dir, "desktop-sounds.json")); e == nil {
		_ = json.Unmarshal(b, &prefs)
	}
	if prefs.Enabled != nil && !*prefs.Enabled {
		return
	}
	data := doneSound
	custom := prefs.Done
	if kind == "blocked" {
		data = requestSound
		custom = prefs.Blocked
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if custom != "" {
			if !filepath.IsAbs(custom) {
				custom = filepath.Join(dir, custom)
			}
			if exec.CommandContext(ctx, "/usr/bin/afplay", custom).Run() == nil {
				return
			}
		}
		f, e := os.CreateTemp("", "fennec-notification-*.mp3")
		if e != nil {
			return
		}
		defer os.Remove(f.Name())
		if _, e = f.Write(data); e != nil {
			f.Close()
			return
		}
		if f.Close() != nil {
			return
		}
		_ = exec.CommandContext(ctx, "/usr/bin/afplay", f.Name()).Run()
	}()
}
