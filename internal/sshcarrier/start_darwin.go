//go:build darwin

package sshcarrier

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// A separate launchd job survives bootout of the main daemon. setsid alone is
// insufficient because launchd also cleans up a retiring job's descendants.
func startWorker(ctx context.Context, base, key string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	label := "com.runta.fennec.transport." + key
	escape := func(value string) string {
		var out strings.Builder
		xml.EscapeText(&out, []byte(value))
		return out.String()
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>` +
		`<key>Label</key><string>` + label + `</string><key>ProgramArguments</key><array><string>` + escape(executable) + `</string><string>transport-worker</string><string>` + escape(base+".json") + `</string></array>` +
		`<key>RunAtLoad</key><true/><key>ProcessType</key><string>Interactive</string><key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>` + escape(base+".log") + `</string></dict></plist>`
	if err := os.WriteFile(base+".plist", []byte(plist), 0600); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if err := exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", domain, base+".plist").Run(); err == nil {
		return nil
	}
	// An idle worker exits but its registration remains. Restart that exact job,
	// without -k, so simultaneous callers never kill a live transport.
	if err := exec.CommandContext(ctx, "/bin/launchctl", "kickstart", domain+"/"+label).Run(); err != nil {
		return fmt.Errorf("start transport service: %w", err)
	}
	return nil
}
