package traffic

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

func capacityHTTPClient() *http.Client {
	// Do not measure a configured HTTP proxy or follow a redirect to another
	// endpoint. Each bounded probe owns and closes its TLS connection.
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true,
			DisableCompression: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("capacity probe redirect rejected") },
	}
}

// uploadCapacity requires an endpoint whose 2xx response acknowledges complete
// receipt of the POST body (the Cloudflare /__up contract). It includes dial,
// TLS, upload, acknowledgement and bounded response reading in elapsed time.
// Merely filling a local socket buffer, a reset, or a partial upload is not a
// successful measurement. No guessed wire overhead is added to payload bytes.
func uploadCapacity(ctx context.Context, client *http.Client, cfg CapacityConfig) (int, error) {
	payload := make([]byte, cfg.ProbeBytes)
	if _, err := rand.Read(payload); err != nil {
		return 0, err
	}
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) { wrote.Store(info.Err == nil) },
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ProbeTarget, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Close = true
	// Do not replay a probe even if a custom transport attempts retries.
	request.GetBody = nil
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return 0, fmt.Errorf("capacity upload returned HTTP %d", response.StatusCode)
	}
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, 4097))
	if err != nil {
		return 0, err
	}
	if n > 4096 || !wrote.Load() {
		return 0, errors.New("incomplete capacity upload acknowledgement")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	elapsed := time.Since(started).Seconds()
	if elapsed <= 0 {
		return 0, errors.New("invalid capacity measurement duration")
	}
	bps := float64(cfg.ProbeBytes) / elapsed
	// A bounded conversion prevents integer overflow even for synthetic clocks
	// or extremely fast endpoints. The policy ceiling is enforced separately.
	if bps < 1 || bps > float64(1<<30) {
		return 0, errors.New("capacity measurement out of range")
	}
	return int(bps), nil
}
