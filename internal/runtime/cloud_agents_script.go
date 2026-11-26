package runtime

import "strings"

// CloudAgentsScript prints, in one round trip, the device key name of the Mac
// connected to this cloud computer ("device\t<name>", empty when none is, and
// then Launch can't start anything), then "agents\t" and the cloud_agents
// operation's JSON (empty when this cloud computer's fennec-terminal predates
// that operation).
func CloudAgentsScript() string {
	script, _ := operationScript(LocalOperation{Kind: "cloud_agents"})
	return `printf 'device\t%s\n' "$(ls -t "$HOME/.ssh/fennec/" 2>/dev/null | sed -n 's/^\([a-f0-9]\{8,64\}\)\.conf$/\1/p' | head -1)"` + "\n" +
		"printf 'agents\\t'; " + strings.TrimPrefix(strings.TrimSpace(script), "exec ") + " 2>/dev/null | tr -d '\\n'; echo\n"
}
