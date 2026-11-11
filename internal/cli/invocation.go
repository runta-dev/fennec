package cli

// The bundled fen executable is the public client by default. Implementation
// entrypoints remain private modes used by launchd, shims and build tooling.
func PublicInvocation(arguments []string) bool {
	if len(arguments) == 0 {
		return true
	}
	switch arguments[0] {
	case "uninstall-worker":
		return false
	case "fen", "control-daemon", "describe-control", "run-agent", "dispatch-agent", "dispatch-entry", "install-entry", "restore-entry", "install-shims", "shell-init", "cloud-agent", "launch-project", "sync-project", "project-manifest", "attach-session", "local-agent", "read-session", "send-session", "stop-session", "list-sessions", "launch-session", "link-runtime", "select-model", "agent-versions", "prepare-runtime", "connector", "serve", "sync", "reconnect":
		return false
	default:
		return true
	}
}
