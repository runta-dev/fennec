// build-locales generates the WebView payload and compiled native English
// fallback from one catalog. It runs only while building the application.
package main

import (
	"bytes"
	"encoding/json"
	"fennec/internal/localization"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func generate(input []byte) ([]byte, []byte, error) {
	c, err := localization.Decode(input)
	if err != nil {
		return nil, nil, err
	}
	keys := []string{}
	for key := range c.Messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	data, err := json.Marshal(c.Messages)
	if err != nil {
		return nil, nil, err
	}
	js := append([]byte("// Generated from translations.json; do not edit.\nwindow.FennecMessages = "), data...)
	js = append(js, ';', '\n')
	var swift bytes.Buffer
	swift.WriteString("// Generated English fallback; do not edit.\nenum FennecEnglish {\n    static let messages: [String: String] = [\n")
	quote := func(value string) string {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		_ = e.Encode(value)
		return regexp.MustCompile(`\\u([0-9a-fA-F]{4})`).ReplaceAllString(strings.TrimSpace(b.String()), `\u{$1}`)
	}
	for _, key := range keys {
		fmt.Fprintf(&swift, "        %s: %s,\n", quote(key), quote(c.Messages[key]["en-US"]))
	}
	swift.WriteString("    ]\n}\n")
	return js, swift.Bytes(), nil
}
func run(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("usage: build-locales <catalog.json> <web.js> <English.swift>")
	}
	raw, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	js, swift, err := generate(raw)
	if err != nil {
		return err
	}
	for i, data := range [][]byte{js, swift} {
		path := args[i+1]
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err = os.WriteFile(path, data, 0644); err != nil {
			return err
		}
	}
	return nil
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
