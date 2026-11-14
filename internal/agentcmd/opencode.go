package agentcmd

import "strings"

func OpenCodeValueOption(arg string) bool {
	switch arg {
	case "--model", "-m", "--session", "-s", "--prompt", "--agent", "--log-level", "--server", "--port", "--hostname", "--password", "--username", "--format", "--title", "--file", "-f", "--variant":
		return true
	}
	return false
}

// OpenCodeRootArgument finds the first root positional argument without
// mistaking a prompt or model option's value for a directory or subcommand.
func OpenCodeRootArgument(args []string) (int, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return -1, false
		}
		if OpenCodeValueOption(arg) {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "run", "mini", "attach", "auth", "providers", "models", "acp", "mcp", "plugin", "session", "stats", "debug", "upgrade", "update", "uninstall", "serve", "service", "api", "reload", "pair", "web", "export", "import", "github", "pr", "agent", "completion":
			return i, true
		}
		return i, false
	}
	return -1, false
}

func OpenCodeV2Arguments(args []string) []string {
	i, subcommand := OpenCodeRootArgument(args)
	if subcommand {
		if args[i] != "run" && args[i] != "mini" {
			return append([]string{}, args...)
		}
		// Version 2 requires command flags after the subcommand.
		out := append([]string{}, args[:i+1]...)
		out = append(out, "--auto", "--standalone")
		return append(out, args[i+1:]...)
	}
	return append([]string{"--auto", "--standalone"}, args...)
}
