package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fennec/internal/clipboardbridge"
	"fennec/internal/hookbridge"
	"fennec/internal/localssh"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/setup"
	"fennec/internal/terminalstream"
	"fmt"
	"io"
	"net"
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
	if name := filepath.Base(os.Args[0]); name == "wl-paste" || name == "xclip" {
		home, err := os.UserHomeDir()
		if err != nil {
			return 1, err
		}
		args := os.Args[1:]
		if name == "xclip" {
			args = clipboardbridge.XclipArgs(args)
		}
		if err := clipboardbridge.Read(home, os.Getenv(clipboardbridge.SessionEnv), args, os.Stdout); err != nil {
			return 1, nil
		}
		return 0, nil
	}
	// ~/.local/bin/fennec-escalate links here; agents run it on the cloud computer.
	if filepath.Base(os.Args[0]) == "fennec-escalate" {
		return localssh.RunCloudEscalate(os.Args[1:])
	}
	serving, err := serveMode(os.Args[1:])
	if err != nil {
		return 1, err
	}
	if len(os.Args) == 2 && os.Args[1] == "protocol-version" {
		fmt.Fprintln(os.Stdout, terminalstream.ProtocolVersion)
		return 0, nil
	}
	// Upgrades arrive as a patch against this binary; the installer verifies
	// the result's SHA-256 before using it.
	if len(os.Args) == 3 && os.Args[1] == "apply-patch" {
		return 0, setup.ApplyTerminalPatch(os.Args[2], os.Stdin, os.Stdout)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 1, err
	}
	// ProxyCommand of the Mac device alias; see localssh.ServeMacRelay. It
	// serves a ControlMaster that outlives the terminal and the client that
	// started it, so only the end of its stdio or SIGTERM stops it.
	if len(os.Args) == 4 && os.Args[1] == localssh.MacRelayCommand {
		signal.Ignore(syscall.SIGHUP, syscall.SIGINT, syscall.SIGPIPE)
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		return 0, localssh.ServeMacRelay(ctx, home, os.Args[2], os.Args[3], os.Stdin, os.Stdout)
	}
	if len(os.Args) == 4 && os.Args[1] == "hook" {
		response := hookbridge.Invoke(context.Background(), home, os.Args[2], os.Args[3], os.Stdin)
		binding, _ := hookbridge.Lookup(home, os.Args[2], os.Args[3])
		if _, err := os.Stdout.Write(response.Stdout); err != nil {
			return 2, err
		}
		if _, err := os.Stderr.Write(response.Stderr); err != nil {
			return 2, err
		}
		return hookbridge.CodexExit(binding, response), nil
	}
	os.Setenv("PATH", filepath.Join(home, ".local/fennec/node/bin")+":"+filepath.Join(home, ".local/fennec/agents/bin")+":"+filepath.Join(home, ".local/bin")+":"+os.Getenv("PATH"))
	if id := os.Getenv(clipboardbridge.SessionEnv); id != "" {
		bin, err := clipboardbridge.Bin(home, id)
		if err != nil {
			return 1, err
		}
		os.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	runtime := cloudruntime.NewLocalRuntime(home)
	if len(os.Args) == 2 && os.Args[1] == "refresh-magpie" {
		return 0, setup.RefreshMagpieProcess(ctx, home)
	}
	if len(os.Args) == 3 && os.Args[1] == "verify-gateway-model" {
		return 0, setup.VerifyGatewayModel(ctx, os.Args[2])
	}
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
	if len(os.Args) == 6 && os.Args[1] == "import-session" && os.Args[2] == "--check" {
		return 0, json.NewEncoder(os.Stdout).Encode(map[string]bool{"present": cloudruntime.HasCloudSession(home, os.Args[3], os.Args[4], os.Args[5])})
	}
	if len(os.Args) == 2 && os.Args[1] == "import-session" {
		written, err := cloudruntime.ReceiveSessionImport(home, os.Stdin)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(map[string]int{"written": written})
	}
	if len(os.Args) == 2 && os.Args[1] == "sync-model-accounts" {
		result, err := setup.ReceiveModelAccounts(ctx, home, os.Stdin)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(result)
	}
	if len(os.Args) >= 3 && os.Args[1] == "native-agent" {
		return 1, setup.ExecNativeAgent(home, os.Args[2], os.Args[3:])
	}
	if len(os.Args) == 4 && os.Args[1] == "native-login" {
		return 0, setup.RunNativeLogin(ctx, home, setup.NativeLogin{Agent: os.Args[2], ID: os.Args[3]}, os.Stdin, os.Stdout, os.Stderr)
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
		// Agents request Mac escalation with this command; pairing does not depend on it.
		if err := localssh.InstallEscalateEntry(home); err != nil {
			fmt.Fprintln(os.Stderr, "fennec-escalate entry unavailable:", err)
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
		cloudruntime.StartMetricsSampler(ctx)
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
		return 0, terminalstream.ServeWithSockets(ctx, input, os.Stdout, func(ctx context.Context, request cloudruntime.LocalLaunch) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			terminal, err := runtime.Launch(ctx, request)
			if err != nil {
				return terminal, nil, err
			}
			cmd := exec.Command(executable, "attach", terminal.Address, request.ID)
			cmd.Env = append(os.Environ(), "FENNEC_STREAM_MANAGED=1")
			return terminal, cmd, nil
		}, func(ctx context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, *exec.Cmd, error) {
			started := time.Now()
			terminal, err := runtime.Terminal(ctx, reference.Address, reference.ID)
			if err != nil {
				return terminal, nil, err
			}
			terminal.Timings = map[string]float64{"attach_lookup": float64(time.Since(started).Microseconds()) / 1000}
			cmd := exec.Command(executable, "attach", terminal.Address, terminal.ID)
			cmd.Env = append(os.Environ(), "FENNEC_STREAM_MANAGED=1")
			return terminal, cmd, nil
		}, func(ctx context.Context, reference cloudruntime.LocalTerminal) (cloudruntime.LocalTerminal, error) {
			return runtime.Stop(ctx, reference.Address, reference.ID)
		}, runtime.Operation, func(ctx context.Context, target string) (net.Conn, error) {
			if target == terminalstream.SocketSessionImport {
				return cloudruntime.DialSessionImport(ctx, home), nil
			}
			if target != terminalstream.SocketCodexAppServer {
				return nil, fmt.Errorf("unsupported socket target")
			}
			conn, err := cloudruntime.DialCodexAppServer(ctx, home)
			if err != nil {
				return nil, err
			}
			return cloudruntime.NewChatGPTPathFilter(conn, home, terminalstream.SocketDevice(ctx)), nil
		})
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
