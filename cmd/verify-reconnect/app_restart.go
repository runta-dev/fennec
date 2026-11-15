package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func exactProcess(installed string, worker bool) (int, error) {
	pattern := "^" + regexp.QuoteMeta(filepath.Join(filepath.Dir(installed), "Fennec")) + "( |$)"
	if worker {
		pattern = "^" + regexp.QuoteMeta(installed) + " transport-worker "
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/pgrep", "-f", pattern).Output()
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(output))
	if len(fields) != 1 {
		return 0, fmt.Errorf("expected exactly one process for %s, found %d", pattern, len(fields))
	}
	return strconv.Atoi(fields[0])
}

func upstreamIdentity(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:ESTABLISHED", "-nP", "-Fn").Output()
	if err != nil {
		return "", err
	}
	var connections []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "n") {
			connections = append(connections, line)
		}
	}
	if len(connections) != 1 {
		return "", fmt.Errorf("worker must own exactly one established upstream, found %d", len(connections))
	}
	sort.Strings(connections)
	return strings.Join(connections, "\n"), nil
}
