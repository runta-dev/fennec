package installation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedInstallMovesVerificationAndStartupBeforeClick(t *testing.T) {
	root := t.TempDir()
	source, dest := filepath.Join(root, "source.app"), filepath.Join(root, "installed.app")
	bundle(t, source, "com.runta.fennec", "new")
	bundle(t, dest, "com.runta.fennec", "old")
	ops := operations()
	checks, warms, stops := 0, 0, 0
	ops.Verify = func(context.Context, string) error { checks++; return nil }
	ops.Warm = func(_ context.Context, path string) error {
		warms++
		if checks != 1 || contents(t, path) != "new" {
			t.Fatal("startup before verification")
		}
		return nil
	}
	ops.StopUI = func(context.Context, string, string) (bool, error) { stops++; return true, nil }
	ops.OpenUI = func(context.Context, string) error { return nil }
	p, err := Prepare(context.Background(), source, dest, true, ops)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if checks != 1 || warms != 1 || stops != 0 || contents(t, dest) != "old" {
		t.Fatal("preparation changed running app")
	}
	result, err := p.Apply(context.Background())
	if err != nil || checks != 1 || stops != 1 || !result.UIRestarted {
		t.Fatalf("apply: %+v %v checks=%d stops=%d", result, err, checks, stops)
	}
	if contents(t, result.Backup) != "old" || contents(t, dest) != "new" {
		t.Fatal("publication lost bundle")
	}
	if _, err := p.Apply(context.Background()); err == nil {
		t.Fatal("prepared publication replayed")
	}
}

func TestPreparedInstallRejectsCandidateMutationsBeforeStoppingUI(t *testing.T) {
	for _, mutation := range []string{"write_restored_mtime", "replace", "mode", "added", "deleted", "symlink", "root_symlink"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			source, dest := filepath.Join(root, "source.app"), filepath.Join(root, "installed.app")
			bundle(t, source, "com.runta.fennec", "new")
			bundle(t, dest, "com.runta.fennec", "old")
			ops := operations()
			ops.StopUI = func(context.Context, string, string) (bool, error) {
				t.Error("tampered candidate stopped UI")
				return false, nil
			}
			ops.OpenUI = func(context.Context, string) error { return nil }
			p, err := Prepare(context.Background(), source, dest, true, ops)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			file := filepath.Join(p.incoming, "Contents/MacOS/fen")
			info, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "write_restored_mtime":
				err = os.WriteFile(file, []byte("bad"), 0700)
				if err == nil {
					err = os.Chtimes(file, info.ModTime(), info.ModTime())
				}
			case "replace":
				err = os.WriteFile(file+".new", []byte("new"), info.Mode())
				if err == nil {
					err = os.Rename(file+".new", file)
				}
			case "mode":
				err = os.Chmod(file, 0600)
			case "added":
				err = os.WriteFile(filepath.Join(p.incoming, "added"), []byte("x"), 0600)
			case "deleted":
				err = os.Remove(file)
			case "symlink":
				err = os.Remove(file)
				if err == nil {
					err = os.Symlink(filepath.Join(source, "Contents/MacOS/fen"), file)
				}
			case "root_symlink":
				err = os.Rename(p.incoming, p.incoming+".moved")
				if err == nil {
					err = os.Symlink(p.incoming+".moved", p.incoming)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Apply(context.Background()); !errors.Is(err, ErrUnverifiedApplication) {
				t.Fatalf("mutation accepted: %v", err)
			}
			if contents(t, dest) != "old" {
				t.Fatal("original application changed")
			}
		})
	}
}

func TestPreparedInstallRejectsMutationDuringVerification(t *testing.T) {
	root := t.TempDir()
	source, dest := filepath.Join(root, "source.app"), filepath.Join(root, "installed.app")
	bundle(t, source, "com.runta.fennec", "new")
	ops := operations()
	ops.Verify = func(_ context.Context, path string) error {
		return os.WriteFile(filepath.Join(path, "Contents/MacOS/fen"), []byte("bad"), 0700)
	}
	ops.Warm = func(context.Context, string) error { t.Error("mutated executable was run"); return nil }
	if p, err := Prepare(context.Background(), source, dest, false, ops); !errors.Is(err, ErrUnverifiedApplication) || p != nil {
		t.Fatalf("mutation accepted: %v", err)
	}
}

func TestPreparedWarmMetadataChangesRequireReverification(t *testing.T) {
	root := t.TempDir()
	source, dest := filepath.Join(root, "source.app"), filepath.Join(root, "installed.app")
	bundle(t, source, "com.runta.fennec", "new")
	ops := operations()
	checks := 0
	ops.Verify = func(context.Context, string) error {
		checks++
		if checks > 1 {
			return errors.New("warm changed seal")
		}
		return nil
	}
	ops.Warm = func(_ context.Context, path string) error {
		return os.Chmod(filepath.Join(path, "Contents/MacOS/fen"), 0600)
	}
	if p, err := Prepare(context.Background(), source, dest, false, ops); !errors.Is(err, ErrUnverifiedApplication) || p != nil || checks != 2 {
		t.Fatalf("missing revalidation: %v checks=%d", err, checks)
	}
}

func TestPreparedOwnsPrivateSnapshotAndRejectsChangedDestination(t *testing.T) {
	for _, changeDestination := range []bool{false, true} {
		t.Run(map[bool]string{false: "source changed", true: "destination changed"}[changeDestination], func(t *testing.T) {
			root := t.TempDir()
			source, dest := filepath.Join(root, "source.app"), filepath.Join(root, "installed.app")
			bundle(t, source, "com.runta.fennec", "new")
			bundle(t, dest, "com.runta.fennec", "old")
			ops := operations()
			stopped := 0
			ops.StopUI = func(context.Context, string, string) (bool, error) { stopped++; return false, nil }
			ops.OpenUI = func(context.Context, string) error { return nil }
			p, err := Prepare(context.Background(), source, dest, true, ops)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if err := os.WriteFile(filepath.Join(source, "Contents/MacOS/fen"), []byte("bad"), 0700); err != nil {
				t.Fatal(err)
			}
			if changeDestination {
				if err := os.Rename(dest, dest+".moved"); err != nil {
					t.Fatal(err)
				}
				bundle(t, dest, "com.runta.fennec", "concurrent")
			}
			_, err = p.Apply(context.Background())
			if changeDestination {
				if err == nil || stopped != 0 || contents(t, dest) != "concurrent" {
					t.Fatalf("concurrent install overwritten: %v", err)
				}
			} else if err != nil || contents(t, dest) != "new" {
				t.Fatalf("source mutation reached prepared candidate: %v", err)
			}
		})
	}
}

// Stopping the old UI runs the incoming copy's adapter, and macOS then records
// that launch on the bundle's metadata. The changed tree is verified again
// instead of being rejected outright.
func TestStoppingUIMetadataChangesRequireReverification(t *testing.T) {
	for _, verifies := range []bool{true, false} {
		t.Run(map[bool]string{true: "still signed", false: "no longer signed"}[verifies], func(t *testing.T) {
			root := t.TempDir()
			source, dest := filepath.Join(root, "source.app"), filepath.Join(root, "installed.app")
			bundle(t, source, "com.runta.fennec", "new")
			bundle(t, dest, "com.runta.fennec", "old")
			ops := operations()
			checks := 0
			ops.Verify = func(_ context.Context, path string) error {
				if filepath.Base(path) != "incoming.app" {
					return nil // the rolled-back original, verified before reopening
				}
				checks++
				if checks > 1 && !verifies {
					return errors.New("signature no longer valid")
				}
				return nil
			}
			ops.StopUI = func(_ context.Context, incoming, _ string) (bool, error) {
				// Stands in for the launch record macOS attaches to the bundle.
				return true, os.Chmod(filepath.Join(incoming, "Contents"), 0750)
			}
			var reopened string
			ops.OpenUI = func(_ context.Context, path string) error {
				reopened = contents(t, path)
				return nil
			}
			result, err := Install(context.Background(), source, dest, true, ops)
			if checks != 2 {
				t.Fatalf("changed candidate verified %d times, want 2", checks)
			}
			if verifies {
				if err != nil || contents(t, dest) != "new" || reopened != "new" || contents(t, result.Backup) != "old" {
					t.Fatalf("result=%+v err=%v installed=%q reopened=%q", result, err, contents(t, dest), reopened)
				}
				return
			}
			if !errors.Is(err, ErrUnverifiedApplication) || contents(t, dest) != "old" || reopened != "old" {
				t.Fatalf("unverified candidate: err=%v installed=%q reopened=%q", err, contents(t, dest), reopened)
			}
		})
	}
}
