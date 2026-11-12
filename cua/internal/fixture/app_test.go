//go:build fixture

// This test builds and launches the real CUAFixture app, so it opens windows
// (and, for the spaces scene, full-screen Spaces) on the desktop it runs on.
// Run it in the test VM only:
//
//	go test -tags fixture -run TestFixtureApp -v ./internal/fixture/
package fixture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func buildApp(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "testapp", "build.sh")
	out, err := exec.Command("bash", script, t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("build.sh: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	app := lines[len(lines)-1]
	if !strings.HasSuffix(app, "CUAFixture.app") {
		t.Fatalf("build.sh did not print the bundle path:\n%s", out)
	}
	return app
}

func TestFixtureApp(t *testing.T) {
	app := buildApp(t)
	dir, err := os.MkdirTemp("", "cfx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a, err := Launch(ctx, app, filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	}()

	scenes, err := a.Scenes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenes) < 15 {
		t.Fatalf("only %d scenes: %v", len(scenes), scenes)
	}
	for _, s := range scenes {
		t.Run(s, func(t *testing.T) {
			sctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			if err := a.Reset(sctx, s); err != nil {
				t.Fatal(err)
			}
			st, err := a.RawState(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if st["scene"] != s {
				t.Fatalf("state scene = %v", st["scene"])
			}
			for _, k := range []string{"windows", "focus", "menu", "commands", "presses", "edits", "controls", "events", "facts", "launch", VolatileKey} {
				if _, ok := st[k]; !ok {
					t.Errorf("state has no %q", k)
				}
			}
			wins, _ := st["windows"].([]any)
			if s == "empty" {
				if len(wins) != 0 {
					t.Errorf("empty scene shows %d windows", len(wins))
				}
			} else if len(wins) == 0 {
				t.Error("scene shows no windows")
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		var fe *Error
		err := a.Reset(ctx, "no-such-scene")
		if err == nil || !asError(err, &fe) {
			t.Fatalf("err = %v", err)
		}
		if _, err := a.Call(ctx, map[string]any{"op": "bogus"}); !asError(err, &fe) {
			t.Fatalf("unknown op err = %v", err)
		}
	})

	t.Run("freeze", func(t *testing.T) {
		if err := a.Reset(ctx, "basic"); err != nil {
			t.Fatal(err)
		}
		if err := a.Freeze(ctx, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		short, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var fe *Error
		if _, err := a.State(short); !asError(err, &fe) || !fe.Busy {
			t.Fatalf("state during a freeze: %v", err)
		}
		time.Sleep(1500 * time.Millisecond)
		st, err := a.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if fz, _ := st["freezes"].([]any); len(fz) != 1 {
			t.Fatalf("freezes = %v", st["freezes"])
		}
	})

	if fi, err := os.Stat(a.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode: %v %v", fi, err)
	}
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
