package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUICopyPunctuationPreservesTechnicalDots(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "native/macos/UI/locale.js"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start, end := strings.Index(source, "function uiCopy("), strings.Index(source, "function translate(")
	if start < 0 || end <= start {
		t.Fatal("UI copy formatter unavailable")
	}
	cases := []map[string]string{{"input": "为云端智能体自由选择模型。", "language": "zh-CN"}, {"input": "Could not connect. Try again.", "language": "en-US"}, {"input": "Fennec 0.2.123 · config.toml · https://example.com/a.json", "language": "en-US"}, {"input": "Connecting...", "language": "en-US"}, {"input": "前半。後半。", "language": "ja"}}
	raw, _ := json.Marshal(cases)
	script := source[start:end] + `process.stdout.write(JSON.stringify(JSON.parse(process.argv[1]).map(x=>uiCopy(x.input,x.language))))`
	output, err := exec.Command("node", "-e", script, string(raw)).CombinedOutput()
	if err != nil {
		t.Fatalf("UI renderer check: %v %s", err, output)
	}
	var got []string
	if json.Unmarshal(output, &got) != nil {
		t.Fatal("invalid UI renderer result")
	}
	want := []string{"为云端智能体自由选择模型", "Could not connect · Try again", "Fennec 0.2.123 · config.toml · https://example.com/a.json", "Connecting…", "前半、後半"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected UI copy: %#v", got)
	}
}

func TestUITranslationDoesNotInterpretPrototypeNames(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "native/macos/UI/locale.js"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start, end := strings.Index(source, "function uiCopy("), strings.Index(source, "function localize(")
	if start < 0 || end <= start {
		t.Fatal("translation functions unavailable")
	}
	script := `const translations={'测试。':['測試。','Test.','テスト。','테스트.']};let language='en-US';` + source[start:end] + `process.stdout.write(JSON.stringify(['constructor','__proto__','model.json','测试。'].map(translate)))`
	raw, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("translation check: %v %s", err, raw)
	}
	var values []string
	_ = json.Unmarshal(raw, &values)
	if !reflect.DeepEqual(values, []string{"constructor", "__proto__", "model.json", "Test"}) {
		t.Fatal(values)
	}
}
