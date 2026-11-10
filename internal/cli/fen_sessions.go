package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/launcher"
	"fennec/internal/launchprofile"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
)

type fenSession struct {
	Receipt launcher.LaunchReceipt
	Title   string
	Status  string
}

var fenID = regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`)
var fenTitleSuffix = regexp.MustCompile(`(?i)\s*[|·]\s*(?:[a-f0-9]{24,64}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}|[a-f0-9][a-f0-9-]{7,63}(?:\.{3}|…))\s*$`)
var fenTechnicalTitle = regexp.MustCompile(`(?i)^(?:[a-f0-9]{24,64}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}|[a-f0-9][a-f0-9-]{7,63}(?:\.{3}|…))$`)

func fenTitle(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return -1
		}
		return character
	}, value)
	value = strings.TrimSpace(fenTitleSuffix.ReplaceAllString(strings.TrimSpace(value), ""))
	if fenTechnicalTitle.MatchString(value) {
		return ""
	}
	return value
}

func fenParseSessions(data []byte, runtimeID string) ([]fenSession, error) {
	var listing struct {
		Terminals []struct {
			Address string            `json:"address"`
			Title   string            `json:"title"`
			Name    string            `json:"name"`
			State   string            `json:"state"`
			Agent   string            `json:"agent"`
			Status  json.RawMessage   `json:"status"`
			Labels  map[string]string `json:"labels"`
		} `json:"terminals"`
	}
	if len(data) > 4<<20 || json.Unmarshal(data, &listing) != nil {
		return nil, fmt.Errorf("invalid cloud session list")
	}
	sessions := []fenSession{}
	for _, terminal := range listing.Terminals {
		id := terminal.Labels["fennec.session"]
		if terminal.State != "live" || !fenID.MatchString(id) {
			continue
		}
		agent := terminal.Labels["fennec.agent"]
		if !launcher.Supported[agent] {
			agent = strings.TrimSuffix(terminal.Agent, ".js")
		}
		if !launcher.Supported[agent] {
			continue
		}
		if _, err := launcher.AttachArgs(runtimeID, terminal.Address, id, "/validation"); err != nil {
			return nil, err
		}
		title := fenTitle(terminal.Title)
		if title == "" {
			title = fenTitle(terminal.Name)
		}
		if title == "" {
			title = "Untitled session"
		}
		var status string
		if json.Unmarshal(terminal.Status, &status) != nil {
			var object struct {
				State string `json:"state"`
			}
			json.Unmarshal(terminal.Status, &object)
			status = object.State
		}
		label := map[string]string{"working": "Working", "blocked": "Needs input", "error": "Errored"}[status]
		if label == "" {
			label = "Idle"
		}
		sessions = append(sessions, fenSession{launcher.LaunchReceipt{RuntimeID: runtimeID, Address: terminal.Address, SessionID: id, Agent: agent}, title, label})
	}
	return sessions, nil
}

func fenCommand(arguments []string, output io.Writer) error {
	if len(arguments) > 0 && arguments[0] == "start" {
		if len(arguments) != 1 {
			return fmt.Errorf("usage: fen start")
		}
		return daemonCommand([]string{"start"}, output)
	}
	if len(arguments) > 0 && arguments[0] == "daemon" {
		return daemonCommand(arguments[1:], output)
	}
	if len(arguments) > 0 && arguments[0] == "control" {
		var input io.Reader
		if !isTerminal(os.Stdin) {
			input = os.Stdin
		}
		return callControl(arguments[1:], input, output)
	}
	if len(arguments) == 1 && (arguments[0] == "--help" || arguments[0] == "-h" || arguments[0] == "help") {
		fmt.Fprint(output, fenControlHelp)
		return nil
	}
	if handled, err := fenControlCommand(arguments, output); handled {
		return err
	}
	if len(arguments) > 2 || len(arguments) > 0 && arguments[0] != "resume" && arguments[0] != "attach" {
		return fmt.Errorf("usage: fen [attach [session-id]]")
	}
	id := ""
	if len(arguments) == 2 {
		id = arguments[1]
		if !fenID.MatchString(id) {
			return fmt.Errorf("invalid session ID")
		}
	}
	if id == "" && (!isTerminal(os.Stdin) || !isTerminal(os.Stdout) || os.Getenv("TERM") == "dumb") {
		return fmt.Errorf("session picker needs an interactive terminal; use fen attach <session-id>")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := waitForBroker(ctx); err != nil {
		return err
	}
	load := func() ([]fenSession, error) {
		reply, err := fenLocalRequest(home, fenServiceRequest{Operation: "list"})
		return reply.Sessions, err
	}
	kill := func(session fenSession) error {
		_, err := fenLocalRequest(home, fenServiceRequest{Operation: "kill", SessionID: session.Receipt.SessionID})
		return err
	}
	if id == "" {
		sessions, err := load()
		if err != nil {
			return err
		}
		selected, err := fenPick(ctx, sessions, load, kill, func() error { _, err := fenLocalRequest(home, fenServiceRequest{Operation: "refresh"}); return err })
		if err != nil {
			return err
		}
		if selected == nil {
			return nil
		}
		id = selected.Receipt.SessionID
	}
	loading := startLaunchLoading(os.Stderr, isTerminal(os.Stderr), os.Getenv("TERM") != "dumb")
	defer loading.Clear()
	lookupDone := launchprofile.Begin("resume.local_request")
	operation := "attach"
	if len(arguments) > 0 && arguments[0] == "resume" {
		operation = "resume"
	}
	reply, err := fenLocalRequest(home, fenServiceRequest{Operation: operation, SessionID: id})
	lookupDone()
	if err != nil {
		return err
	}
	if reply.Receipt == nil || reply.Receipt.SessionID != id {
		return fmt.Errorf("invalid resume response")
	}
	receipt := *reply.Receipt
	if err := launcher.SaveReceipt(filepath.Join(home, ".config/fennec/launches"), receipt); err != nil {
		return err
	}
	if reply.Plan != nil && reply.Plan.StreamPath != "" {
		err := launcher.ExecuteStream(*reply.Plan, launcher.LaunchIntent{RequestID: id, Agent: receipt.Agent}, home, loading.Clear)
		var lost launcher.StreamLostError
		if errors.As(err, &lost) {
			return reconnectResident(ctx, lost.Receipt, home, loading.Clear)
		}
		return err
	}
	return fmt.Errorf("resident terminal channel unavailable; inspect fen status")
}
