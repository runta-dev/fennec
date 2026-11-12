package record_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/flows"
	"fennec/cua/internal/record"
)

// A draft asks for the captions of many unnamed clicks at once (a few at a
// time), so a long recording stays within the draft's time limit.
func TestDraftCaptionsConcurrent(t *testing.T) {
	const clicks, each = 12, 60 * time.Millisecond
	var events []record.Event
	for i := 0; i < clicks; i++ {
		x, y := float64(10*i), 10.0
		events = append(events, record.Event{Kind: record.KindClick, X: &x, Y: &y, App: "Game", Button: "left", Count: 1, Root: true,
			Element: &record.Element{Role: "document"}, Crop: "iVBORw0KGgo=", CropScale: 1})
	}
	var mu sync.Mutex
	running, most := 0, 0
	caption := func(ctx context.Context, c flows.CaptionRequest) (string, error) {
		mu.Lock()
		running++
		if running > most {
			most = running
		}
		mu.Unlock()
		time.Sleep(each)
		mu.Lock()
		running--
		mu.Unlock()
		return "the tile", nil
	}
	start := time.Now()
	d, err := flows.Draft(context.Background(), &record.Recording{ID: "r", Events: events}, nil, caption)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if most < 2 || most > flows.CaptionsAtOnce {
		t.Errorf("%d captions ran at once, want between 2 and %d", most, flows.CaptionsAtOnce)
	}
	if took >= clicks*each {
		t.Errorf("captions took %v, as long as one after another", took)
	}
	for i, s := range d.Steps[1:] {
		if s.Recorded == nil || s.Recorded.Text != "the tile" {
			t.Errorf("click %s: %+v", strconv.Itoa(i), s.Recorded)
		}
	}
}
