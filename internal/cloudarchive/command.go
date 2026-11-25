package cloudarchive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Command is `fennec-terminal archive export|import|inspect`. Export writes
// the archive to stdout and one JSON line with the manifest to stderr before
// the first file; import and inspect read the archive from stdin and print a
// JSON result.
func Command(ctx context.Context, home string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	switch {
	case len(args) >= 1 && args[0] == "export":
		options := Options{Sessions: true, Planned: func(m Manifest) { json.NewEncoder(stderr).Encode(m) }}
		for _, arg := range args[1:] {
			if arg != "--no-sessions" {
				return fmt.Errorf("usage: archive export [--no-sessions]")
			}
			options.Sessions = false
		}
		_, _, err := Export(ctx, home, options, stdout)
		return err
	case len(args) == 1 && args[0] == "import":
		result, err := Import(ctx, home, stdin)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(result)
	case len(args) == 1 && args[0] == "inspect":
		manifest, err := Inspect(stdin)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(manifest)
	}
	return fmt.Errorf("usage: archive export [--no-sessions] | import | inspect")
}
