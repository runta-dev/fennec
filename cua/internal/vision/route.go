package vision

import (
	"fmt"
	"math"
	"strings"

	"fennec/cua/internal/protocol"
)

// PixelStep is one input of a vision agent, in pixels of the screenshot it
// saw or by element number.
type PixelStep struct {
	Action    string // click, double_click, right_click, triple_click, drag, scroll, type, key, wait
	X, Y      *float64
	ToX, ToY  *float64
	Element   int // a numbered element; 0 for none
	Text      string
	Key       string
	Button    string
	Direction string
	Amount    int
	Seconds   float64
}

// Front is the frontmost application window, when one is on screen.
type Front struct {
	PID int
	ID  uint32
}

// Request is the driver call a step becomes.
type Request struct {
	Tool string
	Args map[string]any
	// Ref is the element a numbered step presses; Args then hold its
	// centre as a desktop click for an app that refuses the press.
	Ref string
	// Asked is the screenshot point the agent named, for the reply.
	Asked *[2]float64
}

// NoTextTarget refuses typing with no window to receive it.
const NoTextTarget = "no application window is on the screen to type into; open or focus one first"

// Actions a vision step may name.
var pixelActions = map[string]bool{
	"click": true, "double_click": true, "right_click": true, "triple_click": true,
	"drag": true, "scroll": true, "type": true, "key": true, "wait": true,
}

// Route turns a step into its driver call, mapping image points by shot.
// Input at a screenshot point goes to the driver with desktop scope and
// foreground delivery, since it moves the real pointer; keys and typing
// without an element go to the frontmost window through desktop scope,
// without a pid. Typing with no window on screen is refused, a key is
// not. Points off the screen are refused before anything is sent.
func Route(step PixelStep, shot Shot, front *Front, mark func(n int) (Element, error)) (Request, error) {
	if !pixelActions[step.Action] {
		return Request{}, &protocol.Error{Code: protocol.ErrInvalid, Message: fmt.Sprintf("unknown action %q", step.Action), Hint: "use click, double_click, right_click, triple_click, drag, scroll, type, key or wait"}
	}
	desktop := func(args map[string]any) map[string]any {
		args["scope"] = "desktop"
		args["delivery_mode"] = protocol.InputForeground
		return args
	}
	point := func() (*[2]float64, error) {
		if step.X == nil || step.Y == nil {
			return nil, nil
		}
		return &[2]float64{*step.X, *step.Y}, nil
	}
	at, _ := point()
	if step.Action == "drag" {
		if at == nil || step.ToX == nil || step.ToY == nil {
			return Request{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "drag needs x, y, to_x and to_y"}
		}
		to := [2]float64{*step.ToX, *step.ToY}
		if err := OffScreen(shot, step.Action, *at, to); err != nil {
			return Request{}, err
		}
		fx, fy := shot.ToDesktop(at[0], at[1])
		tx, ty := shot.ToDesktop(to[0], to[1])
		return Request{Tool: "drag", Asked: at, Args: desktop(map[string]any{"from_x": round(fx), "from_y": round(fy), "to_x": round(tx), "to_y": round(ty)})}, nil
	}
	if step.Element == 0 && at != nil {
		if err := OffScreen(shot, step.Action, *at); err != nil {
			return Request{}, err
		}
	}
	clicks := strings.HasSuffix(step.Action, "click")
	switch {
	case step.Action == "wait":
		return Request{Tool: "wait"}, nil
	case clicks:
		args := map[string]any{}
		switch step.Action {
		case "double_click":
			args["count"] = 2
		case "triple_click":
			args["count"] = 3
		case "right_click":
			args["button"] = "right"
		}
		if step.Button != "" && step.Action != "right_click" {
			args["button"] = step.Button
		}
		if step.Element != 0 {
			e, err := mark(step.Element)
			if err != nil {
				return Request{}, err
			}
			req := Request{Tool: "click", Ref: e.Ref, Args: args}
			if e.Center != nil {
				args["x"], args["y"] = round(e.Center[0]), round(e.Center[1])
				desktop(args)
			}
			return req, nil
		}
		if at == nil {
			return Request{}, &protocol.Error{Code: protocol.ErrInvalid, Message: step.Action + " needs an element or x and y"}
		}
		x, y := shot.ToDesktop(at[0], at[1])
		args["x"], args["y"] = round(x), round(y)
		return Request{Tool: "click", Asked: at, Args: desktop(args)}, nil
	case step.Action == "scroll":
		if step.Direction == "" {
			return Request{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "scroll needs a direction"}
		}
		amount := step.Amount
		if amount <= 0 {
			amount = 3
		}
		args := map[string]any{"direction": step.Direction, "amount": amount}
		if at != nil {
			x, y := shot.ToDesktop(at[0], at[1])
			args["x"], args["y"] = round(x), round(y)
			return Request{Tool: "scroll", Asked: at, Args: desktop(args)}, nil
		}
		if front == nil {
			return Request{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "no window is on the screen to scroll", Hint: "give x and y"}
		}
		args["pid"], args["window_id"] = front.PID, front.ID
		return Request{Tool: "scroll", Args: args}, nil
	case step.Action == "type":
		if front == nil && step.Element == 0 && at == nil {
			return Request{}, &protocol.Error{Code: protocol.ErrTargetNotFound, Message: NoTextTarget}
		}
		return Request{Tool: "type_text", Args: map[string]any{"text": step.Text, "scope": "desktop"}}, nil
	}
	// key
	if step.Key == "" {
		return Request{}, &protocol.Error{Code: protocol.ErrInvalid, Message: "key needs a key"}
	}
	return Request{Tool: "press_key", Args: map[string]any{"key": step.Key, "scope": "desktop"}}, nil
}

// round keeps desktop coordinates whole, as the driver posts them.
func round(v float64) float64 { return math.Round(v) }
