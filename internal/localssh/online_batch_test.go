package localssh

import (
	"testing"
	"time"
)

func TestOnlineBatchMergesBurstAndPersists(t *testing.T) {
	now := time.Unix(1000, 0)
	first, err := nextOnlineBatch(onlineBatch{}, "runtime", now)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err = saveOnlineBatch(directory, first); err != nil {
		t.Fatal(err)
	}
	current := loadOnlineBatch(directory)
	for _, seconds := range []int{1, 10, 35, 60} {
		next, e := nextOnlineBatch(current, "runtime", now.Add(time.Duration(seconds)*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		if next.Generation != first.Generation || next.Since != first.Since {
			t.Fatal("burst produced duplicate event")
		}
		current = next
	}
	later, _ := nextOnlineBatch(current, "runtime", now.Add(91*time.Second))
	if later.Generation == first.Generation {
		t.Fatal("separate outage was suppressed")
	}
	other, _ := nextOnlineBatch(current, "other-runtime", now.Add(61*time.Second))
	if other.Generation == first.Generation {
		t.Fatal("runtime ownership crossed")
	}
	backwards, _ := nextOnlineBatch(current, "runtime", now.Add(-time.Second))
	if backwards.Generation == first.Generation {
		t.Fatal("clock rollback extended batch indefinitely")
	}
}

func TestRecoveryRequiresMoreThanThirtySecondsOffline(t *testing.T) {
	now := time.Unix(10000, 0)
	for _, outage := range []time.Duration{time.Second, 29 * time.Second, 30 * time.Second, 30*time.Second + time.Nanosecond, time.Minute} {
		t.Run(outage.String(), func(t *testing.T) {
			b := onlineBatch{Runtime: "runtime", LastHealthy: now.Add(-time.Hour).UnixNano()}
			b.disconnected(now)
			b.disconnected(now.Add(time.Second)) // Retries must not reset the outage.
			dir := t.TempDir()
			if err := saveOnlineBatch(dir, b); err != nil {
				t.Fatal(err)
			}
			b = loadOnlineBatch(dir) // Includes state before any notification generation.
			if got := b.recovered("runtime", now.Add(outage)); got != (outage > 30*time.Second) {
				t.Fatalf("notification=%v for %s", got, outage)
			}
			if b.OfflineSince != 0 || b.LastHealthy != now.Add(outage).UnixNano() {
				t.Fatal(b)
			}
			if b.recovered("runtime", now.Add(outage+4*time.Second)) {
				t.Fatal("stability delay created a new outage")
			}
		})
	}
	b := onlineBatch{}
	if b.recovered("runtime", now) {
		t.Fatal("initial startup notified")
	}
	if !b.recovered("runtime", now.Add(time.Minute)) {
		t.Fatal("abrupt restart lost outage")
	}
	if b.recovered("different-runtime", now.Add(time.Hour)) {
		t.Fatal("cross-runtime outage")
	}
	if b.recovered("different-runtime", now) {
		t.Fatal("clock rollback notified")
	}
}
