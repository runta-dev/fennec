// export-website-ui packages the shipping WebView unchanged, with an isolated
// browser-only data bridge. No daemon, account, or cloud connection is included.
package main

import (
	"encoding/json"
	"fennec/internal/localization"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := export(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func export(root string) error {
	source := filepath.Join(root, "native/macos/UI")
	dest := filepath.Join(root, "website/public/demo-native")
	copyFile := func(from, to string) error {
		data, e := os.ReadFile(from)
		if e != nil {
			return e
		}
		if e = os.MkdirAll(filepath.Dir(to), 0755); e != nil {
			return e
		}
		return os.WriteFile(to, data, 0644)
	}
	raw, err := os.ReadFile(filepath.Join(source, "index.html"))
	if err != nil {
		return err
	}
	// Retain the production CSP and external scripts. The fixture bridge must run
	// first, before the original UI tries to talk to WebKit.
	html := strings.Replace(string(raw), "<head>", `<head><script src="demo-bridge.js"></script>`, 1)
	html = strings.Replace(html, "</head>", `<link rel="stylesheet" href="demo-viewport.css"></head>`, 1)
	for _, match := range regexp.MustCompile(`(?:src|href)="([^"]+\.(?:js|css))"`).FindAllStringSubmatch(string(raw), -1) {
		name := match[1]
		if name == "locale-data.js" {
			continue
		}
		if err = copyFile(filepath.Join(source, name), filepath.Join(dest, name)); err != nil {
			return err
		}
	}
	if err = filepath.WalkDir(filepath.Join(source, "icons"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(source, path)
		if e != nil {
			return e
		}
		return copyFile(path, filepath.Join(dest, rel))
	}); err != nil {
		return err
	}
	catalogRaw, err := os.ReadFile(filepath.Join(source, "translations.json"))
	if err != nil {
		return err
	}
	catalog, err := localization.Decode(catalogRaw)
	if err != nil {
		return err
	}
	// The public English walkthrough needs only the English fallback. The native
	// locale engine still performs all token substitution and custom-menu labels.
	for key, row := range catalog.Messages {
		catalog.Messages[key] = map[string]string{"en-US": row["en-US"]}
	}
	data, err := json.Marshal(catalog.Messages)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dest, "locale-data.js"), append(append([]byte("window.FennecMessages = "), data...), ';'), 0644); err != nil {
		return err
	}
	for from, to := range map[string]string{"native-bridge.js": "demo-bridge.js", "native-viewport.css": "demo-viewport.css"} {
		if err = copyFile(filepath.Join(root, "website/demo", from), filepath.Join(dest, to)); err != nil {
			return err
		}
	}
	for _, name := range []string{"xterm-LICENSE", "addon-fit-LICENSE"} {
		if err = copyFile(filepath.Join(source, "vendor", name), filepath.Join(dest, "vendor", name)); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dest, "index.html"), []byte(html), 0644)
}
