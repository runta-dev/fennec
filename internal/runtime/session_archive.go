package runtime

import (
	"fennec/internal/localssh"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A cloud archive (THI-165) carries the conversations of every session so the
// sessions resume on another cloud computer. It reuses the takeover import
// format: CollectArchiveSessions builds the same SessionImport bundles that
// ApplySessionImport writes, relocated to the target's home and into the
// store the target's agent uses. Unlike a takeover, the archive keeps whole
// transcripts, not only what a resume loads.

// CloudAgentHome is where the cloud agent keeps conversations, or "" when the
// agent has no native conversation storage.
func CloudAgentHome(home, agent string) string { return cloudAgentHome(home, agent) }

// AgentHomes lists every store an agent may have written conversations to on
// this computer, the one in use first. Turning Magpie off or on, or signing
// the agent in, switches stores and leaves the earlier conversations behind.
func AgentHomes(home, agent string) []string {
	var homes []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			homes = append(homes, dir)
		}
	}
	switch agent {
	case "claude", "codex", "pi", "kimi", "opencode", "cursor", "grok":
	default:
		return nil
	}
	add(cloudAgentHome(home, agent))
	add(gatewayAgentHome(home, agent))
	add(nativeAgentHome(filepath.Join(home, ".local/fennec/native"), agent))
	logins, _ := os.ReadDir(filepath.Join(home, ".local/fennec/native-logins"))
	for _, login := range logins {
		if login.IsDir() {
			add(nativeAgentHome(filepath.Join(home, ".local/fennec/native-logins", login.Name()), agent))
		}
	}
	return homes
}

// CodexConversation is one Codex rollout and the store that holds it.
type CodexConversation struct {
	Home string
	ID   string
}

// CodexDirectories maps each working directory to the Codex conversations
// started in it, read once from the session_meta of every rollout.
func CodexDirectories(home string) map[string][]CodexConversation {
	byDirectory := map[string][]CodexConversation{}
	for _, codexHome := range AgentHomes(home, "codex") {
		for _, root := range []string{"sessions", "archived_sessions"} {
			filepath.WalkDir(filepath.Join(codexHome, root), func(file string, entry fs.DirEntry, err error) error {
				if err != nil || !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "rollout-") {
					return nil
				}
				if meta, ok := codexSessionMeta(file); ok {
					cwd := filepath.Clean(meta.Cwd)
					byDirectory[cwd] = append(byDirectory[cwd], CodexConversation{codexHome, meta.ID})
				}
				return nil
			})
		}
	}
	return byDirectory
}

// CollectArchiveSessions returns every conversation the agent keeps for the
// session working directory, from every store in AgentHomes. Claude and Codex
// keep each conversation of the directory (codex lists them from
// CodexDirectories); other agents keep the latest of each store, which is what
// a resume continues. A conversation that cannot be read is returned as an
// error next to the ones that could.
func CollectArchiveSessions(home, agent, directory, launchID string, codex []CodexConversation) ([]*SessionImport, []error) {
	type request struct {
		agentHome string
		args      []string
	}
	var requests []request
	switch agent {
	case "claude":
		for _, agentHome := range AgentHomes(home, agent) {
			entries, _ := os.ReadDir(filepath.Join(agentHome, "projects", ClaudeProjectDirectory(directory)))
			for _, entry := range entries {
				if id, ok := strings.CutSuffix(entry.Name(), ".jsonl"); ok && conversationPattern.MatchString(id) && entry.Type().IsRegular() {
					requests = append(requests, request{agentHome, []string{"--resume", id}})
				}
			}
		}
	case "codex":
		for _, conversation := range codex {
			requests = append(requests, request{conversation.Home, []string{"resume", conversation.ID}})
		}
	default:
		for _, agentHome := range AgentHomes(home, agent) {
			requests = append(requests, request{agentHome, []string{"--continue"}})
		}
	}
	var bundles []*SessionImport
	var failures []error
	seen := map[string]bool{}
	for _, request := range requests {
		bundle, _, err := CollectLocalSession(request.agentHome, agent, directory, launchID, request.args)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if bundle == nil || seen[bundle.Conversation] {
			continue
		}
		seen[bundle.Conversation] = true
		if err := untrimmed(request.agentHome, agent, directory, bundle); err != nil {
			failures = append(failures, err)
			continue
		}
		bundles = append(bundles, bundle)
	}
	return bundles, failures
}

// untrimmed puts the whole transcript back: collection trims what a resume
// does not load, but an archive is the user's copy of the conversation.
func untrimmed(agentHome, agent, directory string, bundle *SessionImport) error {
	var file string
	switch agent {
	case "claude":
		// collectClaude takes the newest copy of the conversation in the store.
		matches, _ := filepath.Glob(filepath.Join(agentHome, "projects", "*", bundle.Conversation+".jsonl"))
		newest := int64(0)
		for _, match := range matches {
			if info, err := os.Lstat(match); err == nil && info.Mode().IsRegular() && info.ModTime().UnixNano() > newest {
				newest, file = info.ModTime().UnixNano(), match
			}
		}
	case "codex":
		file = filepath.Join(agentHome, filepath.FromSlash(bundle.Files[0].Path))
	}
	if file == "" {
		return nil
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	bundle.Modified = info.ModTime()
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	for i := range bundle.Files {
		if agent == "codex" || bundle.Files[i].Path == bundle.Conversation+".jsonl" {
			bundle.Files[i].Data = data
			break
		}
	}
	return nil
}

// ValidateSessionImport checks a bundle read from an archive before it is
// applied.
func ValidateSessionImport(bundle SessionImport) error { return bundle.validate() }

// RestoreSessionTime gives an imported transcript its original modification
// time: "claude --continue" and "codex resume --last" pick the most recently
// modified conversation, and an import would otherwise make every restored
// conversation look newest.
func RestoreSessionTime(home string, bundle SessionImport) error {
	if bundle.Modified.IsZero() {
		return nil
	}
	file := ""
	switch bundle.Agent {
	case "claude":
		workspace := filepath.Join(home, ".fen", bundle.LaunchID)
		file = filepath.Join(cloudAgentHome(home, "claude"), "projects", ClaudeProjectDirectory(workspace), bundle.Conversation+".jsonl")
	case "codex":
		found, err := localssh.FindCodexRollout(cloudAgentHome(home, "codex"), bundle.Conversation)
		if err != nil {
			return nil
		}
		file = found
	default:
		return nil
	}
	if info, err := os.Lstat(file); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return os.Chtimes(file, bundle.Modified, bundle.Modified)
}
