package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"fennec/internal/launchprofile"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const rolloverLabel = "com.runta.fennec.rollover"

func rolloverPlist(binary string) []byte {
	var escaped bytes.Buffer
	xml.EscapeText(&escaped, []byte(binary))
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>` +
		`<key>Label</key><string>` + rolloverLabel + `</string>` +
		`<key>ProgramArguments</key><array><string>` + escaped.String() + `</string><string>start</string></array>` +
		`<key>RunAtLoad</key><false/><key>ProcessType</key><string>Interactive</string>` +
		`<key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string></dict></plist>`)
}

// launchd owns the starter independently of the daemon that it replaces.
// A setsid shell child is still a descendant of the retiring launchd job and
// can be terminated during bootout. The installed bundle is already verified
// and published before this is called. Native UI startup is the fallback.
func scheduleDetachedDaemonRollover(bundlePath string) {
	defer launchprofile.Begin("update.rollover.schedule")()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	directory := filepath.Join(home, ".config/fennec/ipc")
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return
	}
	path := filepath.Join(directory, "rollover.plist")
	data := rolloverPlist(filepath.Join(bundlePath, "Contents/MacOS/fen"))
	current, _ := os.ReadFile(path)
	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/" + rolloverLabel
	if !bytes.Equal(current, data) {
		file, err := os.CreateTemp(directory, ".rollover-*")
		if err != nil {
			return
		}
		defer os.Remove(file.Name())
		_, err = file.Write(data)
		closed := file.Close()
		if err != nil || closed != nil {
			return
		}
		if err := os.Rename(file.Name(), path); err != nil {
			return
		}
		// The application may have moved. Only replace our one-shot starter,
		// never the daemon here. ensureControl still owns daemon serialization.
		_ = exec.CommandContext(ctx, "/bin/launchctl", "bootout", target).Run()
	}
	if exec.CommandContext(ctx, "/bin/launchctl", "print", target).Run() != nil {
		if exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", domain, path).Run() != nil {
			launchprofile.Record("update.rollover.registration_failed", time.Now(), 0)
			return
		}
	}
	// No -k: another caller can already be starting this exact installed build.
	launchprofile.Record("update.rollover.request", time.Now(), 0)
	if exec.CommandContext(ctx, "/bin/launchctl", "kickstart", target).Run() != nil {
		launchprofile.Record("update.rollover.start_failed", time.Now(), 0)
	}
}
