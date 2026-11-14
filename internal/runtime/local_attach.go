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

// SessionReferenceError is only returned after an authoritative identity check.
// A failed transport or an unavailable local cache is not evidence of deletion.
type SessionReferenceError struct{ Code, Message string }

func (e SessionReferenceError) Error() string { return e.Message }

func (runtime *LocalRuntime) Terminal(ctx context.Context, address, id string) (LocalTerminal, error) {
	if !sessionIDPattern.MatchString(id) || !regexp.MustCompile(`^t[0-9a-z]+$`).MatchString(address) {
		return LocalTerminal{}, fmt.Errorf("invalid terminal identity")
	}
	raw, err := runtime.Run(ctx, "get", address)
	if err != nil {
		if runtime.Query != nil {
			listing, listErr := runtime.List(ctx)
			var rows struct {
				Terminals []LocalTerminal `json:"terminals"`
			}
			if listErr == nil && json.Unmarshal(listing, &rows) == nil {
				found := false
				for _, row := range rows.Terminals {
					if row.Address == address {
						found = true
						break
					}
				}
				if !found {
					return LocalTerminal{}, SessionReferenceError{Code: "session_unavailable", Message: "This cloud session is no longer available."}
				}
			}
		}
		return LocalTerminal{}, err
	}
	var terminal LocalTerminal
	if len(raw) > 2<<20 || json.Unmarshal(raw, &terminal) != nil {
		return LocalTerminal{}, fmt.Errorf("terminal is not the requested Fennec session")
	}
	if terminal.Labels["fennec.session"] != id {
		return LocalTerminal{}, SessionReferenceError{Code: "session_identity_mismatch", Message: "The cloud session identity could not be verified."}
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
