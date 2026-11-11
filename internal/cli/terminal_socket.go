package cli

import (
	"context"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/terminalstream"
)

func offerTerminal(ctx context.Context, transport *terminalstream.Transport, directory string, request cloudruntime.LocalLaunch) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	return offerTerminalFrame(ctx, transport, directory, terminalstream.Frame{Type: "launch", ID: request.ID, Launch: &request})
}
func offerTerminalFrame(ctx context.Context, transport *terminalstream.Transport, directory string, opening terminalstream.Frame) (string, error) {
	return terminalstream.OfferLocal(ctx, transport, directory, opening)
}
