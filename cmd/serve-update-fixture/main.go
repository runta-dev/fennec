// serve-update-fixture exposes an existing signed development bundle on loopback
// for actual updater acceptance. It never changes update preferences or installs.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fennec/internal/updater"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	app := flag.String("app", "", "signed candidate .app")
	directory := flag.String("directory", "", "private fixture output directory")
	flag.Parse()
	if !filepath.IsAbs(*app) || !filepath.IsAbs(*directory) {
		return fmt.Errorf("absolute app and directory required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", *app).Run(); err != nil {
		return err
	}
	version := updater.BundleVersion(*app)
	if _, ok := updater.NormalizeVersion(version); !ok {
		return fmt.Errorf("invalid candidate version")
	}
	if err := os.MkdirAll(*directory, 0700); err != nil {
		return err
	}
	name := updater.AppAssetName(version, "arm64")
	archive := filepath.Join(*directory, name)
	if err := exec.CommandContext(ctx, "/usr/bin/ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", *app, archive).Run(); err != nil {
		return err
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	file.Close()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	base := "http://" + listener.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + version, "body": "Local development performance acceptance", "html_url": base, "assets": []map[string]any{{"name": name, "browser_download_url": base + "/asset", "size": size, "digest": "sha256:" + hex.EncodeToString(hash.Sum(nil))}}})
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, archive) })
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); server.Close() }()
	if err := os.WriteFile(filepath.Join(*directory, "feed-url"), []byte(base+"/latest"), 0600); err != nil {
		return err
	}
	fmt.Printf("Fixture ready: version=%s bytes=%d feed=%s/latest\n", version, size, base)
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
