package localization

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestNativeBundleCatalogAndCompiledFallback(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Foundation bundle loading requires macOS")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	contents := filepath.Join(temp, "Localization.app/Contents")
	binary := filepath.Join(contents, "MacOS/Probe")
	resource := filepath.Join(contents, "Resources/UI/translations.json")
	for _, dir := range []string{filepath.Dir(binary), filepath.Dir(resource)} {
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "native/macos/UI/translations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(resource, raw, 0644); err != nil {
		t.Fatal(err)
	}
	info := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.runta.fennec.localization-test</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleExecutable</key><string>Probe</string></dict></plist>`
	if err = os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(info), 0644); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(temp, "English.swift")
	generator := exec.Command("go", "run", "./cmd/build-locales", resource, filepath.Join(temp, "catalog.js"), fallback)
	generator.Dir = root
	if output, err := generator.CombinedOutput(); err != nil {
		t.Fatalf("generate fallback: %v %s", err, output)
	}
	driver := filepath.Join(temp, "Probe.swift")
	source := `import Foundation
@main struct Probe { static func main() throws { let language=CommandLine.arguments[1];let values=["permission.allow","ui.settings","installation.install_here"].map{L10n.text($0,language:language)};let data=try JSONSerialization.data(withJSONObject:values);print(String(data:data,encoding:.utf8)!)} }
`
	if err = os.WriteFile(driver, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	compile := exec.Command("swiftc", filepath.Join(root, "native/macos/Localization.swift"), fallback, driver, "-o", binary)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile native localization: %v %s", err, output)
	}
	check := func(language string, want []string) {
		t.Helper()
		output, err := exec.Command(binary, language).CombinedOutput()
		if err != nil {
			t.Fatalf("native locale %s: %v %s", language, err, output)
		}
		var got []string
		if json.Unmarshal(output, &got) != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("native locale %s: got %q want %q", language, got, want)
		}
	}
	english := []string{"Allow once", "Settings", "Install Here"}
	check("en-US", english)
	check("en-GB", english)
	check("zh-CN", []string{"允许一次", "设置", "安装到此处"})
	check("zh-Hant", []string{"允許一次", "設定", "安装到此处"})
	check("ja", []string{"今回のみ許可", "設定", "Install Here"})
	check("ko", []string{"한 번 허용", "설정", "Install Here"})
	if err = os.Rename(resource, resource+".saved"); err != nil {
		t.Fatal(err)
	}
	check("zh-CN", english)
}
