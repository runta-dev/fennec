package runtime

import (
	"encoding/base64"
	"encoding/json"
)

func operationScript(operation LocalOperation) (string, error) {
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
	return operationScript(LocalOperation{Kind: "read", Address: address, SessionID: sessionID})
}
