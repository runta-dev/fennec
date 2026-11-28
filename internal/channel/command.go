package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const usage = "usage: channel serve [listen address] | channel reply <queue> <number> <agent> <run> (text on stdin) | channel snippet <channel id> | channel setup (request on stdin) | channel info <channel id> | channel remove <channel id> | channel discord status|places|enable-intent|set <id> ... | channel service status|restart"

// Command runs `fennec-terminal channel ...` on the cloud computer.
func Command(home string, args []string, in io.Reader, out io.Writer) error {
	switch {
	case len(args) >= 1 && len(args) <= 2 && args[0] == "serve":
		cfg, err := LoadConfig(home)
		if err != nil {
			return err
		}
		addr := cfg.Listen
		if len(args) == 2 {
			addr = args[1]
		}
		if addr == "" {
			addr = "127.0.0.1:8787"
		}
		return serve(home, addr, out)
	case len(args) == 5 && args[0] == "reply":
		n, err := strconv.Atoi(args[2])
		if err != nil || n <= 0 {
			return errors.New(usage)
		}
		text, err := io.ReadAll(io.LimitReader(in, 64<<10))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		results, err := ReplyForRun(ctx, home, args[1], n, args[3], args[4], string(text))
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if results != nil {
			enc.Encode(results)
		}
		return err
	case len(args) == 1 && args[0] == "setup":
		// Settings › Channels (FEN-228), over SSH from the Mac.
		var req SetupRequest
		dec := json.NewDecoder(io.LimitReader(in, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return errors.New("invalid setup request")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		result, err := Setup(ctx, home, req)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(result)
	case len(args) >= 1 && args[0] == "discord":
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return discordCommand(ctx, home, args[1:], out)
	case len(args) == 2 && args[0] == "service":
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return Service(ctx, home, args[1], out)
	case len(args) == 2 && args[0] == "info":
		info, err := Info(home, args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(info)
	case len(args) == 2 && args[0] == "remove":
		if err := Remove(home, args[1]); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]bool{"ok": true})
	case len(args) == 2 && args[0] == "snippet":
		cfg, err := LoadConfig(home)
		if err != nil {
			return err
		}
		spec, ok := cfg.Spec(args[1])
		if !ok || spec.Kind != KindWidget {
			return fmt.Errorf("no widget channel %q", args[1])
		}
		base := strings.TrimRight(cfg.PublicURL, "/")
		if base == "" {
			return errors.New("set public_url in " + ConfigPath(home))
		}
		_, err = fmt.Fprintf(out, "<script src=%q data-channel=%q async></script>\n", base+"/widget.js", spec.ID)
		return err
	}
	return errors.New(usage)
}

func serve(home, addr string, out io.Writer) error {
	logger := log.New(out, "channel ", log.LstdFlags|log.LUTC)
	server := Server{Home: home, Log: logger}
	srv := server.HTTPServer(addr)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if cfg, err := LoadConfig(home); err == nil {
		server.runDiscord(ctx, cfg)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	logger.Printf("intake listening on %s", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// WriteConfig saves the config with mode 0600 (it holds secrets).
func WriteConfig(home string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := ConfigPath(home) + ".tmp"
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigPath(home))
}
