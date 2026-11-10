package agent

import (
	"os"
	"path/filepath"
)

type Agent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Cloud     bool   `json:"cloudSupported"`
	Icon      string `json:"icon"`
	Evidence  string `json:"evidence"`
}
type definition struct {
	id, name, icon      string
	bins, configs, apps []string
	cloud               bool
}

// Discovery evidence follows Magpie's executable/config/application approach.
// Config files are stat'ed only; credentials and file contents are never read.
var catalog = []definition{
	{"claude", "Claude Code", "claudecode-color.svg", []string{"claude"}, []string{".claude/settings.json"}, nil, true},
	{"codex", "Codex", "codex-color.svg", []string{"codex"}, []string{".codex/config.toml"}, nil, true},
	{"pi", "Pi", "pi.svg", []string{"pi"}, []string{".pi/agent/settings.json"}, nil, true},
	{"kimi", "Kimi Code", "kimi.svg", []string{"kimi"}, []string{".kimi-code/config.toml", ".kimi/config.toml"}, nil, false},
	{"claude-desktop", "Claude Desktop", "claude-color.svg", nil, []string{"Library/Application Support/Claude/claude_desktop_config.json"}, []string{"Claude.app"}, false},
	{"opencode", "OpenCode", "opencode.svg", []string{"opencode"}, []string{".config/opencode/opencode.json", ".config/opencode/opencode.jsonc"}, nil, true},
	{"devin", "Devin", "devin.svg", []string{"devin"}, []string{".config/devin/config.json", "Library/Application Support/devin/config.json"}, nil, false},
	// 'grok' is also an unrelated program: do not treat PATH alone as evidence.
	{"grok", "Grok Build", "xai.svg", nil, []string{".grok/config.toml"}, nil, false},
	{"gemini", "Gemini CLI", "geminicli-color.svg", []string{"gemini"}, []string{".gemini/settings.json"}, nil, false},
	{"cursor", "Cursor CLI", "cursor.svg", []string{"cursor-agent"}, []string{".cursor/cli-config.json"}, nil, false},
	{"copilot", "Copilot CLI", "copilot-color.svg", []string{"copilot"}, []string{".copilot/config.json"}, nil, false},
}

func searchPaths(home string) []string {
	paths := filepath.SplitList(os.Getenv("PATH"))
	for _, p := range []string{".local/bin", ".bun/bin", ".npm-global/bin", ".volta/bin", ".cargo/bin", "Library/pnpm", ".opencode/bin", ".kimi-code/bin"} {
		paths = append(paths, filepath.Join(home, p))
	}
	paths = append(paths, "/opt/homebrew/bin", "/usr/local/bin")
	for _, pattern := range []string{".nvm/versions/node/*/bin", ".local/share/fnm/node-versions/*/installation/bin"} {
		matches, _ := filepath.Glob(filepath.Join(home, pattern))
		paths = append(paths, matches...)
	}
	return paths
}
func discoverAt(home string, paths []string, appRoots []string) []Agent {
	result := []Agent{}
	for _, d := range catalog {
		evidence := ""
		for _, bin := range d.bins {
			for _, dir := range paths {
				if !filepath.IsAbs(dir) {
					continue
				}
				if originalExecutable(home, filepath.Join(dir, bin)) {
					evidence = "executable"
					break
				}
			}
			if evidence != "" {
				break
			}
		}
		if evidence == "" {
			for _, config := range d.configs {
				if info, err := os.Stat(filepath.Join(home, config)); err == nil && info.Mode().IsRegular() {
					evidence = "configuration"
					break
				}
			}
		}
		if evidence == "" {
			for _, app := range d.apps {
				for _, root := range appRoots {
					if info, err := os.Stat(filepath.Join(root, app, "Contents/Info.plist")); err == nil && info.Mode().IsRegular() {
						evidence = "application"
						break
					}
				}
			}
		}
		if evidence != "" {
			result = append(result, Agent{d.id, d.name, true, d.cloud, d.icon, evidence})
		}
	}
	return result
}
func Discover() []Agent {
	home, _ := os.UserHomeDir()
	return discoverAt(home, searchPaths(home), []string{"/Applications", filepath.Join(home, "Applications")})
}
