package preview

import (
	"fennec/internal/sessionid"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Command registers a workspace file and prints the literal marker for the
// agent's reply. --session is explicit when the agent works in a repo checkout.
func Command(home string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("preview", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	session := flags.String("session", "", "session workspace")
	title := flags.String("title", "", "preview title")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("usage: fen preview [--session word-word] [--title title] workspace-file")
	}
	source := flags.Arg(0)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if *session == "" {
		relative, err := filepath.Rel(filepath.Join(home, ".fen"), cwd)
		if err != nil || !filepath.IsLocal(relative) {
			return fmt.Errorf("specify --session for the file's workspace")
		}
		*session = strings.Split(relative, string(filepath.Separator))[0]
	}
	if !sessionid.Valid(*session) {
		return ErrInvalid
	}
	if !filepath.IsAbs(source) && !strings.HasPrefix(source, "~/") {
		source = filepath.Join(cwd, source)
	}
	metadata, err := (Store{Home: home}).Register(*session, source, *title)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, Markup(metadata.Ref, metadata.Title))
	return err
}
