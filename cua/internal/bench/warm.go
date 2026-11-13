package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"fennec/cua/internal/vm"
)

// A warm image is a clone of the golden image booted to a settled desktop
// and suspended with its memory saved. A trial that clones it resumes in
// seconds instead of booting. A resumed clone keeps the MAC address the
// image was suspended with (the saved state does not restore under another
// one), and tart boots afresh rather than run two VMs with one address, so
// each concurrent worker has a warm image of its own: slot 1, 2, ...

// warmRecipe changes whenever BuildWarm prepares the desktop differently,
// which invalidates every warm image built before.
const warmRecipe = 1

// warmStampFile, in the warm image's tart directory, records what the
// image was built from. tart clone does not copy it.
const warmStampFile = "cua-bench-warm.json"

type warmStamp struct {
	Golden   string    `json:"golden"`
	GoldenID string    `json:"golden_id"`
	Built    time.Time `json:"built"`
}

// WarmName is the name of warm image slot (1-based) of golden.
func WarmName(golden string, slot int) string { return fmt.Sprintf("%s-warm-%d", golden, slot) }

// GoldenID identifies the golden image's current contents: its
// configuration and NVRAM, its disk's size and modification time (the disk
// is 50 GB; booting the image always rewrites it), the tart version and
// the warm-up recipe.
func GoldenID(ctx context.Context, tart, golden string) (string, error) {
	g := &vm.VM{Name: golden, Tart: tart}
	h := sha256.New()
	fmt.Fprintf(h, "recipe %d\n", warmRecipe)
	out, err := exec.CommandContext(ctx, tart, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("tart --version: %v", err)
	}
	fmt.Fprintf(h, "tart %s\n", out)
	for _, f := range []string{"config.json", "nvram.bin"} {
		file, err := os.Open(filepath.Join(g.Dir(), f))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n", f)
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			return "", err
		}
	}
	st, err := os.Stat(filepath.Join(g.Dir(), "disk.img"))
	if err != nil {
		return "", err
	}
	fmt.Fprintf(h, "disk %d %d\n", st.Size(), st.ModTime().UnixNano())
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ValidWarm returns warm image slot of golden, or why it cannot be used:
// missing, not suspended, or built from another golden image.
func ValidWarm(ctx context.Context, tart, golden string, slot int) (string, error) {
	w := &vm.VM{Name: WarmName(golden, slot), Tart: tart}
	st, err := w.State(ctx)
	if err != nil {
		return "", err
	}
	if st == "" {
		return "", fmt.Errorf("%s does not exist", w.Name)
	}
	if st != vm.StateSuspended {
		return "", fmt.Errorf("%s is %s, not suspended", w.Name, st)
	}
	data, err := os.ReadFile(filepath.Join(w.Dir(), warmStampFile))
	if err != nil {
		return "", fmt.Errorf("%s has no build record", w.Name)
	}
	var stamp warmStamp
	if err := json.Unmarshal(data, &stamp); err != nil {
		return "", fmt.Errorf("%s: build record: %v", w.Name, err)
	}
	id, err := GoldenID(ctx, tart, golden)
	if err != nil {
		return "", err
	}
	if stamp.Golden != golden || stamp.GoldenID != id {
		return "", fmt.Errorf("%s was built from an older %s (or another tart or recipe); rebuild it with cua-bench warm", w.Name, golden)
	}
	return w.Name, nil
}

// BuildWarm (re)builds warm images 1..slots of golden: a fresh clone with a
// MAC address of its own, booted, its desktop settled exactly as a cold
// trial waits for it, then suspended. Nothing task- or backend-specific
// happens here; trials still run their setup, backend start and grading
// after the resume.
func BuildWarm(ctx context.Context, tart, golden string, slots int, logf func(string, ...any)) error {
	id, err := GoldenID(ctx, tart, golden)
	if err != nil {
		return err
	}
	g := &vm.VM{Name: golden, Tart: tart}
	if st, err := g.State(ctx); err != nil {
		return err
	} else if st != vm.StateStopped {
		return fmt.Errorf("%s is %q; it must exist and be stopped", golden, st)
	}
	goldenMAC, goldenMachine, err := g.Identity()
	if err != nil {
		return err
	}
	usedMAC := map[string]bool{goldenMAC: true}
	usedMachine := map[string]bool{}
	goldenUUID := ""
	for slot := 1; slot <= slots; slot++ {
		w := &vm.VM{Name: WarmName(golden, slot), User: vm.DefaultUser, Tart: tart}
		start := time.Now()
		if st, err := w.State(ctx); err != nil {
			return err
		} else if st != "" {
			if err := w.Delete(ctx); err != nil {
				return err
			}
		}
		if err := w.Clone(ctx, golden); err != nil {
			return err
		}
		if err := uniqueIdentity(ctx, w, usedMAC, usedMachine); err != nil {
			return err
		}
		_, machine, err := w.Identity()
		if err != nil {
			return err
		}
		if machine != goldenMachine {
			if err := adoptByHost(ctx, w, goldenUUID); err != nil {
				_ = w.Delete(context.Background())
				return fmt.Errorf("%s: %w", w.Name, err)
			}
		}
		uuid, err := warmUp(ctx, w)
		if err != nil {
			_ = w.Delete(context.Background())
			return fmt.Errorf("%s: %w", w.Name, err)
		}
		if slot == 1 {
			goldenUUID = uuid
		}
		stamp, _ := json.MarshalIndent(warmStamp{Golden: golden, GoldenID: id, Built: time.Now().UTC()}, "", "  ")
		if err := os.WriteFile(filepath.Join(w.Dir(), warmStampFile), stamp, 0o644); err != nil {
			return err
		}
		logf("built %s in %s", w.Name, time.Since(start).Round(time.Second))
	}
	if now, err := GoldenID(ctx, tart, golden); err != nil || now != id {
		return errors.New("the golden image changed while the warm images were built; build them again")
	}
	return nil
}

// resumeWarm makes v a clone of the warm image and resumes it: the desktop
// is the one the image settled before it was suspended, and the guest clock
// is set to the host's.
func resumeWarm(ctx context.Context, v *vm.VM, warm string, logf func(string, ...any)) (*vm.Session, error) {
	if err := v.Clone(ctx, warm); err != nil {
		return nil, fmt.Errorf("clone: %w", err)
	}
	if err := v.Resume(ctx, 2*time.Minute); err != nil {
		return nil, err
	}
	s, err := v.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if out, ok := guestShell(ctx, s, desktopUp, 20*time.Second); !ok {
		return nil, fmt.Errorf("the resumed desktop is not up: %s", out)
	}
	skew, err := s.SyncClock(ctx)
	if err != nil {
		return nil, fmt.Errorf("clock: %w", err)
	}
	if skew >= time.Second || skew <= -time.Second {
		logf("%s: guest clock was off by %s after the resume; set to the host's", v.Name, skew)
	}
	return s, nil
}

// uniqueIdentity gives each warm image a MAC address and a machine
// identifier of its own. Virtualization.framework refuses to restore a
// saved state while another VM with the same machine identifier runs, so
// two workers cannot resume clones that share one; the first slot keeps
// the golden image's identifier (exactly what a cold trial boots with),
// the others get new ones.
func uniqueIdentity(ctx context.Context, w *vm.VM, usedMAC, usedMachine map[string]bool) error {
	for i := 0; ; i++ {
		mac, machine, err := w.Identity()
		if err != nil {
			return err
		}
		if !usedMAC[mac] && !usedMachine[machine] {
			usedMAC[mac], usedMachine[machine] = true, true
			return nil
		}
		if i == 5 {
			return fmt.Errorf("%s: no unique MAC address or machine identifier after %d tries", w.Name, i)
		}
		if usedMAC[mac] {
			if err := w.RandomMAC(ctx); err != nil {
				return err
			}
		}
		if usedMachine[machine] {
			if err := w.RandomSerial(ctx); err != nil {
				return err
			}
		}
	}
}

// adoptByHost gives a warm image with a new machine identifier the golden
// image's per-host preferences. macOS keys them by hardware UUID
// (~/Library/Preferences/ByHost/*.<UUID>.plist: screen saver, Control
// Center, Music's library agent, global per-host settings), so a new
// identifier would otherwise start the desktop with defaults a cold trial
// never sees. The VM boots once, replaces the files of its new UUID with
// copies of the golden UUID's, and shuts down; warmUp's boot then logs in
// with them.
func adoptByHost(ctx context.Context, w *vm.VM, goldenUUID string) error {
	if goldenUUID == "" {
		return errors.New("the golden image's hardware UUID is unknown (slot 1 records it)")
	}
	bctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	_, err := w.Start(bctx)
	cancel()
	if err != nil {
		return fmt.Errorf("first boot: %w", err)
	}
	s, err := w.Connect(ctx)
	if err != nil {
		return err
	}
	if err := WaitDesktop(ctx, s); err != nil {
		return err
	}
	script := `set -e
cd "$HOME/Library/Preferences/ByHost"
new=` + hardwareUUID + `
old=` + vm.Quote(goldenUUID) + `
test -n "$new" && test "$new" != "$old"
ls -A | grep -q "\.$old\.plist$"
for f in $(ls -A | grep "\.$new\.plist$"); do rm -f "$f"; done
for f in $(ls -A | grep "\.$old\.plist$"); do cp -p "$f" "${f%.$old.plist}.$new.plist"; done
sync`
	if out, ok := guestShell(ctx, s, script, time.Minute); !ok {
		return fmt.Errorf("copy per-host preferences: %s", out)
	}
	// cfprefsd still holds the new UUID's old values and would write them
	// back at logout, so the guest halts at once instead of shutting down.
	_, _ = guestShell(ctx, s, "sync; sudo /sbin/halt -q", 20*time.Second)
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if st, err := w.State(ctx); err == nil && st == vm.StateStopped {
			return nil
		}
		if !sleepCtx(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return errors.New("the VM did not halt within 2 minutes")
}

// hardwareUUID prints the guest's hardware UUID.
const hardwareUUID = `"$(ioreg -rd1 -c IOPlatformExpertDevice | awk -F'"' '/IOPlatformUUID/ {print $4}')"`

// warmUp boots the warm image, waits for its desktop as a cold trial does,
// and suspends it. It returns the guest's hardware UUID.
func warmUp(ctx context.Context, w *vm.VM) (string, error) {
	bctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	err := w.StartSuspendable(bctx)
	cancel()
	if err != nil {
		return "", fmt.Errorf("boot: %w", err)
	}
	s, err := w.Connect(ctx)
	if err != nil {
		return "", err
	}
	if err := WaitDesktop(ctx, s); err != nil {
		return "", err
	}
	uuid, ok := guestShell(ctx, s, "echo "+hardwareUUID, 20*time.Second)
	if !ok || uuid == "" {
		return "", fmt.Errorf("hardware UUID: %s", uuid)
	}
	// Write what the boot changed to the disk, so the disk and the saved
	// memory agree on every later restore.
	if _, err := s.Output(ctx, "sync"); err != nil {
		return "", err
	}
	return uuid, w.Suspend(ctx)
}
