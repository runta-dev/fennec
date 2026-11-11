package cli

import (
	"context"
	"errors"
	"fennec/internal/cmuxhooks"
	"fennec/internal/sessionid"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"fennec/internal/launcher"
	"fennec/internal/launchprofile"
)

// run-agent keeps stdin attached to the terminal and obtains only an attach
// receipt from the private broker. The CLI never receives the user's API token.
func runAgent(arguments []string) error { return runAgentOriginal(arguments, "") }
func runAgentOriginal(arguments []string, original string) error {
	if len(arguments) == 0 || !launcher.Supported[arguments[0]] {
		return fmt.Errorf("run-agent requires a supported agent")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	directory, args, err := launcher.ResolveLaunchDirectory(arguments[0], cwd, arguments[1:])
	if err != nil {
		return err
	}
	id, err := sessionid.Allocate(home)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if arguments[0] == "codex" {
		args, _, err = cmuxhooks.Rewrite(args, home, id)
		if err != nil {
			return err
		}
	}
	if original == "" {
		original, err = launcher.ResolveOriginal(arguments[0], os.Getenv("PATH"), filepath.Join(home, ".local/share/fennec/shims"), self)
	}
	if err != nil {
		return err
	}
	intent := launcher.LaunchIntent{RequestID: id, Agent: arguments[0], Directory: directory, Arguments: args, Prepare: true, Cold: os.Getenv("FENNEC_COLD_START") == "1"}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	socket := filepath.Join(home, ".config/fennec/ipc/launch.sock")
	loading := startLaunchLoading(os.Stderr, isTerminal(os.Stderr), os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == "" && os.Getenv("FENNEC_DEBUG") == "")
	defer loading.Clear()
	if err := brokerReadiness(ctx, intent.Agent); err != nil {
		loading.Fail()
		return err
	}
	var connection *launcher.ColdConnection
	if intent.Cold {
		connection = launcher.StartColdConnection(context.Background(), home, intent)
	}
	defer connection.Close()
	finished := launchprofile.Begin("broker.request")
	plan, err := brokerPlanRequest(ctx, socket, intent)
	finished()
	if err == nil {
		if intent.Cold && !connection.Ready(ctx, plan) {
			connection.Close()
			connection = nil
		}
		if intent.Cold && connection != nil {
			err = launcher.ExecuteColdStream(ctx, plan, intent, home, loading.Clear, connection)
		} else if plan.StreamPath != "" {
			err = launcher.ExecuteStream(plan, intent, home, loading.Clear)
		} else {
			err = launcher.ExecutePlan(plan, intent, home, loading.Clear, connection)
		}
	}
	if err != nil {
		var lost launcher.StreamLostError
		if errors.As(err, &lost) {
			return reconnectResident(ctx, lost.Receipt, home, loading.Clear)
		}
		loading.Fail()
	} else if intent.Agent == "codex" && len(intent.Arguments) == 0 && isTerminal(os.Stdout) {
		loading.Clear()
		fmt.Fprintf(os.Stderr, "\nTo resume: fen resume %s\n", intent.RequestID)
	}
	return err
}

var brokerLaunchRequest = launcher.RequestLaunch
var brokerPlanRequest = launcher.RequestPlan
var brokerReadiness = waitForAgentBroker

func runAgentWith(ctx context.Context, socket string, intent launcher.LaunchIntent, request func(context.Context, string, launcher.LaunchIntent) (launcher.LaunchReceipt, error), attach func(launcher.LaunchReceipt) error) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	requestDone := launchprofile.Begin("broker.request")
	receipt, err := request(ctx, socket, intent)
	requestDone()
	if err != nil {
		return fmt.Errorf("cloud launch request %s: %w; inspect this request before retrying", intent.RequestID, err)
	}
	if receipt.SessionID != intent.RequestID || receipt.Agent != intent.Agent {
		return fmt.Errorf("cloud launch receipt identity mismatch")
	}
	if err = attach(receipt); err != nil {
		return fmt.Errorf("session %s exists; reconnect instead of relaunching: %w", receipt.SessionID, err)
	}
	return nil
}
