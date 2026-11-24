package cli

import (
	"fennec/internal/audit"
	"fennec/internal/runta"
)

// This is the settings projection shared by state.get and settings.get/set.
func desktopSettingsResponse(home string, prefs desktopPreferences, summary audit.Summary) map[string]any {
	env := runta.ActiveEnvironment(home)
	policy := effectiveSandboxPolicy(home, prefs)
	roots := prefs.SandboxRoots
	if roots == nil {
		roots = []string{}
	}
	return map[string]any{
		"revision": prefs.Revision, "magpie": prefs.Magpie, "magpie_off_agents": magpieOffAgents(prefs), "no_auto_update": prefs.NoAutoUpdate, "update_skip": prefs.UpdateSkip,
		"environment": env.Name, "api_endpoint": env.API, "dashboard_url": env.Dashboard, "ssh_host": env.SSHHost,
		"ssh_nodelay": !prefs.NoSSHNoDelay, "no_ssh_nodelay": prefs.NoSSHNoDelay,
		"permission_mode": effectivePermissionPreset(prefs), "sandbox_mode": policy.Mode, "sandbox_network": policy.Network,
		"sandbox_roots": roots, "effective_sandbox_roots": policy.WritableRoots, "audit_summary": summary,
		"theme": prefs.Theme, "language": prefs.Language, "initialized": prefs.Initialized,
		"get_started_done": prefs.GetStartedDone, "get_started_hidden": prefs.GetStartedHidden, "fennec_terminal": prefs.FennecTerminal,
	}
}

func magpieOffAgents(prefs desktopPreferences) []string {
	if prefs.MagpieOffAgents == nil {
		return []string{}
	}
	return prefs.MagpieOffAgents
}
