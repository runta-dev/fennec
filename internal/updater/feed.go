package updater

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const DefaultFeedURL = "https://api.github.com/repos/runta-dev/fennec/releases/latest"

// FeedURL returns the release metadata endpoint, allowing FENNEC_UPDATE_FEED
// for local testing or custom release mirrors.
func FeedURL() string {
	if custom := strings.TrimSpace(os.Getenv("FENNEC_UPDATE_FEED")); custom != "" {
		return custom
	}
	return DefaultFeedURL
}

// Asset describes one downloadable archive for a release.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Release describes one published Fennec version.
type Release struct {
	Version string           `json:"version"`
	Notes   string           `json:"notes"`
	URL     string           `json:"url"`
	Assets  map[string]Asset `json:"assets"`
	// Components is the release component manifest for the target architecture,
	// or nil when the release publishes only the full archive.
	Components *ComponentSet `json:"-"`
}

// AppAssetName returns the expected macOS archive name for version and arch.
func AppAssetName(version, arch string) string {
	return fmt.Sprintf("Fennec-%s-macos-%s.zip", version, arch)
}

// TargetArch returns the CPU architecture an update should fetch, preferring
// native arm64 when an amd64 build runs under Rosetta on Apple Silicon.
func TargetArch() string {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" && isRosettaTranslated() {
		return "arm64"
	}
	return runtime.GOARCH
}

// FetchLatest queries feedURL and resolves asset SHA-256 digests from either
// inline asset metadata or the release's SHA256SUMS file.
func FetchLatest(ctx context.Context, httpClient *http.Client, feedURL string) (*Release, error) {
	if feedURL == "" {
		feedURL = FeedURL()
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json, application/json")
	req.Header.Set("User-Agent", "Fennec-Updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update feed HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(body) > 4<<20 {
		return nil, fmt.Errorf("invalid update feed response")
	}
	return decodeReleaseFeed(ctx, httpClient, body)
}

func decodeReleaseFeed(ctx context.Context, httpClient *http.Client, raw []byte) (*Release, error) {
	var probe struct {
		TagName    string          `json:"tag_name"`
		Version    string          `json:"version"`
		Body       string          `json:"body"`
		Notes      string          `json:"notes"`
		HTMLURL    string          `json:"html_url"`
		URL        string          `json:"url"`
		Draft      bool            `json:"draft"`
		Prerelease bool            `json:"prerelease"`
		Assets     json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("decode update feed: %w", err)
	}
	if probe.Draft {
		return nil, fmt.Errorf("update feed returned a draft release")
	}
	versionRaw := probe.Version
	if versionRaw == "" {
		versionRaw = probe.TagName
	}
	version, ok := NormalizeVersion(versionRaw)
	if !ok {
		return nil, fmt.Errorf("update feed has invalid version %q", versionRaw)
	}
	notes := probe.Notes
	if notes == "" {
		notes = probe.Body
	}
	pageURL := probe.HTMLURL
	if pageURL == "" {
		pageURL = probe.URL
	}
	rel := &Release{
		Version: version,
		Notes:   strings.TrimSpace(notes),
		URL:     strings.TrimSpace(pageURL),
		Assets:  map[string]Asset{},
	}

	trimmedAssets := bytes.TrimSpace(probe.Assets)
	var sumsURL string
	if len(trimmedAssets) > 0 && trimmedAssets[0] == '{' {
		var byName map[string]Asset
		if err := json.Unmarshal(trimmedAssets, &byName); err != nil {
			return nil, fmt.Errorf("decode feed assets map: %w", err)
		}
		for name, asset := range byName {
			asset.Name = name
			asset.SHA256 = normalizeSHA256(asset.SHA256)
			rel.Assets[name] = asset
			if name == "SHA256SUMS" {
				sumsURL = asset.URL
			}
		}
	} else if len(trimmedAssets) > 0 && trimmedAssets[0] == '[' {
		var ghAssets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			URL                string `json:"url"`
			Size               int64  `json:"size"`
			Digest             string `json:"digest"`
			SHA256             string `json:"sha256"`
		}
		if err := json.Unmarshal(trimmedAssets, &ghAssets); err != nil {
			return nil, fmt.Errorf("decode GitHub release assets: %w", err)
		}
		for _, item := range ghAssets {
			if item.Name == "" {
				continue
			}
			downloadURL := item.BrowserDownloadURL
			if downloadURL == "" {
				downloadURL = item.URL
			}
			sum := normalizeSHA256(item.SHA256)
			if sum == "" {
				sum = normalizeSHA256(strings.TrimPrefix(item.Digest, "sha256:"))
			}
			rel.Assets[item.Name] = Asset{
				Name:   item.Name,
				URL:    downloadURL,
				Size:   item.Size,
				SHA256: sum,
			}
			if item.Name == "SHA256SUMS" {
				sumsURL = downloadURL
			}
		}
	}

	needSums := false
	for name, asset := range rel.Assets {
		if (strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, "-components.json")) && asset.SHA256 == "" {
			needSums = true
			break
		}
	}
	if needSums && sumsURL != "" {
		sums, err := fetchSHA256Sums(ctx, httpClient, sumsURL)
		if err == nil {
			for name, sum := range sums {
				if asset, exists := rel.Assets[name]; exists && asset.SHA256 == "" {
					asset.SHA256 = sum
					rel.Assets[name] = asset
				}
			}
		}
	}
	return rel, nil
}

func normalizeSHA256(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(s); err != nil {
		return ""
	}
	return s
}

func fetchSHA256Sums(ctx context.Context, httpClient *http.Client, sumsURL string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Fennec-Updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SHA256SUMS HTTP %d", resp.StatusCode)
	}
	out := map[string]string{}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 256<<10))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		sum := normalizeSHA256(fields[0])
		name := strings.TrimPrefix(fields[1], "*")
		if sum != "" && name != "" {
			out[name] = sum
		}
	}
	return out, scanner.Err()
}
