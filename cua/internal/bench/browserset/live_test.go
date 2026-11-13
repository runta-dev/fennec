//go:build chromiumlive

package browserset

import (
	"context"
	"io/fs"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	browsersite "fennec/cua/bench/browser"
	"fennec/cua/internal/chromium"
)

// Every task end to end on the host, against the pinned Chromium: headless,
// a temporary home and profile, the mock keychain, pages on 127.0.0.1 only.
// The setup's files are made, the untouched state scores 0, the scripted
// solution runs and the solved state scores 100. Notes is the in-memory
// stand-in; the AppleScript snapshot path runs in the VM (cua-bench check).
// Run with
// FENNEC_CUA_CHROMIUM=/path/to/Chromium.app go test -tags chromiumlive -run TestLive ./internal/bench/browserset
func TestLiveSolveEveryTask(t *testing.T) {
	app := os.Getenv(chromium.EnvApp)
	if app == "" {
		t.Skip("set FENNEC_CUA_CHROMIUM to the pinned Chromium.app")
	}
	defs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	site, err := fs.Sub(browsersite.FS, "site")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		t.Run(d.ID, func(t *testing.T) {
			env := testEnv(t)
			env.App = app
			if err := ResetState(env, d); err != nil {
				t.Fatal(err)
			}
			l, err := OpenLog(env.LogDir)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			h, err := NewServer(site, l)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(h)
			defer srv.Close()
			env.Base = srv.URL

			// The start page as the setup's Chromium would load it.
			if resp, err := srv.Client().Get(srv.URL + d.Start); err != nil {
				t.Fatal(err)
			} else {
				resp.Body.Close()
			}
			if _, err := TakeSnapshot(env); err != nil {
				t.Fatal(err)
			}
			if r := grade(t, env, d); r.Pass {
				t.Fatal("passes untouched")
			} else {
				t.Logf("untouched: %s", r)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := Solve(ctx, env, d, true, t.Logf); err != nil {
				t.Fatal(err)
			}
			r := grade(t, env, d)
			if !r.Pass {
				t.Fatalf("solved state: %s", r)
			}
		})
	}
}
