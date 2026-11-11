package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fennec/internal/agent"
	"fennec/internal/daemon"
	"fennec/internal/launcher"
	"fennec/internal/localssh"
	"fennec/internal/project"
	"fennec/internal/runta"
	"fennec/internal/setup"
	"path/filepath"
)

func Run(arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "--help" {
		fmt.Fprintln(output, "Fennec by Runta\n\nUsage:\n  fennec serve [-addr 127.0.0.1:7420]  Open the local control surface\n  fennec agents                         Discover installed CLI agents\n  fennec prepare-runtime                Prepare shared runtime (token JSON on stdin)\n  fennec connector                      Start local SSH approval service\n\nCloud launches run through Herdr; local SSH execution requires Fennec approval by default.")
		return nil
	}
	switch arguments[0] {
	case "describe-control":
		return describeControl(output)
	case "daemon":
		return daemonCommand(arguments[1:], output)
	case "control-daemon":
		return serveControl(arguments[1:])
	case "control":
		var input io.Reader
		if !isTerminal(os.Stdin) {
			input = os.Stdin
		}
		return callControl(arguments[1:], input, output)
	case "fen":
		return fenCommand(arguments[1:], output)
	case "install-entry":
		if len(arguments) != 3 {
			return errors.New("install-entry requires agent and absolute entry path")
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		return launcher.InstallEntry(arguments[2], self, arguments[1])
	case "restore-entry":
		if len(arguments) != 2 {
			return errors.New("restore-entry requires absolute entry path")
		}
		return launcher.RestoreEntry(arguments[1])
	case "dispatch-entry":
		if len(arguments) < 3 || !launcher.Supported[arguments[1]] || !filepath.IsAbs(arguments[2]) {
			return errors.New("invalid entry dispatch")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		enabled, err := launcher.EnabledAgents(filepath.Join(home, ".config/fennec/takeover.json"))
		if err != nil {
			return err
		}
		if enabled[arguments[1]] && !launcher.LocalQuery(arguments[3:]) {
			return runAgentOriginal(append([]string{arguments[1]}, arguments[3:]...), arguments[2])
		}
		return syscall.Exec(arguments[2], append([]string{arguments[2]}, arguments[3:]...), os.Environ())
	case "shell-init":
		if len(arguments) != 2 {
			return errors.New("shell-init requires sh, bash, zsh or fish")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		script, err := launcher.ShellInit(arguments[1], filepath.Join(home, ".local/share/fennec/shims"))
		if err != nil {
			return err
		}
		_, err = io.WriteString(output, script)
		return err
	case "install-shims":
		if len(arguments) != 1 {
			return errors.New("install-shims takes no arguments")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		directory := filepath.Join(home, ".local/share/fennec/shims")
		if err = launcher.InstallShims(directory, self); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]string{"directory": directory})
	case "dispatch-agent":
		if len(arguments) < 2 || !launcher.Supported[arguments[1]] {
			return errors.New("supported agent required")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		enabled, err := launcher.EnabledAgents(filepath.Join(home, ".config/fennec/takeover.json"))
		if err != nil {
			return err
		}
		if enabled[arguments[1]] && !launcher.LocalQuery(arguments[2:]) {
			return runAgent(arguments[1:])
		}
		return launcher.ExecLocal(arguments[1], arguments[2:], filepath.Join(home, ".local/share/fennec/shims"))
	case "run-agent":
		return runAgent(arguments[1:])
	case "launch-broker":
		if len(arguments) != 1 {
			return errors.New("launch-broker takes private configuration on stdin")
		}
		return launchBroker(os.Stdin, output)
	case "reconnect":
		if len(arguments) != 2 {
			return errors.New("reconnect requires a Fennec session ID")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		receipt, err := launcher.ReadReceipt(filepath.Join(home, ".config/fennec/launches"), arguments[1])
		if err != nil {
			return err
		}
		return reconnectResident(context.Background(), receipt, home, func() {})
	case "attach":
		if len(arguments) != 4 {
			return errors.New("attach requires runtime ID, terminal address and session ID")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return reconnectResident(context.Background(), launcher.LaunchReceipt{RuntimeID: arguments[1], Address: arguments[2], SessionID: arguments[3]}, home, func() {})
	case "cloud-agent":
		return cloudAgent(arguments[1:])
	case "launch-project":
		if len(arguments) != 1 {
			return errors.New("launch-project takes JSON on stdin")
		}
		return launchProject(os.Stdin, output)
	case "sync":
		return syncCommand(arguments[1:], output)
	case "sync-project":
		if len(arguments) != 1 {
			return errors.New("sync-project takes JSON on stdin")
		}
		return syncProject(os.Stdin, output)
	case "project-manifest":
		if len(arguments) > 2 {
			return errors.New("project-manifest accepts one project directory")
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if len(arguments) == 2 {
			cwd = arguments[1]
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		manifest, err := project.ManifestGit(ctx, cwd)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(manifest)
	case "attach-session":
		if len(arguments) != 4 {
			return errors.New("attach-session requires runtime ID, Herdr address and session ID")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return reconnectResident(context.Background(), launcher.LaunchReceipt{RuntimeID: arguments[1], Address: arguments[2], SessionID: arguments[3]}, home, func() {})
	case "local-agent":
		if len(arguments) < 2 {
			return errors.New("local-agent requires a supported agent name")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		return launcher.ExecLocal(arguments[1], arguments[2:], filepath.Join(home, ".local", "share", "fennec", "shims"))
	case "send-session":
		if len(arguments) != 1 {
			return errors.New("send-session takes JSON on stdin")
		}
		return sessionControlAlias(os.Stdin, output, "send")
	case "read-session":
		if len(arguments) != 1 {
			return errors.New("read-session takes JSON on stdin")
		}
		return sessionControlAlias(os.Stdin, output, "read")
	case "stop-session":
		if len(arguments) != 1 {
			return errors.New("stop-session takes JSON on stdin")
		}
		return stopSessionShared(os.Stdin, output)
	case "list-sessions":
		if len(arguments) != 1 {
			return errors.New("list-sessions takes JSON on stdin")
		}
		return sessionControlAlias(os.Stdin, output, "list")
	case "launch-session":
		if len(arguments) != 1 {
			return errors.New("launch-session takes JSON on stdin")
		}
		return sessionControlAlias(os.Stdin, output, "launch")
	case "link-runtime":
		if len(arguments) != 1 {
			return errors.New("link-runtime takes no arguments")
		}
		var input struct {
			Token string `json:"token"`
			localssh.LinkConfig
		}
		decoder := json.NewDecoder(os.Stdin)
		if err := decoder.Decode(&input); err != nil {
			return err
		}
		if input.Token == "" {
			return errors.New("token required")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		input.Directory = filepath.Join(home, ".config", "fennec", "ssh")
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		go func() { io.Copy(io.Discard, decoder.Buffered()); io.Copy(io.Discard, os.Stdin); cancel() }()
		client := &runta.Client{Endpoint: "https://api.runta.com", Token: input.Token}
		return localssh.Link(ctx, client, input.LinkConfig, func(stage, detail string) {
			json.NewEncoder(output).Encode(map[string]string{"type": "connector_status", "stage": stage, "detail": detail})
		})
	case "select-model":
		if len(arguments) != 1 {
			return errors.New("select-model takes JSON on stdin")
		}
		var input struct {
			Token string `json:"token"`
			Agent string `json:"agent"`
			Model string `json:"model"`
		}
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&input); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client := &runta.Client{Endpoint: "https://api.runta.com", Token: input.Token}
		user, err := client.CurrentUser(ctx)
		if err != nil {
			return err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if err = setup.SaveModelPreference(filepath.Join(home, ".config", "fennec"), user, input.Agent, input.Model); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]string{"status": "saved", "agent": input.Agent})
	case "agent-versions":
		return json.NewEncoder(output).Encode(agent.DetectVersions(context.Background()))
	case "prepare-runtime":
		// Credentials arrive only over the parent-owned pipe, never argv.
		var input struct {
			Token string `json:"token"`
		}
		if len(arguments) != 1 {
			return errors.New("prepare-runtime takes no arguments")
		}
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&input); err != nil {
			return err
		}
		if input.Token == "" {
			return errors.New("token required")
		}
		lifecycle, stopLifecycle := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stopLifecycle()
		ctx, cancel := context.WithTimeout(lifecycle, 12*time.Minute)
		defer cancel()
		return prepareRuntime(ctx, input.Token, output)
	case "connector":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		flags := flag.NewFlagSet("connector", flag.ContinueOnError)
		requireApproval := flags.Bool("require-approval", true, "require one-shot command approval")
		directory := flags.String("state-dir", filepath.Join(home, ".config", "fennec", "ssh"), "private connector state directory")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected connector arguments")
		}
		return localssh.RunWithApproval(context.Background(), *directory, os.Stdin, output, *requireApproval)
	case "agents":
		if len(arguments) != 1 {
			return errors.New("agents takes no arguments")
		}
		return json.NewEncoder(output).Encode(agent.Discover())
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		flags.SetOutput(output)
		address := flags.String("addr", "127.0.0.1:7420", "loopback listen address")
		if err := flags.Parse(arguments[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 || !daemon.Loopback(*address) {
			return errors.New("serve requires a loopback IP address and no positional arguments")
		}
		server := &http.Server{Addr: *address, Handler: daemon.Handler(*address), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 15 * time.Second}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		}()
		fmt.Fprintf(output, "Fennec development preview: http://%s\nRead-only preview; manage cloud sessions with fen or the macOS app.\n", *address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported command %q; run fennec help", arguments[0])
	}
}
