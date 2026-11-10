package sessionid

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWordsAndValidation(t *testing.T) {
	seen := map[string]bool{}
	for _, word := range words {
		if seen[word] || !Valid(word+"-cedar") {
			t.Fatal(word)
		}
		seen[word] = true
	}
	if len(words)*(len(words)-1) < 1_000_000 {
		t.Fatal(len(words))
	}
	for _, bad := range []string{"../amber-cedar", "amber-cedar/xx", "Amber-Cedar", "amber--cedar", "amber-cedar\n", "a-b"} {
		if Valid(bad) {
			t.Fatal(bad)
		}
	}
	if !Valid(strings.Repeat("a", 32)) || !Valid("amber-cedar") {
		t.Fatal("valid identities rejected")
	}
}
func TestConcurrentAllocationNeverReusesNames(t *testing.T) {
	home := t.TempDir()
	var workers sync.WaitGroup
	names := make(chan string, 512)
	for i := 0; i < 512; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id, err := Allocate(home)
			if err != nil {
				t.Error(err)
				return
			}
			names <- id
		}()
	}
	workers.Wait()
	close(names)
	seen := map[string]bool{}
	for id := range names {
		if seen[id] {
			t.Fatal("collision", id)
		}
		seen[id] = true
		info, err := os.Stat(filepath.Join(home, ".config/fennec/session-names", id))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal(id, err)
		}
	}
	if len(seen) != 512 {
		t.Fatal(len(seen))
	}
}
