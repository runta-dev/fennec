package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fennec/internal/installation"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func installCommand(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(output)
	check := flags.Bool("check", false, "Inspect installation without changes")
	replace := flags.Bool("replace", false, "Replace an existing Fennec installation")
	destination := flags.String("destination", "", "Destination application path")
	installed := flags.String("installed-application", "", "Verify a known installed copy (inspection only)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected install arguments")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if *installed != "" && !*check {
		return fmt.Errorf("--installed-application is only valid with --check")
	}
	state, err := installation.Inspect(executable, home, *installed)
	if err != nil {
		return err
	}
	if *check {
		return json.NewEncoder(output).Encode(state)
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("application installation requires macOS")
	}
	if *destination == "" {
		*destination = state.Destination
	}
	if state.Required {
		sourcePath, _ := filepath.EvalSymlinks(state.Source)
		destinationPath, _ := filepath.EvalSymlinks(*destination)
		if sourcePath != "" && sourcePath == destinationPath {
			_ = json.NewEncoder(output).Encode(map[string]string{"error": "install_failed"})
			return fmt.Errorf("choose an installation location outside the disk image or temporary copy")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := installation.Install(ctx, state.Source, *destination, *replace, installation.SystemOperations())
	if err != nil {
		code := "install_failed"
		if errors.Is(err, installation.ErrUnverifiedApplication) {
			code = "unverified_app"
		}
		_ = json.NewEncoder(output).Encode(map[string]string{"error": code, "backup": result.Backup})
		return err
	}
	return json.NewEncoder(output).Encode(result)
}
