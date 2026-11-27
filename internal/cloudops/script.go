package cloudops

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

func operationScript(operation Operation) (string, error) {
	if err := operation.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(operation)
	if err != nil {
		return "", err
	}
	return "exec \"$HOME/.local/fennec/bin/fennec-terminal\" operation " + base64.StdEncoding.EncodeToString(data) + "\n", nil
}
func ReadScript(address, sessionID string) (string, error) {
	return operationScript(Operation{Kind: "read", Address: address, SessionID: sessionID})
}

func SendScript(address, sessionID, text string) (string, error) {
	return operationScript(Operation{Kind: "send", Address: address, SessionID: sessionID, Text: text})
}

// CloudAgentsScript prints, in one round trip, the device key name of the Mac
// connected to this cloud computer ("device\t<name>", empty when none is, and
// then Launch can't start anything), then "agents\t" and the cloud_agents
// operation's JSON (empty when this cloud computer's fennec-terminal predates
// that operation).
func CloudAgentsScript() string {
	script, _ := operationScript(Operation{Kind: "cloud_agents"})
	return `printf 'device\t%s\n' "$(ls -t "$HOME/.ssh/fennec/" 2>/dev/null | sed -n 's/^\([a-f0-9]\{8,64\}\)\.conf$/\1/p' | head -1)"` + "\n" +
		"printf 'agents\\t'; " + strings.TrimPrefix(strings.TrimSpace(script), "exec ") + " 2>/dev/null | tr -d '\\n'; echo\n"
}
