package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
)

func (runtime *LocalRuntime) Terminal(ctx context.Context, address, id string) (LocalTerminal, error) {
	if !sessionIDPattern.MatchString(id) || !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(address) {
		return LocalTerminal{}, fmt.Errorf("invalid terminal identity")
	}
	raw, err := runtime.Run(ctx, "get", address)
	if err != nil {
		return LocalTerminal{}, err
	}
	var terminal LocalTerminal
	if len(raw) > 2<<20 || json.Unmarshal(raw, &terminal) != nil || terminal.Labels["fennec.session"] != id {
		return LocalTerminal{}, fmt.Errorf("terminal is not the requested Fennec session")
	}
	terminal.ID = id
	if agent := terminal.Labels["fennec.agent"]; agent != "" {
		terminal.Agent = agent
	}
	return terminal, nil
}

func (runtime *LocalRuntime) Attach(ctx context.Context, address, id string, input io.Reader, output, diagnostics io.Writer) (int, error) {
	terminal, err := runtime.Terminal(ctx, address, id)
	if err != nil {
		return 1, err
	}
	run := func(arguments ...string) (int, error) {
		command := exec.CommandContext(ctx, filepath.Join(runtime.Home, ".local/bin/herdr-core"), arguments...)
		command.Stdin = input
		command.Stdout = output
		command.Stderr = diagnostics
		var err error
		if terminalInput, ok := input.(*os.File); ok && term.IsTerminal(int(terminalInput.Fd())) && len(arguments) > 0 && arguments[0] == "attach" {
			command.Stdin = nil
			command.Stdout = nil
			command.Stderr = nil
			err = runAttachment(ctx, command, terminalInput, output)
		} else {
			err = command.Run()
		}
		if exit, ok := err.(*exec.ExitError); ok {
			code := exit.ExitCode()
			if code < 0 {
				code = 1
			}
			return code, nil
		}
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
	if terminal.State != "finished" {
		_, noColor := os.LookupEnv("NO_COLOR")
		styled := false
		if file, ok := output.(*os.File); ok {
			styled = term.IsTerminal(int(file.Fd())) && os.Getenv("TERM") != "dumb" && !noColor
		}
		detachNotice := &detachOutput{
			output:         output,
			address:        address,
			id:             id,
			styled:         styled,
			suppressNotice: os.Getenv("FENNEC_STREAM_MANAGED") == "1",
		}
		originalOutput := output
		output = detachNotice
		code, err := run("attach", address)
		detachNotice.Flush()
		output = originalOutput
		if err != nil || code != 11 {
			return code, err
		}
		terminal, err = runtime.Terminal(ctx, address, id)
		if err != nil {
			return 1, err
		}
	}
	if terminal.State != "finished" || terminal.ExitCode == nil || *terminal.ExitCode < 0 || *terminal.ExitCode > 255 {
		return 1, fmt.Errorf("finished session has no valid exit code")
	}
	code, err := run("read", address, "--lines", "0")
	if err != nil || code != 0 {
		return code, err
	}
	return *terminal.ExitCode, nil
}
