package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fennec/internal/localssh"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
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
	serving, err := serveMode(os.Args[1:])
	if err != nil {
		return 1, err
	}
	if len(os.Args) == 2 && os.Args[1] == "protocol-version" {
		fmt.Fprintln(os.Stdout, terminalstream.ProtocolVersion)
		return 0, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 1, err
	}
	os.Setenv("PATH", filepath.Join(home, ".local/fennec/node/bin")+":"+filepath.Join(home, ".local/fennec/agents/bin")+":"+filepath.Join(home, ".local/bin")+":"+os.Getenv("PATH"))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	runtime := cloudruntime.NewLocalRuntime(home)
	if len(os.Args) == 3 && os.Args[1] == "reconcile-routes" {
		if len(os.Args[2]) > 65536 {
			return 1, fmt.Errorf("oversized route request")
		}
		data, err := base64.StdEncoding.DecodeString(os.Args[2])
		if err != nil {
			return 1, fmt.Errorf("invalid route request")
		}
		var routes []setup.AgentRoute
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&routes) != nil || decoder.Decode(new(any)) != io.EOF {
			return 1, fmt.Errorf("invalid route request")
		}
		results, err := setup.ReconcileLocalRoutes(ctx, home, routes)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(results)
	}
	if len(os.Args) == 2 && os.Args[1] == "sync-model-accounts" {
		result, err := setup.ReceiveModelAccounts(ctx, home, os.Stdin)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(result)
	}
	if len(os.Args) == 2 && os.Args[1] == "restore-model-identity" {
		return 0, setup.RestoreLocalCodexIdentity(ctx, home)
	}
	if len(os.Args) == 3 && os.Args[1] == "reconcile-plugins" {
		if len(os.Args[2]) > 65536 {
			return 1, fmt.Errorf("oversized plugin request")
		}
		data, err := base64.StdEncoding.DecodeString(os.Args[2])
		var plugins []setup.ModelPlugin
		if err != nil || json.Unmarshal(data, &plugins) != nil {
			return 1, fmt.Errorf("invalid plugin request")
		}
		return 0, setup.ReconcileLocalModelPlugins(ctx, home, plugins)
	}
	if len(os.Args) == 2 && os.Args[1] == "register-alias" {
		var request localssh.AliasRegistration
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&request); err != nil {
			return 1, err
		}
		alias, err := localssh.RegisterAlias(home, request)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(alias)
	}
	if len(os.Args) == 2 && os.Args[1] == "migrate-credentials" {
		report, err := setup.ReceiveCredentials(home, os.Stdin)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(report)
	}
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
	if serving {
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
			started := time.Now()
			terminal, err := runtime.Terminal(ctx, reference.Address, reference.ID)
			if err != nil {
				return terminal, nil, err
			}
			terminal.Timings = map[string]float64{"attach_lookup": float64(time.Since(started).Microseconds()) / 1000}
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

func serveMode(arguments []string) (bool, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	if arguments[0] == "serve" {
		if len(arguments) == 1 {
			return true, nil
		}
		if len(arguments) == 2 && arguments[1] == "--protocol="+strconv.Itoa(terminalstream.ProtocolVersion) {
			return true, nil
		}
		return false, fmt.Errorf("terminal protocol incompatible")
	}
	return len(arguments) == 2 && arguments[0] == "serve-initial", nil
}
