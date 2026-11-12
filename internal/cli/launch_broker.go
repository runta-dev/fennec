package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/connectivity"
	"fennec/internal/launchprofile"
	"fennec/internal/project"
	"fennec/internal/runta"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fennec/internal/launcher"
)

type brokerConfiguration struct {
	Network           *connectivity.Monitor     `json:"-"`
	PreparedTerminal  *terminalstream.Transport `json:"-"`
	PreparedSSH       *ssh.Client               `json:"-"`
	PreparedRuntimeID string                    `json:"-"`
	Token             string                    `json:"token"`
	RuntimeID         string                    `json:"runtime_id"`
	Device            string                    `json:"device"`
	Socket            string                    `json:"socket"`
	Agents            []string                  `json:"agents"`
	Roots             []string                  `json:"roots"`
	Selected          []string                  `json:"selected,omitempty"`
	AutoProjects      bool                      `json:"auto_projects,omitempty"`
}

// Roots and agent enablement arrive only from the parent-owned pipe. A local
// CLI request cannot expand the synchronization grant or substitute credentials.
func brokerRequest(config brokerConfiguration, intent launcher.LaunchIntent) (launchProjectRequest, error) {
	if err := intent.Validate(); err != nil {
		return launchProjectRequest{}, err
	}
	allowed := false
	for _, id := range config.Agents {
		if id == intent.Agent {
			allowed = true
		}
	}
	if !allowed {
		for _, selected := range config.Selected {
			if selected == intent.Agent {
				return launchProjectRequest{}, fmt.Errorf("cloud takeover is enabled, but this agent's model or runtime is not ready; refresh preparation in Fennec")
			}
		}
		return launchProjectRequest{}, fmt.Errorf("cloud takeover not enabled for this agent")
	}
	cwd, err := filepath.EvalSymlinks(intent.Directory)
	if err != nil {
		return launchProjectRequest{}, err
	}
	cloudOnly := func() (launchProjectRequest, error) {
		return launchProjectRequest{Token: config.Token, RuntimeID: config.RuntimeID, Device: config.Device, SessionID: intent.RequestID, Agent: intent.Agent, Directory: cwd, Arguments: intent.Arguments, Version: intent.Version, CloudWorkspace: true}, nil
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return launchProjectRequest{}, fmt.Errorf("working directory unavailable")
	}
	// Agent enablement controls execution placement, not implicit project upload.
	// Start the remote loop immediately; synchronization is a session operation.
	if config.AutoProjects {
		return cloudOnly()
	}
	// Git expands cwd to the repository root; authorize that whole scope too.
	gitContext, cancelGit := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelGit()
	rootCommand := exec.CommandContext(gitContext, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	rootOutput, err := rootCommand.Output()
	if err != nil {
		if config.AutoProjects {
			return cloudOnly()
		}
		return launchProjectRequest{}, fmt.Errorf("Git project required")
	}
	projectRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return launchProjectRequest{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return launchProjectRequest{}, err
	}
	if err = project.ValidateRoot(projectRoot, home); err != nil {
		if config.AutoProjects {
			return cloudOnly()
		}
		return launchProjectRequest{}, err
	}
	authorized := config.AutoProjects
	for _, root := range config.Roots {
		if err := project.ValidateRoot(root, home); err != nil {
			continue
		}
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(canonical, projectRoot)
		if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			authorized = true
			break
		}
	}
	if !authorized {
		return launchProjectRequest{}, fmt.Errorf("project directory has not been authorized")
	}
	return launchProjectRequest{Token: config.Token, RuntimeID: config.RuntimeID, Device: config.Device, SessionID: intent.RequestID, Agent: intent.Agent, Directory: cwd, Arguments: intent.Arguments, Version: intent.Version}, nil
}
func launchBroker(input io.Reader, output io.Writer) error {
	decoder := json.NewDecoder(input)
	var config brokerConfiguration
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("invalid broker configuration")
	}
	if config.Token == "" || config.RuntimeID == "" || config.Device == "" || !filepath.IsAbs(config.Socket) {
		return fmt.Errorf("authenticated private broker configuration required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	for _, root := range config.Roots {
		if err := project.ValidateRoot(root, home); err != nil {
			return err
		}
		if !filepath.IsAbs(root) {
			return fmt.Errorf("absolute grants required")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { io.Copy(io.Discard, decoder.Buffered()); io.Copy(io.Discard, input); cancel() }()
	return serveLaunchBroker(ctx, home, func() brokerConfiguration { return config }, output)
}

func serveLaunchBroker(ctx context.Context, home string, configuration func() brokerConfiguration, output io.Writer, observers ...func(*terminalChannel)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	config := configuration()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	scope := sha256.Sum256([]byte(config.RuntimeID + "\x00" + config.Device))
	journal := &launcher.LaunchJournal{Directory: filepath.Join(home, ".config/fennec/launch-journal", hex.EncodeToString(scope[:16]))}
	api := runta.NewClient(home, config.Token)
	shared, err := launcher.NewSharedConnection(ctx, filepath.Join(home, ".config/fennec/cloud"), config.RuntimeID, func(operation context.Context) error {
		_, err := api.PrepareOwnedSSH(operation, config.RuntimeID, filepath.Join(home, ".config/fennec/cloud"))
		return err
	})
	if err != nil {
		return err
	}
	if config.Network != nil {
		shared.Online = config.Network.Online
		shared.WaitOnline = config.Network.WaitOnline
		shared.Changes = config.Network.Changes
	}
	channel := &terminalChannel{ctx: ctx, network: config.Network, shared: shared, home: home, runtimeID: config.RuntimeID}
	if config.PreparedRuntimeID == config.RuntimeID {
		channel.preparedSSH = config.PreparedSSH
		if config.PreparedTerminal != nil && config.PreparedTerminal.IsReady() {
			channel.transport = config.PreparedTerminal
			channel.generation = 1
		}
	}
	channelDone := channel.start()
	defer func() { cancel(); <-channelDone; shared.Close() }()
	stopSessions, err := startFenSessionService(ctx, home, config.RuntimeID, shared, channel)
	if err != nil {
		return err
	}
	defer stopSessions()
	for _, observe := range observers {
		observe(channel)
	}
	return launcher.ServeBrokerPrepared(ctx, config.Socket, func(operation context.Context, intent launcher.LaunchIntent) (launcher.LaunchReceipt, error) {
		config := configuration()
		request, err := brokerRequest(config, intent)
		if err != nil {
			return launcher.LaunchReceipt{}, err
		}
		return journal.Do(operation, intent, func(operation context.Context, intent launcher.LaunchIntent) (launcher.LaunchReceipt, error) {
			if request.CloudWorkspace {
				transport, err := channel.get(operation)
				if err != nil {
					return launcher.LaunchReceipt{}, err
				}
				raw, err := transport.Operate(operation, cloudruntime.LocalOperation{Kind: "launch", Launch: &cloudruntime.LocalLaunch{ID: intent.RequestID, Agent: intent.Agent, LocalDirectory: request.Directory, Device: config.Device, Arguments: intent.Arguments, Hooks: intent.Hooks}})
				if err != nil {
					return launcher.LaunchReceipt{}, err
				}
				var terminal cloudruntime.LocalTerminal
				if json.Unmarshal(raw, &terminal) != nil || terminal.ID != intent.RequestID || terminal.Address == "" {
					return launcher.LaunchReceipt{}, fmt.Errorf("invalid launch receipt")
				}
				return launcher.LaunchReceipt{RuntimeID: config.RuntimeID, SessionID: intent.RequestID, Address: terminal.Address, Agent: intent.Agent}, nil
			}
			return launchBrokerProject(operation, executable, config, request, intent, filepath.Dir(config.Socket))
		})
	}, func(operation context.Context, intent launcher.LaunchIntent) (launcher.LaunchPlan, error) {
		defer launchprofile.Begin("plan.prepare")()
		config := configuration()
		request, err := brokerRequest(config, intent)
		if err != nil {
			return launcher.LaunchPlan{}, err
		}
		if !request.CloudWorkspace {
			return launcher.LaunchPlan{}, fmt.Errorf("direct terminal requires cloud workspace")
		}
		native := cloudruntime.LocalLaunch{ID: intent.RequestID, Agent: intent.Agent, LocalDirectory: request.Directory, Device: config.Device, Arguments: intent.Arguments, Hooks: intent.Hooks}
		if err := native.Validate(); err != nil {
			return launcher.LaunchPlan{}, err
		}
		control := ""
		if intent.Cold {
			if _, err := api.PrepareOwnedSSH(operation, config.RuntimeID, filepath.Join(home, ".config/fennec/cloud")); err != nil {
				return launcher.LaunchPlan{}, err
			}
		} else {
			control = shared.ControlPath()
		}
		if err := journal.Reserve(operation, intent); err != nil {
			return launcher.LaunchPlan{}, err
		}
		streamPath := ""
		if !intent.Cold {
			transport, err := channel.get(operation)
			if err != nil {
				return launcher.LaunchPlan{}, err
			}
			streamPath, err = offerTerminal(ctx, transport, filepath.Dir(config.Socket), native)
			if err != nil {
				return launcher.LaunchPlan{}, err
			}
		}
		return launcher.LaunchPlan{RuntimeID: config.RuntimeID, SessionID: intent.RequestID, Agent: intent.Agent, Device: config.Device, Launch: &native, ControlPath: control, StreamPath: streamPath}, nil
	}, func() error {
		if err := launcher.InstallShims(filepath.Join(home, ".local/share/fennec/shims"), executable, agent.ExecutableSearchPath(home)); err != nil {
			return err
		}
		shell := os.Getenv("SHELL")
		if shell == "" && runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		}
		if err := launcher.InstallShellIntegration(home, shell); err != nil {
			return fmt.Errorf("shell integration: %w", err)
		}
		selection := config.Selected
		if selection == nil {
			selection = config.Agents
		}
		if err := launcher.PublishEnabled(filepath.Join(home, ".config/fennec/takeover.json"), selection); err != nil {
			return err
		}
		for _, id := range config.Agents {
			if len(observers) > 0 {
				continue
			} // the control daemon owns entry reconciliation
			entry, err := agent.EntryPath(id)
			coverage := "unavailable"
			if err == nil {
				if err = launcher.InstallEntry(entry, executable, id); err == nil {
					coverage = "entry_and_new_shells"
				} else if original, resolveErr := launcher.OriginalEntry(entry); resolveErr == nil {
					if _, versionErr := agent.VersionAt(ctx, original); versionErr == nil {
						coverage = "new_shells"
					}
				}
			}
			if err := json.NewEncoder(output).Encode(map[string]string{"type": "entry_coverage", "agent": id, "coverage": coverage}); err != nil {
				return err
			}
		}
		return json.NewEncoder(output).Encode(map[string]string{"type": "broker_ready"})
	})
}

var brokerProjectCommand = exec.CommandContext

func launchBrokerProject(ctx context.Context, executable string, config brokerConfiguration, request launchProjectRequest, intent launcher.LaunchIntent, socketDirectory string) (launcher.LaunchReceipt, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return launcher.LaunchReceipt{}, err
	}
	command := brokerProjectCommand(ctx, executable, "launch-project")
	command.Stdin = bytes.NewReader(payload)
	// This child is cancellation-scoped; credentials never appear in argv.
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = "project launch did not confirm success"
		}
		return launcher.LaunchReceipt{}, fmt.Errorf("%s", message)
	}
	var result struct {
		Address string `json:"address"`
		State   string `json:"state"`
	}
	if json.Unmarshal(raw, &result) != nil || result.State != "live" {
		return launcher.LaunchReceipt{}, fmt.Errorf("session not confirmed live")
	}
	receipt := launcher.LaunchReceipt{RuntimeID: config.RuntimeID, SessionID: intent.RequestID, Agent: intent.Agent, Address: result.Address}
	if _, err = launcher.AttachArgs(receipt.RuntimeID, receipt.Address, receipt.SessionID, socketDirectory); err != nil {
		return launcher.LaunchReceipt{}, err
	}
	return receipt, nil
}
