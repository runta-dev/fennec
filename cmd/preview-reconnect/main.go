// preview-reconnect runs the production terminal recovery UI against an isolated
// local socket. It never changes the installed application or contacts the cloud.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/launcher"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	outage := flag.Duration("outage", 14*time.Second, "simulated outage before the next retry can succeed, zero retries until Ctrl+C")
	screenFile := flag.String("screen-file", "", "retained frame as text, ANSI, or a sessions.read JSON response")
	flag.Parse()
	retained := ""
	if *screenFile != "" {
		data, err := os.ReadFile(*screenFile)
		if err != nil {
			return err
		}
		var read struct {
			Screen struct {
				Text string `json:"text"`
			} `json:"screen"`
		}
		if json.Unmarshal(data, &read) == nil && read.Screen.Text != "" {
			retained = strings.ReplaceAll(read.Screen.Text, "\n", "\r\n")
		} else {
			retained = string(data)
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("run the preview in an interactive terminal")
	}
	home, err := os.MkdirTemp("/tmp", "fn-preview-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	const id = "reconnect-preview"
	directory := filepath.Join(home, ".config/fennec/ipc")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	path := filepath.Join(directory, "terminal-"+id)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for attempt := 0; ; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				decoder, encoder := json.NewDecoder(conn), json.NewEncoder(conn)
				var hello terminalstream.Frame
				if decoder.Decode(&hello) != nil || hello.Type != "connect" || hello.ID != id {
					return
				}
				encoder.Encode(terminalstream.Frame{Type: "receipt", ID: id, Terminal: &cloudruntime.LocalTerminal{ID: id, Agent: "codex", Address: "t1", State: "live"}})
				text := "Fennec reconnect preview\r\n\r\nThe connection will drop in one second\r\nThe screen stays still for three seconds, then retry progress appears below\r\nPress Ctrl+C to leave the preview\r\n"
				if attempt > 0 {
					text = "Connection restored\r\n\r\nThe retry animation has cleared and the original terminal is usable again\r\nThis preview will close in three seconds\r\n"
				}
				if retained != "" {
					text = retained
				}
				encoder.Encode(terminalstream.Frame{Type: "data", ID: id, Data: []byte("\x1b[?1049h\x1b[0m\x1b[2J\x1b[H" + text)})
				for {
					var frame terminalstream.Frame
					if decoder.Decode(&frame) != nil {
						return
					}
					if frame.Type == "credit" {
						break
					}
				}
				duration := time.Second
				if attempt > 0 {
					duration = 3 * time.Second
				}
				timer := time.NewTimer(duration)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				if attempt > 0 {
					encoder.Encode(terminalstream.Frame{Type: "exit", ID: id})
				}
			}()
		}
	}()
	plan := launcher.LaunchPlan{RuntimeID: "01a1b144-56c1-7fc2-a4ab-cf8d84f8543e", SessionID: id, Agent: "codex", Resume: true, StreamPath: path}
	var lostAt time.Time
	recovery := launcher.StreamRecovery{Plan: func(context.Context, launcher.LaunchReceipt) (launcher.LaunchPlan, error) {
		if lostAt.IsZero() {
			lostAt = time.Now()
		}
		if *outage == 0 || time.Since(lostAt) < *outage {
			return launcher.LaunchPlan{}, errors.New("simulated offline connection")
		}
		return plan, nil
	}}
	err = launcher.ExecuteStream(plan, launcher.LaunchIntent{RequestID: id, Agent: "codex"}, home, func() {}, recovery)
	var detached launcher.DetachedError
	if errors.As(err, &detached) {
		fmt.Println("Reconnect preview cancelled")
		return nil
	}
	if err == nil {
		fmt.Println("Reconnect preview complete")
	}
	return err
}
