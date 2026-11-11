package uninstall

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type LocalPlan struct {
	Home       string   `json:"home"`
	App        string   `json:"app"`
	Apps       []string `json:"apps"`
	Data       []string `json:"data"`
	ShellFiles []string `json:"shell_files"`
	Entries    []string `json:"entries"`
}

var localData = []string{
	".config/fennec", ".local/share/fennec",
	"Library/Caches/com.runta.fennec", "Library/WebKit/com.runta.fennec",
	"Library/Application Support/com.runta.fennec", "Library/Saved Application State/com.runta.fennec.savedState",
	"Library/Preferences/com.runta.fennec.plist", "Library/LaunchAgents/com.runta.fennec.daemon.plist",
}

func BundleID(app string) (string, error) {
	info, err := os.Lstat(app)
	if err != nil || !info.IsDir() || !strings.HasSuffix(app, ".app") {
		return "", fmt.Errorf("Fennec application bundle required")
	}
	file, err := os.Open(filepath.Join(app, "Contents/Info.plist"))
	if err != nil {
		return "", err
	}
	defer file.Close()
	decoder := xml.NewDecoder(io.LimitReader(file, 1<<20))
	want := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			var key string
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return "", err
			}
			want = key == "CFBundleIdentifier"
		case "string":
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				return "", err
			}
			if want {
				return value, nil
			}
		}
	}
	return "", fmt.Errorf("bundle identifier missing")
}
func DiscoverLocal(home, executable string, entries []string, zdot, xdg string) (LocalPlan, error) {
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	plan := LocalPlan{Home: filepath.Clean(home), Entries: entries}
	if !filepath.IsAbs(home) || home == string(filepath.Separator) {
		return plan, fmt.Errorf("user home required")
	}
	if filepath.Base(filepath.Dir(executable)) != "MacOS" {
		return plan, fmt.Errorf("run uninstall from the installed Fennec app")
	}
	plan.App = filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	if id, err := BundleID(plan.App); err != nil || id != "com.runta.fennec" {
		return plan, fmt.Errorf("Fennec bundle identity could not be verified")
	}
	candidates := []string{plan.App, filepath.Join(home, "Applications/Fennec.app"), "/Applications/Fennec.app"}
	if registered := registeredApplication(home); registered != "" {
		candidates = append(candidates, registered)
	}
	// Development archives are scoped to the current app's build directory.
	caches := map[string]bool{}
	for _, candidate := range append([]string{}, candidates...) {
		if filepath.Base(filepath.Dir(candidate)) == "build" {
			module, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(candidate)), "go.mod"))
			if err == nil && bytes.HasPrefix(module, []byte("module fennec\n")) {
				matches, _ := filepath.Glob(filepath.Join(filepath.Dir(candidate), "Fennec*.app"))
				candidates = append(candidates, matches...)
				caches[filepath.Join(filepath.Dir(candidate), "credential-helper-cache")] = true
			}
		}
	}
	for cache := range caches {
		plan.Data = append(plan.Data, cache)
	}
	seen := map[string]bool{}
	for _, app := range candidates {
		if seen[app] {
			continue
		}
		seen[app] = true
		if id, err := BundleID(app); err == nil && id == "com.runta.fennec" {
			plan.Apps = append(plan.Apps, app)
		}
	}
	for _, relative := range localData {
		plan.Data = append(plan.Data, filepath.Join(home, relative))
	}
	for _, name := range []string{".zshrc", ".bashrc", ".bash_profile", ".bash_login", ".profile"} {
		plan.ShellFiles = append(plan.ShellFiles, filepath.Join(home, name))
	}
	if filepath.IsAbs(zdot) && filepath.Clean(zdot) != filepath.Clean(home) {
		plan.ShellFiles = append(plan.ShellFiles, filepath.Join(zdot, ".zshrc"))
	}
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(xdg) {
		return plan, fmt.Errorf("absolute shell config directory required")
	}
	plan.ShellFiles = append(plan.ShellFiles, filepath.Join(xdg, "fish/conf.d/fennec.fish"))
	sort.Strings(plan.Apps)
	sort.Strings(plan.Data)
	sort.Strings(plan.ShellFiles)
	sort.Strings(plan.Entries)
	return plan, nil
}

// Data removal is limited to the reviewed allowlist. Never follow a symlink in
// a parent directory or remove the repository containing a development bundle.
func removeLocalPaths(paths []string) error {
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
			return fmt.Errorf("invalid uninstall path")
		}
		parent := filepath.Dir(path)
		real, err := filepath.EvalSymlinks(parent)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if filepath.Clean(real) != filepath.Clean(parent) {
			return fmt.Errorf("uninstall parent is a symbolic link; contents preserved")
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func (p LocalPlan) RemoveData() error {
	if !filepath.IsAbs(p.Home) || filepath.Clean(p.Home) == "/" {
		return fmt.Errorf("user home required")
	}
	allowed := map[string]bool{}
	for _, relative := range localData {
		allowed[filepath.Join(p.Home, relative)] = true
	}
	for _, app := range p.Apps {
		if filepath.Base(filepath.Dir(app)) == "build" {
			module, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(app)), "go.mod"))
			if err == nil && bytes.HasPrefix(module, []byte("module fennec\n")) {
				allowed[filepath.Join(filepath.Dir(app), "credential-helper-cache")] = true
			}
		}
	}
	for _, path := range p.Data {
		if !allowed[path] {
			return fmt.Errorf("uninstall data path is outside Fennec's allowlist")
		}
	}
	return removeLocalPaths(p.Data)
}
func RemoveLocalApps(apps []string) error {
	for _, app := range apps {
		if _, err := os.Lstat(app); os.IsNotExist(err) {
			continue
		}
		id, err := BundleID(app)
		if err != nil || id != "com.runta.fennec" {
			return fmt.Errorf("application changed since uninstall preview; preserved")
		}
		if err := os.RemoveAll(app); err != nil {
			return err
		}
	}
	return nil
}

// A DMG or second app copy may not be the bundle that owns the running daemon.
// Include the registered bundle so uninstall cannot leave it recreating state.
func registeredApplication(home string) string {
	file, err := os.Open(filepath.Join(home, "Library/LaunchAgents/com.runta.fennec.daemon.plist"))
	if err != nil {
		return ""
	}
	defer file.Close()
	decoder := xml.NewDecoder(io.LimitReader(file, 1<<20))
	key, label, binary := "", "", ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			if decoder.DecodeElement(&key, &start) != nil {
				return ""
			}
		case "string":
			var value string
			if decoder.DecodeElement(&value, &start) != nil {
				return ""
			}
			if key == "Label" {
				label = value
			}
		case "array":
			if key == "ProgramArguments" {
				var values struct {
					Strings []string `xml:"string"`
				}
				if decoder.DecodeElement(&values, &start) != nil {
					return ""
				}
				if len(values.Strings) > 0 {
					binary = values.Strings[0]
				}
			}
		}
	}
	if label != "com.runta.fennec.daemon" || !filepath.IsAbs(binary) || filepath.Base(filepath.Dir(binary)) != "MacOS" {
		return ""
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(binary)))
	if id, err := BundleID(app); err != nil || id != "com.runta.fennec" {
		return ""
	}
	return app
}
