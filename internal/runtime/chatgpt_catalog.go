package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// magpieCatalogName is the Codex model catalog Magpie writes next to Codex's
// config on the Mac (~/.codex/magpie-models.json, referenced by
// model_catalog_json).
const magpieCatalogName = "magpie-models.json"

// magpieCatalogURL is the Magpie gateway's Codex model catalog: the model
// list Magpie serves in Codex's model-info shape (display names, reasoning
// efforts, context windows, base instructions, ordering). It only lists
// models and never sends an inference request.
var magpieCatalogURL = "http://127.0.0.1:3425/v1/codex/models"

// codexClientVersion reports the installed Codex version the catalog is
// requested for; the gateway leaves out models older clients cannot use.
var codexClientVersion = installedCodexVersion

const magpieCatalogLimit = 32 << 20

// magpieCatalogRefresh is how often ChatGPT connections ask the gateway for
// the catalog. Every ChatGPT connection prepares its server, and fetching and
// laying out the catalog (about 2 MB) on each would add tens of milliseconds
// to every connection for a list that changes only when Magpie's models do.
var magpieCatalogRefresh = time.Minute

var catalogRefreshes struct {
	sync.Mutex
	last map[string]time.Time
}

// refreshMagpieCatalog syncs the catalog at path at most once per
// magpieCatalogRefresh while one is in place; without one every call asks
// the gateway.
func refreshMagpieCatalog(ctx context.Context, path string) bool {
	catalogRefreshes.Lock()
	last := catalogRefreshes.last[path]
	catalogRefreshes.Unlock()
	if time.Since(last) < magpieCatalogRefresh {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	if !syncMagpieCatalog(ctx, path) {
		return false
	}
	catalogRefreshes.Lock()
	defer catalogRefreshes.Unlock()
	if catalogRefreshes.last == nil {
		catalogRefreshes.last = map[string]time.Time{}
	}
	catalogRefreshes.last[path] = time.Now()
	return true
}

// syncMagpieCatalog writes Magpie's Codex model catalog to path the way Magpie
// does on the Mac, so ChatGPT running with the magpie provider shows Magpie's
// models by name instead of "Custom". The file changes only when the catalog
// does; an unavailable gateway keeps the last catalog. It reports whether a
// catalog is in place.
func syncMagpieCatalog(ctx context.Context, path string) bool {
	if version := codexClientVersion(); version != "" {
		if catalog, err := fetchMagpieCatalog(ctx, version); err == nil {
			if existing, err := os.ReadFile(path); err != nil || !bytes.Equal(existing, catalog) {
				if writeFileAtomic(path, catalog) != nil {
					return false
				}
			}
			return true
		}
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func fetchMagpieCatalog(ctx context.Context, version string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, magpieCatalogURL+"?"+url.Values{"client_version": {version}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errInvalidCatalog
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, magpieCatalogLimit+1))
	if err != nil {
		return nil, err
	}
	return formatMagpieCatalog(raw)
}

type catalogError string

func (e catalogError) Error() string { return string(e) }

const errInvalidCatalog = catalogError("invalid Magpie model catalog")

// formatMagpieCatalog checks the gateway's catalog and lays it out as Magpie
// writes magpie-models.json (Go's escaped JSON, one-space indent), keeping the
// gateway's field and model order.
func formatMagpieCatalog(raw []byte) ([]byte, error) {
	if len(raw) > magpieCatalogLimit {
		return nil, errInvalidCatalog
	}
	var catalog struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &catalog) != nil || len(catalog.Models) == 0 {
		return nil, errInvalidCatalog
	}
	for _, model := range catalog.Models {
		if model.Slug == "" {
			return nil, errInvalidCatalog
		}
	}
	var escaped, formatted bytes.Buffer
	json.HTMLEscape(&escaped, bytes.TrimSpace(raw))
	if err := json.Indent(&formatted, escaped.Bytes(), "", " "); err != nil {
		return nil, err
	}
	return formatted.Bytes(), nil
}

// installedCodexVersion reads the version of the codex found on PATH from its
// npm package, without starting codex.
func installedCodexVersion() string {
	command, err := exec.LookPath("codex")
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(command)
	if err != nil {
		return ""
	}
	directory := filepath.Dir(resolved)
	for range 6 {
		var manifest struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		data, err := os.ReadFile(filepath.Join(directory, "package.json"))
		if err == nil && json.Unmarshal(data, &manifest) == nil && manifest.Name == "@openai/codex" && validVersion(manifest.Version) {
			return manifest.Version
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	return ""
}

func validVersion(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	return strings.Trim(version, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.-+") == ""
}

// catalogSignature identifies the model catalog an app-server loaded, so a
// changed catalog restarts Fennec's ChatGPT server on the next connection.
func catalogSignature(codexHome string) string {
	data, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		return ""
	}
	var config struct {
		Catalog string `toml:"model_catalog_json"`
	}
	if toml.Unmarshal(data, &config) != nil || config.Catalog == "" {
		return ""
	}
	info, err := os.Stat(config.Catalog)
	if err != nil {
		return ""
	}
	// The catalog is replaced by rename, so an unchanged file keeps its
	// identity, size and time and its digest is not computed again.
	catalogDigests.Lock()
	known := catalogDigests.entries[config.Catalog]
	catalogDigests.Unlock()
	if known.info != nil && os.SameFile(known.info, info) && known.info.Size() == info.Size() && known.info.ModTime().Equal(info.ModTime()) {
		return known.signature
	}
	catalog, err := os.ReadFile(config.Catalog)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(catalog)
	signature := "model_catalog_json sha256:" + hex.EncodeToString(digest[:])
	if after, err := os.Stat(config.Catalog); err == nil && os.SameFile(after, info) && after.Size() == int64(len(catalog)) && after.ModTime().Equal(info.ModTime()) {
		catalogDigests.Lock()
		if catalogDigests.entries == nil {
			catalogDigests.entries = map[string]catalogDigest{}
		}
		catalogDigests.entries[config.Catalog] = catalogDigest{info: after, signature: signature}
		catalogDigests.Unlock()
	}
	return signature
}

type catalogDigest struct {
	info      os.FileInfo
	signature string
}

var catalogDigests struct {
	sync.Mutex
	entries map[string]catalogDigest
}
