package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

func RequestPlan(ctx context.Context, path string, intent LaunchIntent) (LaunchPlan, error) {
	if err := intent.Validate(); err != nil {
		return LaunchPlan{}, err
	}
	if !intent.Prepare {
		return LaunchPlan{}, fmt.Errorf("preparation intent required")
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return LaunchPlan{}, fmt.Errorf("Fennec launch broker unavailable")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(intent); err != nil {
		return LaunchPlan{}, err
	}
	var reply BrokerReply
	if err := json.NewDecoder(io.LimitReader(conn, 256*1024)).Decode(&reply); err != nil {
		return LaunchPlan{}, fmt.Errorf("launch authorization outcome unknown; inspect request %s", intent.RequestID)
	}
	if reply.Error != "" {
		return LaunchPlan{}, fmt.Errorf("%s", reply.Error)
	}
	if reply.Plan == nil || reply.Plan.SessionID != intent.RequestID || reply.Plan.Agent != intent.Agent || (reply.Plan.Script == "" && reply.Plan.Launch == nil) {
		return LaunchPlan{}, fmt.Errorf("invalid launch plan")
	}
	return *reply.Plan, nil
}
