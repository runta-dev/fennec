package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

type prefixedInput struct {
	io.Reader
	io.Closer
}

func run() (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 1, err
	}
	os.Setenv("PATH", filepath.Join(home, ".local/fennec/node/bin")+":"+filepath.Join(home, ".local/fennec/agents/bin")+":"+filepath.Join(home, ".local/bin")+":"+os.Getenv("PATH"))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	runtime := cloudruntime.NewLocalRuntime(home)
	if len(os.Args) == 2 && os.Args[1] == "list" {
		result, err := runtime.List(ctx)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(result)
	}
	if len(os.Args) == 3 && os.Args[1] == "operation" {
		data, err := base64.StdEncoding.DecodeString(os.Args[2])
		if err != nil || len(data) > 65536 {
			return 1, fmt.Errorf("invalid operation")
		}
		var request cloudruntime.LocalOperation
		if json.Unmarshal(data, &request) != nil {
			return 1, fmt.Errorf("invalid operation")
		}
		result, err := runtime.Operation(ctx, request)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(result)
	}
	if len(os.Args) == 4 && os.Args[1] == "stop" {
		terminal, err := runtime.Stop(ctx, os.Args[2], os.Args[3])
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(terminal)
	}
	if len(os.Args) == 3 && os.Args[1] == "start" {
		data, err := base64.StdEncoding.DecodeString(os.Args[2])
		if err != nil || len(data) > 65536 {
			return 1, fmt.Errorf("invalid launch payload")
		}
		var request cloudruntime.LocalLaunch
		if err := json.Unmarshal(data, &request); err != nil {
			return 1, err
		}
		terminal, err := runtime.Launch(ctx, request)
		if err != nil {
			return 1, err
		}
		receipt, err := json.Marshal(terminal)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(os.Stdout, "\x1eFENNEC_RECEIPT %s\n", receipt)
		return runtime.Attach(ctx, terminal.Address, request.ID, os.Stdin, os.Stdout, os.Stderr)
	}
	if (len(os.Args) == 2 && os.Args[1] == "serve") || (len(os.Args) == 3 && os.Args[1] == "serve-initial") {
		var input io.Reader = os.Stdin
		if os.Args[1] == "serve-initial" {
			data, err := base64.StdEncoding.DecodeString(os.Args[2])
			if err != nil || len(data) > 262144 {
				return 1, fmt.Errorf("invalid initial terminal frame")
			}
			var frame terminalstream.Frame
			if json.Unmarshal(data, &frame) != nil || frame.Type != "launch" || frame.Launch == nil || frame.ID != frame.Launch.ID || frame.Launch.Validate() != nil {
				return 1, fmt.Errorf("invalid initial launch")
			}
			input = prefixedInput{Reader: io.MultiReader(bytes.NewReader(append(data, '\n')), os.Stdin), Closer: os.Stdin}
		}
		executable, err := os.Executable()
		if err != nil {
			return 1, err
		}
		return 0, terminalstream.ServeWithOperations(ctx, input, os.Stdout, func(ctx context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			terminal, err := runtime.Launch(ctx, request)
			if err != nil {
				return terminal, nil, err
			}
			return terminal, exec.Command(executable, "attach", terminal.Address, request.ID), nil
		}, func(ctx context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			terminal, err := runtime.Terminal(ctx, reference.Address, reference.ID)
			if err != nil {
				return terminal, nil, err
			}
			return terminal, exec.Command(executable, "attach", terminal.Address, terminal.ID), nil
		}, func(ctx context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, error) {
			return runtime.Stop(ctx, reference.Address, reference.ID)
		}, runtime.Operation)
	}
	if len(os.Args) == 4 && os.Args[1] == "attach" {
		return runtime.Attach(ctx, os.Args[2], os.Args[3], os.Stdin, os.Stdout, os.Stderr)
	}
	if len(os.Args) != 2 || os.Args[1] != "launch" {
		return 1, fmt.Errorf("expected launch or attach")
	}
	var request cloudruntime.LocalLaunch
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 65536))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return 1, err
	}
	terminal, err := runtime.Launch(ctx, request)
	if err != nil {
		return 1, err
	}
	return 0, json.NewEncoder(os.Stdout).Encode(terminal)
}
