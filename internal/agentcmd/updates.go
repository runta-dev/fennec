package agentcmd

// Fennec pins every cloud agent's version and upgrades it together with the
// Fennec release. An agent's own update check can therefore only announce a
// version Fennec will not install, or replace the pinned executable behind
// Fennec's back, so cloud sessions turn those checks off.

// NoUpdateEnvironment returns the variables that disable an agent's update
// check, notice and self-update.
func NoUpdateEnvironment(id string) []string {
	switch id {
	case "claude":
		return []string{"DISABLE_AUTOUPDATER=1", "DISABLE_UPDATES=1"}
	case "kimi":
		return []string{"KIMI_CODE_NO_AUTO_UPDATE=1"}
	case "opencode":
		return []string{"OPENCODE_DISABLE_AUTOUPDATE=1"}
	case "grok":
		return []string{"GROK_DISABLE_AUTOUPDATER=1"}
	case "pi":
		return []string{"PI_SKIP_VERSION_CHECK=1"}
	}
	return nil
}

// NoUpdateArguments returns the leading options for agents that read the
// setting only from configuration or the command line, not the environment.
func NoUpdateArguments(id string) []string {
	switch id {
	case "codex":
		return []string{"-c", "check_for_update_on_startup=false"}
	case "cursor":
		return []string{"--disable-auto-update"}
	}
	return nil
}
