package magpieweb

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"fennec/internal/terminalstream"
)

// Opt in on a prepared cloud computer, using the terminal artifact being tested.
func TestLivePanelThroughResident(t *testing.T) {
	binary := os.Getenv("FENNEC_PANEL_TEST_BINARY")
	if binary == "" {
		t.Skip("requires a prepared cloud and terminal binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "serve")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	transport := terminalstream.StartPipes(input, output)
	defer func() { transport.Close(); cancel(); cmd.Wait() }()
	if err = transport.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !transport.Supports(terminalstream.CapabilityMagpieWeb) {
		t.Fatal("missing panel capability")
	}
	local := &Local{}
	defer local.Close()
	link, err := local.URL(ctx, func(ctx context.Context) (net.Conn, error) {
		return transport.DialSocket(ctx, terminalstream.SocketMagpieWeb, "")
	}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	response, err := client.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !strings.Contains(strings.ToLower(string(body)), "<html") {
		t.Fatalf("panel did not load: status=%d", response.StatusCode)
	}
	if strings.Contains(string(body), "MAGPIE_WEB_KEY") || response.Header.Get("Set-Cookie") != "" {
		t.Fatal("cloud access material reached browser")
	}
	base, _ := url.Parse(link)
	base.RawQuery = ""
	base.Path = "/api/settings"
	settings, e := client.Get(base.String())
	if e != nil {
		t.Fatal(e)
	}
	defer settings.Body.Close()
	data, _ := io.ReadAll(settings.Body)
	if settings.StatusCode != 200 || !json.Valid(data) {
		t.Fatalf("settings API did not load: status=%d", settings.StatusCode)
	}
	t.Log("real Magpie page and settings API loaded through terminal serve, resident socket and authenticated loopback proxy")
}
