// run-macos-check launches an explicitly requested acceptance command in its
// own launchd job, independent of the Fennec daemon being restarted or replaced.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	worker := flag.Bool("worker", false, "internal launchd worker")
	output := flag.String("output", "", "new absolute directory for the job, log and result")
	flag.Parse()
	args := flag.Args()
	if !filepath.IsAbs(*output) || len(args) == 0 || !filepath.IsAbs(args[0]) {
		return fmt.Errorf("usage: run-macos-check -output /absolute/new-directory -- /absolute/command [args...]")
	}
	if *worker {
		return execute(*output, args)
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("independent launchd checks require macOS")
	}
	// Reusing a directory could confuse a previous result with a new run.
	if err := os.Mkdir(*output, 0700); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	label := "com.runta.fennec.acceptance." + strconv.FormatInt(time.Now().UnixNano(), 36)
	log := filepath.Join(*output, "output.log")
	arguments := append([]string{binary, "-worker", "-output", *output, "--"}, args...)
	plist := filepath.Join(*output, "job.plist")
	if err := os.WriteFile(plist, jobPlist(label, arguments, log), 0600); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", domain, plist)
	command.Stdout, command.Stderr = os.Stderr, os.Stderr
	if err := command.Run(); err != nil {
		// An observation failure does not prove bootstrap failed. Preserve the
		// unique label and files so callers can inspect instead of relaunching.
		return fmt.Errorf("inspect launchd job %s/%s and %s before retrying: %w", domain, label, plist, err)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{
		"job": domain + "/" + label, "log": log,
		"result": filepath.Join(*output, "result.json"), "plist": plist,
	})
}

func jobPlist(label string, args []string, log string) []byte {
	var out bytes.Buffer
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>`)
	stringValue := func(value string) {
		out.WriteString("<string>")
		xml.EscapeText(&out, []byte(value))
		out.WriteString("</string>")
	}
	out.WriteString("<key>Label</key>")
	stringValue(label)
	out.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range args {
		stringValue(arg)
	}
	out.WriteString("</array><key>RunAtLoad</key><true/><key>ProcessType</key><string>Interactive</string><key>StandardOutPath</key>")
	stringValue(log)
	out.WriteString("<key>StandardErrorPath</key>")
	stringValue(log)
	out.WriteString("</dict></plist>")
	return out.Bytes()
}

func execute(directory string, args []string) error {
	started := time.Now()
	command := exec.Command(args[0], args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := command.Run()
	code := 0
	if err != nil {
		code = 1
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		}
	}
	data, encodeErr := json.Marshal(map[string]any{
		"exit_code": code, "started": started.UTC(), "finished": time.Now().UTC(),
	})
	if encodeErr != nil {
		return encodeErr
	}
	temp := filepath.Join(directory, "result.json.next")
	if writeErr := os.WriteFile(temp, data, 0600); writeErr != nil {
		return writeErr
	}
	if renameErr := os.Rename(temp, filepath.Join(directory, "result.json")); renameErr != nil {
		return renameErr
	}
	return err
}
