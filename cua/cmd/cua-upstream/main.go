// Command cua-upstream lists the pinned shuttle release's macOS behaviours and
// checks that the capability catalog accounts for each of them.
//
//	go run ./cmd/cua-upstream inventory [-all]   print the inventory as TSV
//	go run ./cmd/cua-upstream coverage           check tests/capabilities
//
// SHUTTLE_CHECKOUT points at an existing checkout of the pinned commit;
// otherwise one is fetched into the user cache directory.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"fennec/cua/internal/capability"
	"fennec/cua/internal/upstream"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cua-upstream:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cua-upstream inventory [-all] | coverage")
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	items, err := load(root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "inventory":
		fs := flag.NewFlagSet("inventory", flag.ExitOnError)
		all := fs.Bool("all", false, "include items of other platforms")
		_ = fs.Parse(args[1:])
		for _, it := range items {
			if it.MacOS || *all {
				fmt.Printf("%s\t%v\t%s\n", it.ID, it.MacOS, it.Why)
			}
		}
		return nil
	case "coverage":
		c, err := capability.Load(filepath.Join(root, "tests", "capabilities"))
		if err != nil {
			return err
		}
		r := capability.Check(c, items)
		for _, p := range r.Problems {
			fmt.Println("problem:", p)
		}
		for _, it := range r.Missing {
			fmt.Println("missing:", it.ID)
		}
		macOS := 0
		for _, it := range items {
			if it.MacOS {
				macOS++
			}
		}
		fmt.Printf("capabilities %d (planned %d); macOS items %d: covered %d, excluded %d, missing %d; problems %d\n",
			r.Capabilities, r.Planned, macOS, r.Covered, r.Excluded, len(r.Missing), len(r.Problems))
		if !r.OK() {
			return fmt.Errorf("catalog incomplete")
		}
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func load(root string) ([]upstream.Item, error) {
	pin, err := upstream.ReadPin(filepath.Join(root, "tests", "UPSTREAM"))
	if err != nil {
		return nil, err
	}
	dir := os.Getenv("SHUTTLE_CHECKOUT")
	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		dir, err = upstream.Checkout(pin, filepath.Join(cache, "fennec", "cua", "upstream"))
		if err != nil {
			return nil, err
		}
	}
	return upstream.Inventory(dir)
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "tests", "UPSTREAM")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("run inside the cua module")
		}
		dir = parent
	}
}
