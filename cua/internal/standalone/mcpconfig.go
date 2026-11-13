package standalone

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MCPClients are the clients MCPConfig knows.
var MCPClients = []string{"claude", "codex", "json", "http"}

// MCPConfig is how to connect an agent to fennec-cua, for client (one of
// MCPClients, or "" for all). command is the fennec-cua to run, best the
// installed link.
func MCPConfig(client, command string) (string, error) {
	var b strings.Builder
	section := func(name string) bool { return client == "" || client == name }
	if section("claude") {
		fmt.Fprintf(&b, `# Claude Code: one command (user scope: every project)
claude mcp add --scope user fennec-cua -- %s mcp
# Long UI tasks: allow js calls up to 5 minutes (the REPL ends one at 90 s by itself)
#   export MCP_TOOL_TIMEOUT=300000

`, shellQuote(command))
	}
	if section("codex") {
		fmt.Fprintf(&b, `# Codex: one command
codex mcp add fennec-cua -- %s mcp
# or add to ~/.codex/config.toml:
[mcp_servers.fennec-cua]
command = %s
args = ["mcp"]
startup_timeout_sec = 30
tool_timeout_sec = 300

`, shellQuote(command), tomlQuote(command))
	}
	if section("json") {
		cfg := map[string]any{"mcpServers": map[string]any{"fennec-cua": map[string]any{"command": command, "args": []string{"mcp"}}}}
		data, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Fprintf(&b, "# Any MCP client over stdio (Claude Desktop, Cursor, ...): the usual mcpServers entry\n%s\n\n", data)
	}
	if section("http") {
		fmt.Fprintf(&b, `# MCP over Streamable HTTP (clients that cannot start a process). A bearer token is required.
# This runs the daemon in your terminal, so macOS attributes the permissions to the terminal app;
# prefer stdio. Stop the app's daemon first: %s quit
#   export FENNEC_CUA_HTTP_TOKEN="$(openssl rand -hex 32)"
#   %s serve --http 127.0.0.1:8765
# URL: http://127.0.0.1:8765/mcp   header: Authorization: Bearer $FENNEC_CUA_HTTP_TOKEN
`, shellQuote(command), shellQuote(command))
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("unknown client %q; one of %s", client, strings.Join(MCPClients, ", "))
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"$`\\!*?[](){}<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func tomlQuote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
