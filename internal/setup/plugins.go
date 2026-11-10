package setup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
)

type ModelPlugin struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

var modelPluginAllowlist = map[string]bool{"@magpie-community/opencode-devin-auth": true, "@magpie-community/opencode-grok-auth": true}

// DesiredModelPlugins reads installation metadata, never plugin credential data.
// Only explicitly configured model plugins are copied; versions come from the
// installed package rather than a floating @latest spec.
func DesiredModelPlugins(home string) ([]ModelPlugin, error) {
	root := filepath.Join(home, ".config", "magpie")
	data, err := os.ReadFile(filepath.Join(root, "plugins.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config struct {
		Plugins []struct {
			Spec string `json:"spec"`
		} `json:"plugins"`
	}
	if json.Unmarshal(data, &config) != nil {
		return nil, fmt.Errorf("invalid local plugin manifest")
	}
	wanted := map[string]bool{}
	for _, entry := range config.Plugins {
		for pkg := range modelPluginAllowlist {
			if entry.Spec == pkg || strings.HasPrefix(entry.Spec, pkg+"@") {
				wanted[pkg] = true
			}
		}
	}
	found := map[string]string{}
	err = filepath.WalkDir(filepath.Join(root, "plugins"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "package.json" {
			return nil
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &pkg) != nil || !wanted[pkg.Name] {
			return nil
		}
		if !exactVersion.MatchString(pkg.Version) {
			return fmt.Errorf("model plugin has no exact version")
		}
		if old := found[pkg.Name]; old != "" && old != pkg.Version {
			return fmt.Errorf("multiple installed versions of model plugin %s", pkg.Name)
		}
		found[pkg.Name] = pkg.Version
		return nil
	})
	if len(wanted) == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plugins := []ModelPlugin{}
	for pkg := range wanted {
		version := found[pkg]
		if version == "" {
			return nil, fmt.Errorf("configured model plugin %s is not installed locally", pkg)
		}
		plugins = append(plugins, ModelPlugin{pkg, version})
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].Package < plugins[j].Package })
	return plugins, nil
}

// ReconcileModelPlugins runs on every preparation, checks actual installed
// package metadata and configuration, and repairs only missing/different specs.
func ReconcileModelPlugins(ctx context.Context, client *ssh.Client, plugins []ModelPlugin) error {
	for _, p := range plugins {
		if !modelPluginAllowlist[p.Package] || !exactVersion.MatchString(p.Version) {
			return fmt.Errorf("unsupported model plugin")
		}
	}
	data, _ := json.Marshal(plugins)
	script := `set -eu
export PATH="$HOME/.local/fennec/node/bin:$HOME/.local/bin:$PATH"
python3 - ` + shellQuote(base64.StdEncoding.EncodeToString(data)) + ` <<'RECONCILE'
import base64,json,pathlib,subprocess,sys
wanted=json.loads(base64.b64decode(sys.argv[1])) or []
root=pathlib.Path.home()/'.config'/'magpie'
def installed(name,version):
 manifest=root/'plugins.json'
 entries=json.loads(manifest.read_text()).get('plugins',[]) if manifest.exists() else []
 if not any(e.get('spec')==name+'@'+version for e in entries):return False
 for file in (root/'plugins').rglob('package.json'):
  if file.is_symlink() or file.stat().st_size>1024*1024:continue
  try: pkg=json.loads(file.read_text())
  except (ValueError,OSError):continue
  if pkg.get('name')==name and pkg.get('version')==version:return True
 return False
for plugin in wanted:
 name,version=plugin['package'],plugin['version']
 if not installed(name,version):
  result=subprocess.run(['magpie','plugin','add',name+'@'+version],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=180)
  if result.returncode or not installed(name,version):raise SystemExit('model plugin installation verification failed')
RECONCILE
`
	return Exec(ctx, client, script)
}
