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
	for _, path := range []string{"internal/cli/fen_sessions_test.go", "internal/transcript/transcript_test.go"} {
		if err := sourceLanguage(path, []byte("package fixture\nvar title = \"中文标题\"\n")); err != nil {
			t.Fatal(err)
		}
		if sourceLanguage(path, []byte("package fixture\n// 中文注释\nvar title = \"中文标题\"\n")) == nil {
			t.Fatal("fixture exception allowed non-English comments", path)
		}
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

func TestSwiftUnicodeFixturePolicy(t *testing.T) {
	const path = "ios/FennecTests/FennecTests.swift"
	if err := sourceLanguage(path, []byte("let title = \"中文标题\"\n")); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"// 中文注释\n", "/* 中文注释 */\n", "let 中文 = 1\n"} {
		if sourceLanguage(path, []byte(source)) == nil {
			t.Fatal("fixture allowed non-English code/comment")
		}
	}
}
