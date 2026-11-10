package cli

import (
	"context"
	"fennec/internal/launcher"
	"fmt"
)

// A lost attachment may reconnect to the confirmed session once. It never
// creates a replacement agent or opens an independent SSH connection.
func reconnectResident(ctx context.Context, receipt launcher.LaunchReceipt, home string, ready func()) error {
	if err := waitForBroker(ctx); err != nil {
		return err
	}
	reply, err := fenLocalRequest(home, fenServiceRequest{Operation: "resume", SessionID: receipt.SessionID, RuntimeID: receipt.RuntimeID, Address: receipt.Address})
	if err != nil {
		return err
	}
	if reply.Plan == nil || reply.Plan.StreamPath == "" || reply.Receipt == nil || reply.Receipt.RuntimeID != receipt.RuntimeID || reply.Receipt.SessionID != receipt.SessionID {
		return fmt.Errorf("existing session reattachment not confirmed")
	}
	return launcher.ExecuteStream(*reply.Plan, launcher.LaunchIntent{RequestID: receipt.SessionID, Agent: reply.Receipt.Agent}, home, ready)
}
