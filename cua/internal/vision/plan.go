package vision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ShotFailed prefixes the note on a screenshot that failed after input
// that ran.
const ShotFailed = "the input ran, but the screenshot after it failed"

// PlanHost is what a pixel plan runs on.
type PlanHost interface {
	// Front is the frontmost application window, nil when none is shown.
	Front(ctx context.Context) *Front
	// Send makes one driver call and answers its reply text.
	Send(ctx context.Context, req Request) (string, error)
	// Settle waits for the screen after one step; it returns the capture
	// it held still on, if any.
	Settle(ctx context.Context, step PixelStep) *Capture
	// Shoot takes the screenshot after the plan; settled, when set, is the
	// capture the last settle held still on, which it uses instead of
	// capturing again.
	Shoot(ctx context.Context, settled *Capture) (Screenshot, Shot, error)
}

// PlanOptions shape a plan.
type PlanOptions struct {
	// Scale replaces the pinned screenshot's scale when set.
	Scale float64
	// View pins the plan to the screenshot of that view.
	View string
}

// PlanResult reports a plan.
type PlanResult struct {
	Lines  []string
	Failed bool
	// Shot is the screenshot after the last step that ran, if it worked.
	Shot  *Screenshot
	Map   Shot
	Ran   int
	Error error
}

// Text joins the plan's lines.
func (r PlanResult) Text() string { return strings.Join(r.Lines, "\n") }

// RunPlan runs pixel steps in order and stops at the first failure,
// saying how many steps did not run. Every step maps by the screenshot
// taken before the plan started (or opts), even though the plan's own
// screenshot may use another size; the next plan maps by that newer one.
// Steps settle but produce no image: one screenshot is taken after the
// last step that ran. A screenshot that fails after input ran does not
// make the plan fail; it is reported so the input is not repeated.
func RunPlan(ctx context.Context, sessions *Sessions, session string, host PlanHost, steps []PixelStep, opts PlanOptions) PlanResult {
	var res PlanResult
	shot, err := sessions.Pin(session, opts.View, opts.Scale)
	if err != nil {
		res.Failed, res.Error = true, err
		res.Lines = append(res.Lines, err.Error())
		return res
	}
	mark := func(n int) (Element, error) { return sessions.Mark(session, n) }
	var settled *Capture
	for i, step := range steps {
		req, err := Route(step, shot, host.Front(ctx), mark)
		var text string
		if err == nil {
			text, err = run(ctx, host, step, req, shot)
		}
		if err != nil {
			res.Failed, res.Error = true, err
			res.Lines = append(res.Lines, fmt.Sprintf("%d. %s failed: %s", i+1, step.Action, err.Error()))
			if left := len(steps) - i - 1; left > 0 {
				res.Lines = append(res.Lines, fmt.Sprintf("the remaining %d steps did not run", left))
			}
			break
		}
		res.Ran++
		res.Lines = append(res.Lines, fmt.Sprintf("%d. %s: %s", i+1, step.Action, text))
		settled = host.Settle(ctx, step)
	}
	out, next, err := host.Shoot(ctx, settled)
	if err != nil {
		res.Lines = append(res.Lines, ShotFailed+": "+err.Error())
		return res
	}
	res.Shot, res.Map = &out, next
	return res
}

func run(ctx context.Context, host PlanHost, step PixelStep, req Request, shot Shot) (string, error) {
	if req.Tool == "wait" {
		secs := min(max(step.Seconds, 0.1), 10)
		select {
		case <-time.After(time.Duration(secs * float64(time.Second))):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return fmt.Sprintf("waited %.1fs", secs), nil
	}
	reply, err := host.Send(ctx, req)
	if err != nil {
		return "", err
	}
	if req.Tool == "click" && req.Asked != nil {
		return ClickReply(reply, *req.Asked, shot.Scale), nil
	}
	return ScreenshotPoints(reply, shot.Scale), nil
}
