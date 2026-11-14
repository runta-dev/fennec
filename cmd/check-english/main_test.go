package main

import (
	"fennec/internal/localization"
	"testing"
)

func TestSourceLanguagePolicy(t *testing.T) {
	for _, path := range []string{"AGENTS.md", "TODO.md", "app.swift", "app.js"} {
		if sourceLanguage(path, []byte("// 中文注释")) == nil {
			t.Fatal("unlocalized source accepted", path)
		}
	}
	if err := sourceLanguage("native/macos/UI/translations.json", []byte(`{"zh-CN":"中文"}`)); err != nil {
		t.Fatal(err)
	}
	if err := sourceLanguage("internal/cli/fen_sessions_test.go", []byte("package cli\nvar title = \"中文标题\"\n")); err != nil {
		t.Fatal(err)
	}
	if sourceLanguage("internal/cli/fen_sessions_test.go", []byte("package cli\n// 中文注释\nvar title = \"中文标题\"\n")) == nil {
		t.Fatal("fixture exception allowed non-English comments")
	}
}
func TestUnknownMessageReferenceFails(t *testing.T) {
	c := localization.Catalog{Version: 1, Messages: map[string]map[string]string{"ui.retry": {"en-US": "Retry"}}}
	if references("native/macos/UI/panel.js", []byte(`button.textContent='[[i18n:ui.missing]]'`), c) == nil {
		t.Fatal("missing key accepted")
	}
	if err := references("native/macos/UI/index.html", []byte(`<button data-i18n="ui.retry">Retry</button>`), c); err != nil {
		t.Fatal(err)
	}
}

func TestLocalizedReadmeDoesNotExemptOtherDocumentation(t *testing.T) {
	link := []byte(`<a href="docs/README_ZH.md" lang="zh-CN">简体中文</a>`)
	if err := sourceLanguage("README.md", link); err != nil {
		t.Fatal("native language selector rejected", err)
	}
	if err := sourceLanguage("docs/README_ZH.md", []byte("# 认识 Fennec\n简体中文使用指南")); err != nil {
		t.Fatal("translated user guide rejected", err)
	}
	for _, path := range []string{"AGENTS.md", "docs/developer.md", "docs/README_ZH.go"} {
		if sourceLanguage(path, link) == nil {
			t.Fatal("selector exemption escaped the README", path)
		}
	}
	if sourceLanguage("README.md", append(link, []byte("\n其他中文正文")...)) == nil {
		t.Fatal("selector exemption allowed translated English README prose")
	}
}
