package updater

import (
	"context"
	"errors"
	"fennec/internal/installation"
	"fennec/internal/uninstall"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type preparedFixture struct {
	applied, closed int
	fail            bool
	err             error
}

type installationFixture struct{ *installation.Prepared }

func (p installationFixture) Apply(ctx context.Context) error {
	_, err := p.Prepared.Apply(ctx)
	return err
}

func TestServicePreparesAndPublishesRealInstallationCopy(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	svc := NewService(Config{Bundle: installed, Current: "0.2.100", Arch: "arm64",
		Feed: func() string { return r.srv.URL + "/latest" }, HTTPClient: r.srv.Client(), Transport: r.srv.Client().Transport, VerifySigner: acceptSigner,
		PrepareUpdate: func(ctx context.Context, source, dest string) (PreparedUpdate, error) {
			ops := installation.SystemOperations()
			ops.StopUI, ops.OpenUI = nil, nil
			ops.Verify = func(context.Context, string) error { return nil }
			ops.Copy = func(_ context.Context, source, dest string) error { return os.CopyFS(dest, os.DirFS(source)) }
			prepared, err := installation.Prepare(ctx, source, dest, true, ops)
			if err != nil {
				return nil, err
			}
			return installationFixture{prepared}, nil
		},
	})
	defer svc.closePrepared()
	ctx := context.Background()
	if snap := svc.Check(ctx, true); snap.State != "ready" {
		t.Fatalf("preparation failed: %+v", snap)
	}
	if filepath.Ext(svc.staged) == ".app" {
		t.Fatal("downloaded source retained app extension")
	}
	if _, err := svc.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := TreeDigest(installed); err != nil || got != r.referenceDigest(t) {
		t.Fatalf("published candidate differs: %v", err)
	}
}

func (p *preparedFixture) Apply(context.Context) error {
	p.applied++
	if p.err != nil {
		return p.err
	}
	if p.fail {
		return errors.New("candidate changed")
	}
	return nil
}

func TestServiceDiscardsChangedPreparedCandidate(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "stage", "staged-payload")
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	prepared := &preparedFixture{err: installation.ErrUnverifiedApplication}
	svc := NewService(Config{Bundle: filepath.Join(t.TempDir(), "Fennec.app"), Current: "0.2.100"})
	svc.state, svc.staged, svc.prepared = "ready", stage, prepared
	snap, err := svc.Install(context.Background())
	if !errors.Is(err, installation.ErrUnverifiedApplication) || snap.State != "error" || !snap.Retry || svc.staged != "" || svc.prepared != nil || prepared.closed != 1 {
		t.Fatalf("invalid candidate retained: %+v %v", snap, err)
	}
}
func (p *preparedFixture) Close() { p.closed++ }

func TestServiceIsNotReadyUntilPreparedCandidateFinishes(t *testing.T) {
	r := newComponentRelease(t, "0.2.200", "build-1")
	installed := installedFennec(t, "build-1", true)
	entered, release := make(chan struct{}), make(chan struct{})
	prepared := &preparedFixture{}
	svc := NewService(Config{Bundle: installed, Current: "0.2.100", Arch: "arm64",
		Feed: func() string { return r.srv.URL + "/latest" }, HTTPClient: r.srv.Client(), Transport: r.srv.Client().Transport,
		VerifySigner: acceptSigner,
		PrepareUpdate: func(ctx context.Context, source, dest string) (PreparedUpdate, error) {
			if _, err := uninstall.BundleID(source); err != nil {
				return nil, err
			}
			close(entered)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return prepared, nil
			}
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan Snapshot, 1)
	go func() { done <- svc.Check(ctx, true) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("preflight was not called")
	}
	if snap := svc.Snapshot(ctx, false); snap.State != "downloading" {
		t.Fatalf("ready before preflight: %+v", snap)
	}
	if _, err := svc.Install(ctx); err == nil {
		t.Fatal("installation started during preparation")
	}
	close(release)
	select {
	case snap := <-done:
		if snap.State != "ready" {
			t.Fatalf("preparation did not become ready: %+v", snap)
		}
	case <-ctx.Done():
		t.Fatal("preparation stalled")
	}
	if _, err := svc.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if prepared.applied != 1 || prepared.closed != 1 {
		t.Fatalf("unexpected lifecycle: %+v", prepared)
	}
}

func TestServiceAppliesPreparedCandidateWithoutRestaging(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rejected"}[fail], func(t *testing.T) {
			stage := filepath.Join(t.TempDir(), "stage", "staged-payload")
			if err := os.MkdirAll(stage, 0700); err != nil {
				t.Fatal(err)
			}
			prepared := &preparedFixture{fail: fail}
			svc := NewService(Config{Bundle: filepath.Join(t.TempDir(), "Fennec.app"), Current: "0.2.100",
				ApplyUpdate: func(context.Context, string, string) error { t.Fatal("prepared install used legacy path"); return nil },
			})
			svc.state, svc.staged, svc.prepared = "ready", stage, prepared
			_, err := svc.Install(context.Background())
			if (err != nil) != fail || prepared.applied != 1 {
				t.Fatalf("apply: %v %+v", err, prepared)
			}
			if fail {
				if _, err := os.Stat(stage); err != nil {
					t.Fatal("failed install lost staged source")
				}
				if prepared.closed != 0 {
					t.Fatal("retry candidate closed prematurely")
				}
			} else if svc.prepared != nil || prepared.closed != 1 {
				t.Fatal("successful candidate not released")
			}
			svc.closePrepared()
			svc.closePrepared()
			if prepared.closed != 1 {
				t.Fatal("candidate cleanup not exactly once")
			}
		})
	}
}
