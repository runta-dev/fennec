package policy

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"strconv"
	"strings"
	"testing"

	"fennec/cua/internal/sched"
)

func TestOwnerOnlyControls(t *testing.T) {
	if err := OwnerOnly(501, 501); err != nil {
		t.Fatalf("the owner was refused: %v", err)
	}
	for _, peer := range []uint32{0, 502, 20} {
		err := OwnerOnly(peer, 501)
		if err == nil {
			t.Fatalf("uid %d passed as the owner 501", peer)
		}
		for _, w := range []string{"stop", "resume", "recording", "uid 501", "uid " + strconv.Itoa(int(peer))} {
			if !strings.Contains(err.Error(), w) {
				t.Fatalf("%q does not say %q", err, w)
			}
		}
	}
	if strings.Join(Controls, ",") != "stop,resume,record" {
		t.Fatal(Controls)
	}
}

func TestIndicatorIcons(t *testing.T) {
	seen := map[string]string{}
	for _, name := range Icons {
		for _, scale := range []int{1, 2} {
			b, err := IconPNG(name, scale)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatalf("%s@%dx is no PNG: %v", name, scale, err)
			}
			size := img.Bounds().Size()
			if size.X != IconPoints*scale || size.Y != IconPoints*scale {
				t.Fatalf("%s@%dx is %v, not %d points square", name, scale, size, IconPoints)
			}
			// A template image: black, its shape in alpha alone.
			inked := 0
			for y := 0; y < size.Y; y++ {
				for x := 0; x < size.X; x++ {
					r, g, bl, a := img.At(x, y).RGBA()
					if r != 0 || g != 0 || bl != 0 {
						t.Fatalf("%s@%dx has a coloured pixel at (%d, %d)", name, scale, x, y)
					}
					if a > 0x8000 {
						inked++
					}
				}
			}
			if inked == 0 || inked == size.X*size.Y {
				t.Fatalf("%s@%dx shows no shape (%d of %d pixels)", name, scale, inked, size.X*size.Y)
			}
			if scale == 2 {
				if other, ok := seen[string(b)]; ok {
					t.Fatalf("%s looks like %s", name, other)
				}
				seen[string(b)] = name
			}
		}
	}
	// Each state has its own icon.
	in := &Indicator{}
	icons := map[string]string{}
	icons[StateIdle] = in.View().Icon
	end := in.Acting()
	icons[StateActing] = in.View().Icon
	end()
	in.Recording(true)
	icons[StateRecording] = in.View().Icon
	in.Recording(false)
	in.Stopped(true)
	icons[StateStopped] = in.View().Icon
	distinct := map[string]bool{}
	for state, icon := range icons {
		if _, err := IconPNG(icon, 2); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		distinct[icon] = true
	}
	if len(distinct) != 4 {
		t.Fatalf("states share icons: %v", icons)
	}
	if _, err := IconPNG("nope", 1); err == nil {
		t.Fatal("an unknown icon rendered")
	}
}

func TestIndicatorStates(t *testing.T) {
	var drawn []IndicatorView
	in := &Indicator{Version: "1.2.3", Input: "background", Draw: func(v IndicatorView) { drawn = append(drawn, v) }}
	in.Stopped(false) // the first draw
	if len(drawn) != 1 || drawn[0].State != StateIdle || drawn[0].Icon != IconIdle {
		t.Fatalf("%+v", drawn)
	}
	for _, v := range []IndicatorView{drawn[0]} {
		if !strings.Contains(v.Tooltip, "1.2.3") || !strings.Contains(v.Tooltip, "background input") {
			t.Fatalf("tooltip %q", v.Tooltip)
		}
		if !strings.Contains(v.Menu[0], "1.2.3") || !strings.Contains(v.Menu[0], "background input") {
			t.Fatalf("menu %q", v.Menu)
		}
	}
	// Outlined when idle, filled while acting; overlapping calls are one
	// state and draw once.
	end1 := in.Acting()
	end2 := in.Acting()
	if len(drawn) != 2 || drawn[1].Icon != IconActing || drawn[1].State != StateActing {
		t.Fatalf("%+v", drawn)
	}
	end1()
	end1()
	if len(drawn) != 2 {
		t.Fatalf("redrew without a change: %+v", drawn[2:])
	}
	end2()
	if len(drawn) != 3 || drawn[2].Icon != IconIdle {
		t.Fatalf("%+v", drawn)
	}
	// Marked while recording, acting or not.
	in.Recording(true)
	end := in.Acting()
	if got := in.View(); got.Icon != IconActingRecording || !got.Recording {
		t.Fatalf("%+v", got)
	}
	end()
	if got := in.View(); got.Icon != IconRecording || !strings.Contains(got.Tooltip, "recording") {
		t.Fatalf("%+v", got)
	}
	// Struck through when stopped, which offers resume.
	n := len(drawn)
	in.Stopped(true)
	in.Stopped(true)
	if len(drawn) != n+1 {
		t.Fatalf("%d draws for one stop", len(drawn)-n)
	}
	got := drawn[len(drawn)-1]
	if got.State != StateStopped || got.Icon != IconStopped || got.Menu[len(got.Menu)-1] != "Resume" {
		t.Fatalf("%+v", got)
	}
	in.Stopped(false)
	if got := in.View(); got.State == StateStopped || got.Menu[len(got.Menu)-1] != "Stop" {
		t.Fatalf("%+v", got)
	}
}

func TestStopOrder(t *testing.T) {
	stopper := &sched.Stopper{}
	in := &Indicator{}
	var order []string
	script, _, done, err := stopper.Guard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	halt := &Halt{Indicator: in, Recording: func() {
		if script.Err() == nil {
			order = append(order, "recording before the script")
			return
		}
		order = append(order, "script", "recording")
	}}
	stopper.OnState(halt.OnState)
	stopper.Stop()
	if strings.Join(order, ",") != "script,recording" {
		t.Fatalf("stop order %v", order)
	}
	if !errors.Is(context.Cause(script), sched.ErrStopped) || in.View().State != StateStopped {
		t.Fatalf("%v %+v", context.Cause(script), in.View())
	}
	// A resume ends no recording.
	order = nil
	stopper.Resume()
	if len(order) != 0 || in.View().State != StateIdle {
		t.Fatalf("%v %+v", order, in.View())
	}
}
