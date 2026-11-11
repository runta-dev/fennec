// check-control-contract rejects packaging a newer native client with an older
// Go control executable. It runs no services and reads no credentials.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: check-control-contract <app> <Swift source>...")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(os.Args[1], "Contents/MacOS/fen"), "describe-control")
	raw, err := command.Output()
	if err != nil {
		return fmt.Errorf("cannot inspect packaged control API: %w", err)
	}
	var schema struct {
		Version          int      `json:"version"`
		TerminalProtocol int      `json:"terminal_protocol"`
		Methods          []string `json:"methods"`
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Version != 1 {
		return fmt.Errorf("packaged control protocol is incompatible")
	}
	protocol, err := os.ReadFile(filepath.Join(os.Args[1], "Contents/Resources/terminal-protocol-version"))
	if err != nil {
		return err
	}
	expected, err := strconv.Atoi(strings.TrimSpace(string(protocol)))
	if err != nil || expected != schema.TerminalProtocol {
		return fmt.Errorf("packaged daemon and remote terminal protocols differ; rebuild Go from the current source")
	}
	supported := map[string]bool{}
	for _, method := range schema.Methods {
		supported[method] = true
	}
	pattern := regexp.MustCompile(`\b(?:call|mutate)\("([a-z][a-z0-9.-]+)"`)
	missing := map[string]bool{}
	for _, path := range os.Args[2:] {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range pattern.FindAllSubmatch(data, -1) {
			method := string(match[1])
			if !supported[method] {
				missing[method] = true
			}
		}
	}
	if len(missing) > 0 {
		names := []string{}
		for method := range missing {
			names = append(names, method)
		}
		sort.Strings(names)
		return fmt.Errorf("native UI requires missing daemon methods %v; rebuild Go from the same source snapshot", names)
	}
	return nil
}
