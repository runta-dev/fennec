package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fennec/internal/channel/discord"
)

// The commands below are how the project's agent finishes connecting a
// channel (FEN-228): the person pastes only the bot token into Settings ›
// Channels, which files a request; the agent then reads what the bot sees,
// asks which forum or channels to use, saves that, and starts the intake.
// None of them print a secret.

const agentUsage = "usage: channel discord status|places|enable-intent <id> | channel discord set <id> [forums=<ids>] [channels=<ids>] [guilds=<ids>] | channel service status|restart"

func discordAdapter(home, id string) (ChannelSpec, *discord.Adapter, error) {
	cfg, err := LoadConfig(home)
	if err != nil {
		return ChannelSpec{}, nil, err
	}
	spec, ok := cfg.Spec(id)
	if !ok || spec.Kind != KindDiscord || spec.Discord == nil {
		return spec, nil, fmt.Errorf("no discord channel %q; the person saves the bot token in Settings › Channels first", id)
	}
	c := spec.Discord.config()
	c.APIBase = DiscordAPIBase
	a, err := discord.New(c)
	return spec, a, err
}

func discordCommand(ctx context.Context, home string, args []string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New(agentUsage)
	}
	verb, id := args[0], args[1]
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	switch verb {
	case "status":
		_, a, err := discordAdapter(home, id)
		if err != nil {
			return err
		}
		st, err := a.Check(ctx)
		report := map[string]any{"status": st, "ok": err == nil}
		var bad *discord.CheckError
		if errors.As(err, &bad) {
			report["problem"] = bad.Code + " " + bad.Detail
		} else if err != nil {
			return err
		}
		return enc.Encode(report)
	case "places":
		_, a, err := discordAdapter(home, id)
		if err != nil {
			return err
		}
		places, err := a.Places(ctx)
		if err != nil {
			return err
		}
		return enc.Encode(places)
	case "enable-intent":
		_, a, err := discordAdapter(home, id)
		if err != nil {
			return err
		}
		return a.EnableMessageContent(ctx)
	case "set":
		spec, _, err := discordAdapter(home, id)
		if err != nil {
			return err
		}
		d := DiscordSetup{Forums: spec.Discord.Forums, Channels: spec.Discord.Channels, Guilds: spec.Discord.Guilds}
		for _, kv := range args[2:] {
			k, v, ok := strings.Cut(kv, "=")
			list := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
			switch {
			case ok && k == "forums":
				d.Forums = list
			case ok && k == "channels":
				d.Channels = list
			case ok && k == "guilds":
				d.Guilds = list
			default:
				return errors.New(agentUsage)
			}
		}
		result, err := Setup(ctx, home, SetupRequest{Project: spec.Project, ID: id, Kind: KindDiscord, Discord: &d})
		if err != nil {
			return err
		}
		if err = enc.Encode(result); err != nil || !result.OK {
			return errors.Join(err, fmt.Errorf("not saved: %s %s", result.Code, result.Detail))
		}
		// The Gateway reads its settings at start.
		if running(ctx) {
			return Service(ctx, home, "restart", out)
		}
		return nil
	}
	return errors.New(agentUsage)
}

const serviceName = "fennec-channels.service"

func systemctl(ctx context.Context, args ...string) *exec.Cmd {
	if os.Geteuid() != 0 {
		args = append([]string{"--user"}, args...)
	}
	return exec.CommandContext(ctx, "systemctl", args...)
}

func running(ctx context.Context) bool {
	return systemctl(ctx, "is-active", "--quiet", serviceName).Run() == nil
}

// Service runs the intake (`channel serve`) as a systemd unit that survives
// reboots and restarts on failure, or reports how it is doing.
func Service(ctx context.Context, home, verb string, out io.Writer) error {
	switch verb {
	case "restart":
		if _, err := LoadConfig(home); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		dir := "/etc/systemd/system"
		if os.Geteuid() != 0 {
			dir = filepath.Join(home, ".config/systemd/user")
		}
		bin := filepath.Join(home, ".local/fennec/bin/fennec-terminal")
		unit := fmt.Sprintf("[Unit]\nDescription=Fennec channel intake (Discord, web widget)\nAfter=network-online.target\n[Service]\nType=simple\nEnvironment=HOME=%s\nExecStart=%s channel serve\nRestart=always\nRestartSec=5\n[Install]\nWantedBy=default.target\n", strconv.Quote(home), strconv.Quote(bin))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, serviceName), []byte(unit), 0o644); err != nil {
			return err
		}
		for _, step := range [][]string{{"daemon-reload"}, {"enable", serviceName}, {"restart", serviceName}} {
			if b, err := systemctl(ctx, step...).CombinedOutput(); err != nil {
				return fmt.Errorf("systemctl %s: %v: %s", strings.Join(step, " "), err, b)
			}
		}
		time.Sleep(4 * time.Second)
		fallthrough
	case "status":
		state, _ := systemctl(ctx, "is-active", serviceName).Output()
		args := []string{"-u", serviceName, "-n", "15", "--no-pager", "-o", "cat"}
		if os.Geteuid() != 0 {
			args = append([]string{"--user"}, args...)
		}
		logs, _ := exec.CommandContext(ctx, "journalctl", args...).Output()
		_, err := fmt.Fprintf(out, "%s: %s\n%s", serviceName, strings.TrimSpace(string(state)), logs)
		return err
	}
	return errors.New(agentUsage)
}
