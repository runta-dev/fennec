package preview

import (
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// A page's local files (src="chart.png", href="data.csv") are embedded
// into its snapshot as data: URLs, so the page shows the same anywhere,
// though a snapshot is one file. Only files inside the workspace are read
// (through its os.Root); remote URLs, absolute paths and anchors are left alone.
var pageReference = regexp.MustCompile(`(?i)\b(src|href)(\s*=\s*)("([^"]*)"|'([^']*)')`)

const maxInlinedFile = 16 << 20

func isPage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
		return true
	}
	return false
}

func inlinePage(workspace *os.Root, page string, source []byte) []byte {
	directory := filepath.Dir(page)
	total := len(source)
	return pageReference.ReplaceAllFunc(source, func(match []byte) []byte {
		parts := pageReference.FindSubmatch(match)
		attribute, equals := string(parts[1]), string(parts[2])
		value := string(parts[4])
		if len(parts[5]) > 0 {
			value = string(parts[5])
		}
		value = html.UnescapeString(value)
		if value == "" || strings.Contains(value, ":") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "#") {
			return match
		}
		if i := strings.IndexAny(value, "?#"); i >= 0 {
			value = value[:i]
		}
		target := filepath.Join(directory, filepath.FromSlash(value))
		if !filepath.IsLocal(target) {
			return match
		}
		info, err := workspace.Stat(target)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxInlinedFile || int64(total)+info.Size()*4/3 > MaxBytes {
			return match
		}
		file, err := workspace.Open(target)
		if err != nil {
			return match
		}
		data, err := io.ReadAll(io.LimitReader(file, maxInlinedFile+1))
		file.Close()
		if err != nil || len(data) > maxInlinedFile {
			return match
		}
		kind := mime.TypeByExtension(path.Ext(value))
		if kind == "" {
			kind = http.DetectContentType(data)
		}
		total += len(data) * 4 / 3
		replaced := fmt.Sprintf(`%s%s"data:%s;base64,%s"`, attribute, equals, kind, base64.StdEncoding.EncodeToString(data))
		// A link to a file (a CSV) downloads it under its own name.
		if strings.EqualFold(attribute, "href") && !strings.HasPrefix(kind, "text/css") && !strings.HasPrefix(kind, "text/html") {
			replaced += fmt.Sprintf(` download="%s"`, html.EscapeString(filepath.Base(target)))
		}
		return []byte(replaced)
	})
}
