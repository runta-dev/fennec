package launcher

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestLiveObservedSessionTitleFiltering(t *testing.T) {
	if os.Getenv("FENNEC_LIVE_TITLE_TEST") != "1" {
		t.Skip("explicit live observation required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := "219a43a1a2de5cc147206da1372251d3"
	directory := filepath.Join(home, ".config/fennec/cloud")
	command := exec.CommandContext(ctx, "/usr/bin/ssh", "-tt", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile="+filepath.Join(directory, "known_hosts"), "-i", filepath.Join(directory, "runtime_key"), "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e@ssh.runta.com", `"$HOME/.local/bin/herdr-core" attach --observe t2g`)
	terminal, err := pty.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer func() { command.Process.Kill(); command.Wait() }()
	var original, filtered bytes.Buffer
	writer := SessionTitleWriter{Writer: &filtered, SessionID: id}
	pattern := regexp.MustCompile("\x1b\\](?:0|2);([^\x07\x1b]*)(?:\x07|\x1b\\\\)")
	buffer := make([]byte, 4096)
	for original.Len() < 65536 {
		length, err := terminal.Read(buffer)
		if err != nil {
			t.Fatal("no complete title observed", err)
		}
		original.Write(buffer[:length])
		writer.Write(buffer[:length])
		for _, match := range pattern.FindAllSubmatch(original.Bytes(), -1) {
			title := string(match[1])
			clean := CleanSessionTitle(title, id)
			if clean == title {
				continue
			}
			for _, result := range pattern.FindAllSubmatch(filtered.Bytes(), -1) {
				if string(result[1]) == clean {
					return
				}
			}
			t.Fatal("title suffix was not removed")
		}
	}
	t.Fatal("no matching session title in observation window")
}
