package runtime

func SendScript(address, sessionID, text string) (string, error) {
	return operationScript(LocalOperation{Kind: "send", Address: address, SessionID: sessionID, Text: text})
}
