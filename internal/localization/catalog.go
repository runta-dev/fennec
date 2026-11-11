// Package localization validates the shared build-time UI catalog.
package localization

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

type Catalog struct {
	Version  int                          `json:"version"`
	Messages map[string]map[string]string `json:"messages"`
}

var MessageID = regexp.MustCompile(`^[a-z][a-zA-Z0-9_.]+$`)
var locales = map[string]bool{"en-US": true, "en-GB": true, "zh-CN": true, "zh-TW": true, "ja": true, "ko": true}

func IsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r)
}
func Decode(raw []byte) (Catalog, error) {
	var c Catalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF || c.Version != 1 || len(c.Messages) == 0 {
		return c, fmt.Errorf("invalid localization catalog")
	}
	for key, row := range c.Messages {
		if !MessageID.MatchString(key) || strings.TrimSpace(row["en-US"]) == "" || strings.IndexFunc(row["en-US"], IsCJK) >= 0 {
			return c, fmt.Errorf("message %q requires an English ID and fallback", key)
		}
		for locale, value := range row {
			if !locales[locale] || strings.TrimSpace(value) == "" {
				return c, fmt.Errorf("message %q has an invalid locale or empty translation", key)
			}
		}
	}
	return c, nil
}
