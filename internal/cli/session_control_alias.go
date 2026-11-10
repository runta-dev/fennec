package cli

import (
	"bytes"
	"encoding/json"
	"fennec/internal/controlplane"
	"io"
)

// Compatibility commands project public parameters into the daemon API. Legacy
// token fields are never used to create a second connection or authority source.
func sessionControlAlias(input io.Reader, output io.Writer, operation string) error {
	var request sessionRequest
	if err := json.NewDecoder(io.LimitReader(input, 65536)).Decode(&request); err != nil {
		return err
	}
	params := controlplane.SessionParams{SessionID: request.SessionID, Agent: request.Agent, Directory: request.Directory, Address: request.Address, Text: request.Text, Arguments: request.Arguments, ExpectedRuntimeID: request.RuntimeID}
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return callControl([]string{"sessions." + operation}, bytes.NewReader(data), output)
}
