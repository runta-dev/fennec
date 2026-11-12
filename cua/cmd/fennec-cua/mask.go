package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/draw"
	_ "image/jpeg" // captures may come as JPEG
	"image/png"
	"reflect"

	"fennec/cua/internal/ax"
	"fennec/cua/internal/engine"
	"fennec/cua/internal/policy"
	"fennec/cua/internal/protocol"
	"fennec/cua/internal/view"
	"fennec/cua/internal/vision"
)

// maskedObserve answers an observe that asks for pixels. Under an app
// allowlist, screen and region captures show only admitted apps: every
// other pixel is painted black, by the window server's list (which also
// holds floating panels and accessory apps' windows that accessibility
// does not list), taken before and after the capture and required to
// match. Vision screenshots, which see and click the whole screen, are
// refused.
func maskedObserve(ctx context.Context, s *vision.Service, allow policy.Allowlist, agent string, req protocol.ObserveRequest, base engine.Observer) (*protocol.ObserveResult, error) {
	if allow.Empty() {
		return s.Observe(ctx, agent, req, base)
	}
	if req.Vision {
		return nil, &protocol.Error{Code: protocol.ErrNotAllowed, Message: "vision screenshots see the whole screen, which the app allowlist does not admit; nothing was captured", Hint: "observe a window of an admitted app with image: window"}
	}
	if req.Image != vision.ImageScreen && req.Image != vision.ImageRegion {
		return s.Observe(ctx, agent, req, base)
	}
	for try := 0; try < policy.CaptureTries; try++ {
		before := serverApps()
		res, err := s.Observe(ctx, agent, req, base)
		if err != nil || res == nil || res.Image == nil {
			return res, err
		}
		if !reflect.DeepEqual(before, serverApps()) {
			continue
		}
		if err := maskImage(res.Image, allow, before); err != nil {
			return nil, &protocol.Error{Code: protocol.ErrInternal, Message: "the capture could not be masked; nothing is shown: " + err.Error()}
		}
		return res, nil
	}
	return nil, &protocol.Error{Code: protocol.ErrBusy, Message: policy.ErrUnstable.Error()}
}

// serverApps lists the windows on screen front to back as the blackout
// reads them: owner app and desktop bounds.
func serverApps() []view.RawWindow {
	ws, err := ax.ServerWindows(true)
	if err != nil {
		return nil
	}
	scale := ax.BackingScale()
	var out []view.RawWindow
	for _, w := range ws {
		if w.Layer < 0 || w.Layer >= ax.OverlayLayer || w.Alpha <= 0 {
			continue
		}
		out = append(out, view.RawWindow{Key: ax.WindowKey(w.ID), PID: w.PID, App: w.Owner, Title: w.Title, Bounds: ax.ToDesk(w.Bounds, scale)})
	}
	return out
}

// maskImage blacks out an image's pixels outside admitted windows.
func maskImage(im *protocol.Image, allow policy.Allowlist, ws []view.RawWindow) error {
	data, err := base64.StdEncoding.DecodeString(im.Data)
	if err != nil {
		return err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	rgba := image.NewRGBA(src.Bounds())
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	allow.Blackout(policy.Capture{Image: rgba, Origin: im.Origin, Scale: im.Scale}, ws)
	var out bytes.Buffer
	if err := png.Encode(&out, rgba); err != nil {
		return err
	}
	im.MIME, im.Data = "image/png", base64.StdEncoding.EncodeToString(out.Bytes())
	return nil
}
