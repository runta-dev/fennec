package agent

import (
	"os"
	"path/filepath"
	"sort"
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

// PanelCatalog keeps the announced integrations visible without claiming that
// they are installed. Discovery remains factual for runtime preparation.
func PanelCatalog(discovered []Agent) []Agent {
	fixed := map[string]bool{"claude": true, "codex": true, "pi": true, "kimi": true, "claude-desktop": true, "opencode": true, "devin": true, "grok": true, "cursor": true}
	byID := map[string]Agent{}
	for _, item := range discovered {
		byID[item.ID] = item
	}
	result := []Agent{}
	for _, definition := range catalog {
		if item, ok := byID[definition.id]; ok {
			result = append(result, item)
		} else if fixed[definition.id] {
			result = append(result, Agent{ID: definition.id, Name: definition.name, Icon: definition.icon, Cloud: definition.cloud})
		}
	}
	return result
}

// These cloud agents are available even when this Mac has no CLI installation.
func CloudAgentIDs() []string { return []string{"claude", "codex", "pi"} }

// CodexDesktopID is ChatGPT.app's Codex (bundle com.openai.codex). Its codex
// app-server can run on the cloud computer through Fennec's loopback bridge.
// It is a desktop application, never a CLI agent prepared on the runtime.
const CodexDesktopID = "codex-desktop"

var panelOrder = map[string]int{CodexDesktopID: 0, "codex": 1, "claude": 2, "pi": 3}

// PanelList is the Agents tab: Codex Desktop first, then the CLI catalog with
// the cloud-capable agents ordered Codex, Claude Code, Pi.
func PanelList(discovered []Agent) []Agent {
	home, _ := os.UserHomeDir()
	return panelList(discovered, []string{"/Applications", filepath.Join(home, "Applications")})
}

func panelList(discovered []Agent, appRoots []string) []Agent {
	desktop := Agent{ID: CodexDesktopID, Name: "Codex Desktop", Icon: "codex-color.svg", Cloud: true}
	if codexDesktopInstalled(appRoots) {
		desktop.Installed, desktop.Evidence = true, "application"
	}
	result := append([]Agent{desktop}, PanelCatalog(discovered)...)
	sort.SliceStable(result, func(i, j int) bool { return rank(result[i].ID) < rank(result[j].ID) })
	return result
}

func rank(id string) int {
	if value, ok := panelOrder[id]; ok {
		return value
	}
	return len(panelOrder)
}

func codexDesktopInstalled(roots []string) bool {
	for _, root := range roots {
		for _, name := range []string{"ChatGPT.app", "Codex.app"} {
			if info, err := os.Stat(filepath.Join(root, name, "Contents/Resources/codex-cli")); err == nil && info.IsDir() {
				return true
			}
		}
	}
	return false
}
