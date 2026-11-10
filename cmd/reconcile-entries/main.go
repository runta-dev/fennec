// reconcile-entries repairs only Fennec-owned wrappers to a stable installed bundle.
package main

import (
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/launcher"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	daemon := flag.String("daemon", "", "installed daemon path")
	flag.Parse()
	if !filepath.IsAbs(*daemon) || strings.HasPrefix(*daemon, "/tmp/") || strings.HasPrefix(*daemon, os.TempDir()) {
		return fmt.Errorf("stable installed executable required")
	}
	for _, id := range flag.Args() {
		if !launcher.Supported[id] {
			return fmt.Errorf("unsupported agent")
		}
		path, err := agent.EntryPath(id)
		if err != nil {
			return err
		}
		if err = launcher.ReconcileEntry(path, *daemon, id); err != nil {
			return err
		}
		if _, err = launcher.OriginalEntry(path); err != nil {
			return err
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"agent": id, "repaired": true})
	}
	return nil
}
