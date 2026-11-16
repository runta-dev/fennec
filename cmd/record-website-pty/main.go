//go:build website_record

// record-website-pty captures bytes from tmux pipe-pane or records the production
// fen picker with isolated fixture data. Raw captures are private until reviewed.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fennec/internal/cli"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

type chunk struct {
	Time int64  `json:"t"`
	Data string `json:"b"`
}

func collect(reader io.Reader, path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	start := time.Now()
	buffer := make([]byte, 32768)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if e := encoder.Encode(chunk{time.Since(start).Milliseconds(), base64.StdEncoding.EncodeToString(buffer[:n])}); e != nil {
				return e
			}
		}
		if err != nil {
			return nil
		}
	}
}
func run() error {
	if len(os.Args) == 9 && os.Args[1] == "pack" {
		a, e := strconv.Atoi(os.Args[4])
		if e != nil {
			return e
		}
		b, e := strconv.Atoi(os.Args[5])
		if e != nil {
			return e
		}
		end, e := strconv.ParseInt(os.Args[6], 10, 64)
		if e != nil {
			return e
		}
		duration, e := strconv.ParseInt(os.Args[7], 10, 64)
		if e != nil {
			return e
		}
		if os.Args[8] != "reviewed" {
			return fmt.Errorf("review raw recording before publishing")
		}
		return pack(os.Args[2], os.Args[3], a, b, end, duration)
	}
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: record-website-pty pipe <private.ndjson> | picker <private.ndjson>")
	}
	if os.Args[1] == "picker-child" {
		return cli.RecordWebsitePicker()
	}
	if len(os.Args) != 3 {
		return fmt.Errorf("output path required")
	}
	if os.Args[1] == "pipe" {
		return collect(os.Stdin, os.Args[2])
	}
	if os.Args[1] != "picker" {
		return fmt.Errorf("unknown command")
	}
	dir, err := os.MkdirTemp("", "fennec-picker-record-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err = os.WriteFile(filepath.Join(dir, "fen"), []byte("#!/bin/sh\nexec "+quote(exe)+" picker-child\n"), 0700); err != nil {
		return err
	}
	command := exec.Command("/bin/bash", "--noprofile", "--norc", "-i")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "NO_COLOR=") && !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "PS1=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "TERM=xterm-256color", "PS1=relay main > ", "PATH="+dir+":"+os.Getenv("PATH"))
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 100, Rows: 32})
	if err != nil {
		return err
	}
	defer terminal.Close()
	defer command.Process.Kill()
	var wg sync.WaitGroup
	var recordErr error
	wg.Add(1)
	go func() { defer wg.Done(); recordErr = collect(terminal, os.Args[2]) }()
	time.Sleep(200 * time.Millisecond)
	for _, key := range []string{"f", "e", "n", "\r"} {
		terminal.Write([]byte(key))
		time.Sleep(150 * time.Millisecond)
	}
	time.Sleep(time.Second)
	terminal.Write([]byte("j"))
	time.Sleep(time.Second)
	terminal.Write([]byte("j"))
	time.Sleep(time.Second)
	terminal.Write([]byte("k"))
	time.Sleep(time.Second)
	terminal.Write([]byte("k"))
	time.Sleep(2 * time.Second)
	terminal.Write([]byte("q"))
	time.Sleep(200 * time.Millisecond)
	terminal.Write([]byte("exit\r"))
	command.Wait()
	wg.Wait()
	return recordErr
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
