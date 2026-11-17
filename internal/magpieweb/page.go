package magpieweb

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"net/http"
	"strconv"
	"strings"
)

//go:embed loading.html
var panelLoading []byte

func preparePanelResponse(response *http.Response) error {
	path := response.Request.URL.Path
	// Preserve Magpie's content-hashed static asset caching. Adding no-store
	// to every response defeats immutable caching and retransfers all scripts.
	if strings.HasPrefix(path, "/api/") {
		response.Header.Set("Cache-Control", "no-store")
		return nil
	}
	if path != "/" || response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	var reader io.Reader = response.Body
	if response.Header.Get("Content-Encoding") == "gzip" {
		decoded, err := gzip.NewReader(response.Body)
		if err != nil {
			return err
		}
		defer decoded.Close()
		reader = decoded
	} else if response.Header.Get("Content-Encoding") != "" {
		return nil
	}
	body, err := io.ReadAll(reader)
	response.Body.Close()
	if err != nil {
		return err
	}
	// This runs before the upstream parser-blocking scripts, without changing
	// their execution order or replacing Magpie's application UI.
	body = bytes.Replace(body, []byte("<head>"), append([]byte("<head>\n"), panelLoading...), 1)
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Del("Content-Encoding")
	response.Header.Del("ETag")
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Set("Cache-Control", "no-store")
	return nil
}
