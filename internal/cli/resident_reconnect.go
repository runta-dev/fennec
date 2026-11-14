package cli

import (
	"context"
	"fennec/internal/connectivity"
	"fennec/internal/control"
	"fennec/internal/launcher"
	"fmt"
	"path/filepath"
	"time"
)

// Reconnection only attaches to a pinned receipt through the shared daemon.
func reconnectResident(ctx context.Context, receipt launcher.LaunchReceipt, home string, ready func()) error {
	if _, err := launcher.AttachArgs(receipt.RuntimeID, receipt.Address, receipt.SessionID, filepath.Join(home, ".config/fennec/cloud")); err != nil {
		return err
	}
	plan, err := residentReconnectPlan(ctx, receipt, home)
	if err != nil {
		return err
	}
	return launcher.ExecuteStream(plan, launcher.LaunchIntent{RequestID: receipt.SessionID, Agent: plan.Agent}, home, ready, residentRecovery(home, ready))
}

func residentRecovery(home string, lost func()) launcher.StreamRecovery {
	return launcher.StreamRecovery{
		Lost: lost,
		Plan: func(ctx context.Context, receipt launcher.LaunchReceipt) (launcher.LaunchPlan, error) {
			return residentReconnectPlan(ctx, receipt, home)
		},
		Wake: func(ctx context.Context) <-chan struct{} { return watchResidentRecovery(ctx, home) },
	}
}

func residentReconnectPlan(ctx context.Context, receipt launcher.LaunchReceipt, home string) (launcher.LaunchPlan, error) {
	if err := ensureControl(ctx); err != nil {
		return launcher.LaunchPlan{}, err
	}
	client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
	var state struct {
		Account struct {
			Status   string `json:"status"`
			Revision uint64 `json:"revision"`
		} `json:"account"`
		Runtime struct {
			ID string `json:"runtime_id"`
		} `json:"runtime"`
		Services struct {
			Ready bool `json:"broker_ready"`
		} `json:"services"`
	}
	if err := client.Call(ctx, "state.get", nil, &state); err != nil {
		return launcher.LaunchPlan{}, err
	}
	if state.Account.Status == "signed_out" {
		if state.Account.Revision == 0 {
			return launcher.LaunchPlan{}, fmt.Errorf("account restoration is pending")
		}
		return launcher.LaunchPlan{}, launcher.ReconnectStopped{Message: "Sign in to Fennec before reattaching this session."}
	}
	if state.Runtime.ID != "" && state.Runtime.ID != receipt.RuntimeID {
		return launcher.LaunchPlan{}, launcher.ReconnectStopped{Message: "This session belongs to a different cloud computer."}
	}
	if !state.Services.Ready {
		return launcher.LaunchPlan{}, fmt.Errorf("connection is recovering")
	}
	reply, err := fenLocalRequestContext(ctx, home, fenServiceRequest{Operation: "attach", SessionID: receipt.SessionID, RuntimeID: receipt.RuntimeID, Address: receipt.Address})
	if err != nil {
		return launcher.LaunchPlan{}, err
	}
	if reply.Plan == nil || reply.Plan.StreamPath == "" || reply.Receipt == nil || reply.Receipt.RuntimeID != receipt.RuntimeID || reply.Receipt.SessionID != receipt.SessionID || reply.Receipt.Address != receipt.Address || (receipt.Agent != "" && reply.Receipt.Agent != receipt.Agent) {
		return launcher.LaunchPlan{}, launcher.ReconnectStopped{Message: "The reconnected session identity could not be verified."}
	}
	return *reply.Plan, nil
}

// Read-only local probes wake a sleeping attachment when the daemon's shared
// connection or the local network returns. They never open their own SSH link.
func watchResidentRecovery(ctx context.Context, home string) <-chan struct{} {
	return watchResidentRecoveryEvents(ctx, home, connectivity.WatchChanges(ctx))
}

func watchResidentRecoveryEvents(ctx context.Context, home string, networkEvents <-chan struct{}) <-chan struct{} {
	wake := make(chan struct{}, 1)
	go func() {
		client := control.Client{Socket: filepath.Join(home, ".config/fennec/ipc/control.sock")}
		var previous struct {
			Ready      bool   `json:"ready"`
			Generation uint64 `json:"generation"`
			Started    int64  `json:"connection_started_unix_ms"`
		}
		online := connectivity.ProbeRoutingTable()
		first := true
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			current := previous
			probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			err := client.Call(probe, "connection.status", nil, &current)
			cancel()
			if err != nil {
				current.Ready = false
			}
			nextOnline := connectivity.ProbeRoutingTable()
			if !first && ((current.Ready && (!previous.Ready || current.Generation != previous.Generation || current.Started != previous.Started)) || (!online && nextOnline)) {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			previous, online, first = current, nextOnline, false
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case _, ok := <-networkEvents:
				if !ok {
					networkEvents = nil
					continue
				}
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}()
	return wake
}
