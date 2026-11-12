package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	StepDownload  = "download"
	StepRetry     = "retry"
	StepVerify    = "verify"
	StepUnpack    = "unpack"
	StepSignature = "signature"
)

var stallTimeout = 45 * time.Second

type progressOptions struct {
	onProgress func(done, total int64)
	onStep     func(step string)
}

// NetError marks a transient download interruption that can be retried.
type NetError struct {
	Err error
}

func (e *NetError) Error() string { return e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// IsNetworkError reports whether err came from a network/transport failure.
func IsNetworkError(err error) bool {
	var netErr *NetError
	return errors.As(err, &netErr)
}

func downloadVerifiedAsset(ctx context.Context, transport http.RoundTripper, asset Asset, dest string, opts progressOptions) error {
	if asset.URL == "" {
		return fmt.Errorf("update asset %s has no download URL", asset.Name)
	}
	expectedSum := normalizeSHA256(asset.SHA256)
	if expectedSum == "" {
		return fmt.Errorf("update asset %s is missing a verified SHA-256 digest", asset.Name)
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	// Like Magpie's fileClient: no total wall-clock timeout so slow moving
	// downloads complete, while stalled connections abort via stallTimeout.
	client := &http.Client{Transport: transport}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if opts.onStep != nil {
				opts.onStep(StepRetry)
			}
			timer := time.NewTimer(time.Duration(attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		err := fetchAssetOnce(ctx, client, asset, expectedSum, dest, opts)
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil || !IsNetworkError(err) {
			return err
		}
	}
	return lastErr
}

func fetchAssetOnce(ctx context.Context, client *http.Client, asset Asset, expectedSum, dest string, opts progressOptions) error {
	var have int64
	if info, err := os.Stat(dest); err == nil && info.Mode().IsRegular() {
		if asset.Size <= 0 || info.Size() < asset.Size {
			have = info.Size()
		} else if info.Size() == asset.Size && strings.EqualFold(fileSHA256(dest), expectedSum) {
			if opts.onStep != nil {
				opts.onStep(StepVerify)
			}
			return nil
		}
	}

	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()
	idle := time.AfterFunc(stallTimeout, cancelAttempt)
	defer idle.Stop()

	if opts.onStep != nil {
		opts.onStep(StepDownload)
	}

	var resp *http.Response
	for restarted := false; ; restarted = true {
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, asset.URL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "Fennec-Updater")
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		resp, err = client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &NetError{Err: err}
		}
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have > 0 && !restarted {
			resp.Body.Close()
			have = 0
			continue
		}
		if resp.StatusCode == http.StatusPartialContent && have > 0 && !restarted && !strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", have)) {
			resp.Body.Close()
			have = 0
			continue
		}
		break
	}
	defer resp.Body.Close()

	openFlags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 && strings.HasPrefix(resp.Header.Get("Content-Range"), fmt.Sprintf("bytes %d-", have)):
		openFlags = os.O_WRONLY | os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		have = 0
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return &NetError{Err: fmt.Errorf("download %s: HTTP %d", asset.Name, resp.StatusCode)}
	default:
		return fmt.Errorf("download %s: HTTP %d", asset.Name, resp.StatusCode)
	}

	file, err := os.OpenFile(dest, openFlags, 0644)
	if err != nil {
		return err
	}
	idle.Reset(stallTimeout)
	tracked := &stallReader{r: resp.Body, idle: idle}
	var reader io.Reader = tracked
	if opts.onProgress != nil {
		total := resp.ContentLength
		if total > 0 {
			total += have
		} else {
			total = asset.Size
		}
		if total < 0 {
			total = 0
		}
		opts.onProgress(have, total)
		reader = &progressReader{r: tracked, done: have, total: total, report: opts.onProgress}
	}
	_, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil {
		if ctx.Err() == nil && (tracked.err != nil || attemptCtx.Err() != nil) {
			return &NetError{Err: copyErr}
		}
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	idle.Stop()

	if opts.onStep != nil {
		opts.onStep(StepVerify)
	}
	if !strings.EqualFold(fileSHA256(dest), expectedSum) {
		_ = os.Remove(dest)
		return fmt.Errorf("%s failed SHA-256 verification", asset.Name)
	}
	return nil
}

type stallReader struct {
	r    io.Reader
	idle *time.Timer
	err  error
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 && s.idle != nil {
		s.idle.Reset(stallTimeout)
	}
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}

type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.done += int64(n)
		p.report(p.done, p.total)
	}
	return n, err
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
