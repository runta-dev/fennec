package localization

import "testing"

func TestCatalogRequiresEnglishIDsAndFallbacks(t *testing.T) {
	for _, raw := range []string{`{"version":1,"messages":{"重试":{"en-US":"Retry"}}}`, `{"version":1,"messages":{"ui.retry":{"en-US":"重试"}}}`, `{"version":1,"messages":{"ui.retry":{"zh-CN":"重试"}}}`, `{"version":1,"messages":{"ui.retry":{"en-US":"Retry","中文":"重试"}}}`} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
	c, err := Decode([]byte(`{"version":1,"messages":{"ui.retry":{"en-US":"Retry","zh-CN":"重试","ja":"再試行"}}}`))
	if err != nil || c.Messages["ui.retry"]["zh-CN"] != "重试" {
		t.Fatal(c, err)
	}
}
