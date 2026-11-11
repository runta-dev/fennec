// check-english enforces English source/docs while retaining explicit Unicode
// fixtures and validating every static UI message reference against the catalog.
package main

import (
	"bytes"
	"fennec/internal/localization"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

var fixtures = map[string]string{
	"internal/browserterminal/server_test.go": "terminal UTF-8 output",
	"internal/cli/fen_sessions_test.go":       "Unicode titles and truncation",
	"internal/cli/ui_copy_test.go":            "locale punctuation and Unicode translation fixtures",
	"internal/localssh/alias_test.go":         "non-ASCII device aliases",
	"internal/setup/migrate_test.go":          "credential JSON Unicode round trips",
	"internal/localization/catalog_test.go":   "translation catalog fixtures",
	"internal/localization/native_test.go":    "native locale and fallback fixtures",
	"cmd/check-english/main_test.go":          "language-policy fixtures",
	"scripts/tests/session-status.cjs":        "Unicode user-authored session titles",
	"scripts/tests/terminal-bridge.cjs":       "UTF-8 split across terminal frames",
}

func sourceLanguage(path string, data []byte) error {
	if path == "native/macos/UI/translations.json" || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil
	}
	if fixtures[path] != "" {
		if filepath.Ext(path) == ".go" {
			var s scanner.Scanner
			fset := token.NewFileSet()
			f := fset.AddFile(path, -1, len(data))
			s.Init(f, data, nil, scanner.ScanComments)
			for {
				pos, kind, literal := s.Scan()
				if kind == token.EOF {
					break
				}
				if kind != token.STRING && kind != token.CHAR && strings.IndexFunc(literal, localization.IsCJK) >= 0 {
					return fmt.Errorf("%s: non-English code/comment outside a Unicode fixture", fset.Position(pos))
				}
			}
			return nil
		}
		// CJS fixtures may contain Unicode strings, but their code/comments remain English.
		for i := 0; i < len(data); i++ {
			if bytes.HasPrefix(data[i:], []byte("//")) || bytes.HasPrefix(data[i:], []byte("/*")) {
				end := len(data)
				if data[i+1] == '/' {
					if n := bytes.IndexByte(data[i:], '\n'); n >= 0 {
						end = i + n
					}
				} else if n := bytes.Index(data[i+2:], []byte("*/")); n >= 0 {
					end = i + 2 + n + 2
				}
				if strings.IndexFunc(string(data[i:end]), localization.IsCJK) >= 0 {
					return fmt.Errorf("%s: non-English fixture comment", path)
				}
				i = end - 1
				continue
			}
			if data[i] == '\'' || data[i] == '"' || data[i] == '`' {
				q := data[i]
				i++
				for i < len(data) {
					if data[i] == '\\' {
						i += 2
						continue
					}
					if data[i] == q {
						break
					}
					i++
				}
				continue
			}
			if data[i] >= utf8.RuneSelf {
				r, n := utf8.DecodeRune(data[i:])
				if localization.IsCJK(r) {
					return fmt.Errorf("%s: non-English code/comment outside a Unicode fixture", path)
				}
				i += n - 1
			}
		}
		return nil
	}
	if index := strings.IndexFunc(string(data), localization.IsCJK); index >= 0 {
		return fmt.Errorf("%s:%d: move translated copy to the catalog or write source/documentation in English", path, bytes.Count(data[:index], []byte{'\n'})+1)
	}
	return nil
}

var referencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\[\[i18n:([a-zA-Z][a-zA-Z0-9_.]*)\]\]`),
	regexp.MustCompile(`data-i18n(?:-(?:aria-label|placeholder|title))?="([a-zA-Z][a-zA-Z0-9_.]*)"`),
	regexp.MustCompile(`setAttribute\(['"]data-i18n['"],\s*['"]([a-zA-Z][a-zA-Z0-9_.]*)['"]\)`),
	regexp.MustCompile(`text\("(installation\.[a-zA-Z0-9_.]+)"\)`),
}

func references(path string, data []byte, c localization.Catalog) error {
	if !strings.HasPrefix(path, "native/macos/") {
		return nil
	}
	keys := []string{}
	for _, pattern := range referencePatterns {
		for _, m := range pattern.FindAllSubmatch(data, -1) {
			keys = append(keys, string(m[1]))
		}
	}
	if strings.HasSuffix(path, ".swift") {
		for _, key := range regexp.MustCompile(`"(permission\.[a-zA-Z0-9_.]+)"`).FindAllSubmatch(data, -1) {
			keys = append(keys, string(key[1]))
		}
	}
	for _, key := range keys {
		if _, ok := c.Messages[key]; !ok {
			return fmt.Errorf("%s: unknown localization key %s", path, key)
		}
	}
	return nil
}
func run() error {
	raw, err := os.ReadFile("native/macos/UI/translations.json")
	if err != nil {
		return err
	}
	catalog, err := localization.Decode(raw)
	if err != nil {
		return err
	}
	files, err := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return err
	}
	for _, path := range strings.Split(string(files), "\x00") {
		if path == "" {
			continue
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = sourceLanguage(path, data); err != nil {
			return err
		}
		if err = references(path, data, catalog); err != nil {
			return err
		}
	}
	fmt.Println("PASS English source/docs, explicit Unicode fixtures, and localization references")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
