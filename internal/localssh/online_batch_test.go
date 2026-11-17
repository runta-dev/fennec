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
