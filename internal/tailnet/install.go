package tailnet

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"
)

// packages is Tailscale's stable package index; tests point it elsewhere.
var packages = "https://pkgs.tailscale.com/stable/"

// install puts the static tailscale and tailscaled binaries in place, checked
// against the SHA-256 Tailscale publishes next to each tarball.
func install(ctx context.Context, p paths) error {
	if p.installed() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{}
	get := func(url string, limit int64) ([]byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", url, response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err == nil && int64(len(data)) > limit {
			err = fmt.Errorf("%s: response too large", url)
		}
		return data, err
	}
	raw, err := get(packages+"?mode=json", 1<<20)
	if err != nil {
		return err
	}
	var index struct {
		Tarballs map[string]string
	}
	if err = json.Unmarshal(raw, &index); err != nil {
		return err
	}
	name := index.Tarballs[goruntime.GOARCH]
	if name == "" || name != path.Base(name) || !strings.HasSuffix(name, ".tgz") {
		return fmt.Errorf("no Tailscale build for %s", goruntime.GOARCH)
	}
	sum, err := get(packages+name+".sha256", 1024)
	if err != nil {
		return err
	}
	want := strings.TrimSpace(strings.Fields(string(sum) + " ")[0])
	archive, err := get(packages+name, 256<<20)
	if err != nil {
		return err
	}
	got := sha256.Sum256(archive)
	if len(want) != 64 || !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return fmt.Errorf("checksum mismatch for %s", name)
	}
	staging := p.bin() + ".tmp"
	os.RemoveAll(staging)
	if err = os.MkdirAll(staging, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err = extract(archive, staging); err != nil {
		return err
	}
	version := strings.TrimSuffix(strings.TrimPrefix(name, "tailscale_"), "_"+goruntime.GOARCH+".tgz")
	if err = os.WriteFile(filepath.Join(staging, "VERSION"), []byte(version+"\n"), 0644); err != nil {
		return err
	}
	os.RemoveAll(p.bin())
	if err = os.MkdirAll(filepath.Dir(p.bin()), 0755); err != nil {
		return err
	}
	return os.Rename(staging, p.bin())
}

// extract copies only the two binaries out of the tarball.
func extract(archive []byte, dir string) error {
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	reader := tar.NewReader(compressed)
	found := 0
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		base := path.Base(header.Name)
		if header.Typeflag != tar.TypeReg || (base != "tailscale" && base != "tailscaled") || path.Base(path.Dir(header.Name)) == "systemd" {
			continue
		}
		file, err := os.OpenFile(filepath.Join(dir, base), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
		if err != nil {
			return err
		}
		_, err = io.Copy(file, io.LimitReader(reader, 512<<20))
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		found++
	}
	if found != 2 {
		return fmt.Errorf("tailscale binaries missing from the package")
	}
	return nil
}

func installedVersion(p paths) string {
	data, _ := os.ReadFile(filepath.Join(p.bin(), "VERSION"))
	return strings.TrimSpace(string(data))
}
