package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"fennec/internal/agent"
	"fennec/internal/daemon"
)

func Run(arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "--help" {
		fmt.Fprintln(output, "Fennec by Runta\n\nUsage:\n  fennec serve [-addr 127.0.0.1:7420]  Open the local control surface\n  fennec agents                         Discover installed CLI agents\n\nCloud launch and connector execution are not implemented yet.")
		return nil
	}
	switch arguments[0] {
	case "agents":
		if len(arguments) != 1 {
			return errors.New("agents takes no arguments")
		}
		return json.NewEncoder(output).Encode(agent.Discover())
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		flags.SetOutput(output)
		address := flags.String("addr", "127.0.0.1:7420", "loopback listen address")
		if err := flags.Parse(arguments[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() != 0 || !daemon.Loopback(*address) {
			return errors.New("serve requires a loopback IP address and no positional arguments")
		}
		server := &http.Server{Addr: *address, Handler: daemon.Handler(*address), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, WriteTimeout: 15 * time.Second}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		}()
		fmt.Fprintf(output, "Fennec control surface: http://%s\nScaffold only; no cloud resources or credentials are accessed.\n", *address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported command %q; run fennec help (cloud launch is not implemented)", arguments[0])
	}
}
