package launcher

import (
	"context"
	"encoding/json"
	"fennec/internal/clipboard"
	"fennec/internal/launchprofile"
	"fennec/internal/predict"
	"fennec/internal/terminalstream"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// predictiveMode reads FEN_PREDICTIVE_ECHO, then settings.json's
// predictive_echo. Unknown values keep the adaptive default.
func predictiveMode(home string) predict.Mode {
	if value := strings.TrimSpace(strings.ToLower(os.Getenv("FEN_PREDICTIVE_ECHO"))); value != "" {
		if mode, ok := predict.ParseMode(value); ok {
			return mode
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".config/fennec/settings.json"))
	if err != nil || len(raw) > 65536 {
		return predict.ModeAdaptive
	}
	var settings struct {
		PredictiveEcho string `json:"predictive_echo"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return predict.ModeAdaptive
	}
	mode, _ := predict.ParseMode(settings.PredictiveEcho)
	return mode
}

// newPredictor returns the engine for an interactive attachment. The engine
// passes output straight through when the agent has no composer profile or
// prediction is off.
func newPredictor(out io.Writer, agent, home string, rows, columns int, interactive bool) *predict.Engine {
	var profile predict.Profile
	mode := predictiveMode(home)
	if interactive && mode != predict.ModeOff {
		profile = predict.ProfileFor(agent)
	}
	var record *predict.Recorder
	if dir := strings.TrimSpace(os.Getenv("FEN_PREDICT_TRACE")); dir != "" && interactive {
		record, _ = predict.NewRecorder(dir, agent)
	}
	return predict.New(out, profile, rows, columns, predict.Options{
		Mode:   mode,
		Record: record,
		ImageReady: func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			return clipboard.HasImage(ctx)
		},
	})
}

// streamFeatures are requested on connect. Cadence is useful for every agent
// with timing-based paste detection; acknowledgements only with prediction.
func streamFeatures(engine *predict.Engine) []string {
	features := []string{terminalstream.CapabilityInputCadence}
	if engine != nil && engine.Active() {
		features = append(features, terminalstream.CapabilityEchoAck)
	}
	return features
}

// inputStamp carries the engine's sequence number and the typing gap for one
// packet of decoded input.
type inputStamp struct {
	seq uint64
	gap time.Duration
}

// stampInput records typed input with the engine and measures the gap since
// the previous packet.
func stampInput(engine *predict.Engine, data []byte, last *time.Time) inputStamp {
	now := time.Now()
	stamp := inputStamp{}
	if !last.IsZero() {
		stamp.gap = now.Sub(*last)
	}
	*last = now
	if engine != nil {
		stamp.seq = engine.Input(data)
	}
	return stamp
}

// framesWithSequence assigns each frame the input sequence number at its end
// and gives the first keystroke frame of a packet its typing gap.
func framesWithSequence(frames []terminalstream.Frame, stamp inputStamp, cadence bool, pasted bool) []terminalstream.Frame {
	offset := stamp.seq
	gapUsed := false
	for i := range frames {
		switch frames[i].Type {
		case "focus":
			offset += 3
		default:
			offset += uint64(len(frames[i].Data))
		}
		frames[i].Seq = offset
		if cadence && !pasted && !gapUsed && frames[i].Type == "input" && stamp.gap > 0 {
			frames[i].DelayMS = int(min(stamp.gap, time.Second) / time.Millisecond)
			gapUsed = true
		}
	}
	return frames
}

func recordPredictionStats(engine *predict.Engine) {
	if engine == nil || !engine.Active() {
		return
	}
	stats := engine.Stats()
	now := time.Now()
	if stats.Predicted == 0 {
		return
	}
	launchprofile.Record("predict.confirm_p50", now.Add(-stats.ConfirmP50), stats.ConfirmP50)
	launchprofile.Record("predict.confirm_p95", now.Add(-stats.ConfirmP95), stats.ConfirmP95)
	launchprofile.Record("predict.local_paint_p50", now.Add(-stats.LocalPaintP50), stats.LocalPaintP50)
	launchprofile.Record("predict.local_paint_p95", now.Add(-stats.LocalPaintP95), stats.LocalPaintP95)
}
