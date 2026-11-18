package sandbox

import (
	"path/filepath"
	"strings"
)

// Codex parity for the local edge connector.
//
// Codex runs ordinary commands inside the workspace sandbox without asking and
// only reviews commands that explicitly request escalation
// (sandbox_permissions=require_escalated plus a justification). Cloud agents
// reach this Mac over plain SSH exec, so the same request is expressed as a
// command prefix:
//
//	fennec-escalate --reason "<justification>" -- <command>
//
// The prefix is advertised to the agent in stderr when the sandbox denies a
// command, mirroring Codex's on-request instructions.
const EscalateCommand = "fennec-escalate"

// ModeEscalated is Codex's EscalatedSandboxWithRestrictions: an approved
// escalation gets full-disk write and network, while denied-read credential
// paths stay unreadable because this policy has explicit deny-read entries.
const ModeEscalated = "escalated"

// ProtectedMetadataNames mirrors Codex PROTECTED_METADATA_PATH_NAMES. Inside
// each writable root these stay read-only in the workspace sandbox.
var ProtectedMetadataNames = []string{".git", ".agents", ".codex", ".aws"}

// ForApprovedEscalation returns the policy an approved escalation runs with.
func ForApprovedEscalation(policy Policy) Policy {
	return Policy{Approval: policy.Approval, Mode: ModeEscalated, Network: true, WritableRoots: policy.WritableRoots}
}

// EscalationHint is appended to stderr when the workspace sandbox appears to
// have denied a command, so the agent can re-issue it with a justification.
func EscalationHint(command string) string {
	return "Fennec: this command was likely blocked by the workspace sandbox on the Mac (writes outside the project, protected metadata such as .git, or network access).\n" +
		"Fennec: if it is required, run this from the cloud computer with a one-sentence justification to request approval on the Mac:\n" +
		"Fennec:   " + EscalateCommand + " --reason \"<why this is needed>\" -- " + ShellQuote(command) + "\n" +
		"Fennec: keep the command quoted so it runs unchanged on the Mac.\n"
}

// ShellQuote returns a POSIX single-quoted word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// ParseEscalation recognizes the escalation prefix. It returns the inner
// command exactly as written after the `--` separator.
func ParseEscalation(command string) (inner, reason string, escalated bool) {
	trimmed := strings.TrimLeft(command, " \t")
	if !strings.HasPrefix(trimmed, EscalateCommand) {
		return command, "", false
	}
	rest := trimmed[len(EscalateCommand):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return command, "", false
	}
	pos := 0
	next := func() (string, bool) {
		for pos < len(rest) && (rest[pos] == ' ' || rest[pos] == '\t') {
			pos++
		}
		if pos >= len(rest) {
			return "", false
		}
		var word strings.Builder
		for pos < len(rest) && rest[pos] != ' ' && rest[pos] != '\t' {
			switch c := rest[pos]; c {
			case '\'':
				end := strings.IndexByte(rest[pos+1:], '\'')
				if end < 0 {
					return "", false
				}
				word.WriteString(rest[pos+1 : pos+1+end])
				pos += end + 2
			case '"':
				pos++
				for pos < len(rest) && rest[pos] != '"' {
					if rest[pos] == '\\' && pos+1 < len(rest) {
						pos++
					}
					word.WriteByte(rest[pos])
					pos++
				}
				if pos >= len(rest) {
					return "", false
				}
				pos++
			case '\\':
				if pos+1 < len(rest) {
					word.WriteByte(rest[pos+1])
				}
				pos += 2
			default:
				word.WriteByte(c)
				pos++
			}
		}
		return word.String(), true
	}
	for {
		word, ok := next()
		if !ok {
			return command, "", false
		}
		switch {
		case word == "--":
			inner = strings.TrimSpace(rest[pos:])
			// A command passed as one quoted word (the cloud fennec-escalate
			// form) runs exactly as quoted.
			if inner != "" && (inner[0] == '\'' || inner[0] == '"') {
				if segments := shellSegments(inner); quotedWord(inner) && len(segments) == 1 && len(segments[0]) == 1 {
					inner = segments[0][0]
				}
			}
			reason = strings.Join(strings.Fields(reason), " ")
			if inner == "" || reason == "" || len(reason) > 512 {
				return command, "", false
			}
			return inner, reason, true
		case word == "--reason":
			value, ok := next()
			if !ok {
				return command, "", false
			}
			reason = value
		case strings.HasPrefix(word, "--reason="):
			reason = strings.TrimPrefix(word, "--reason=")
		default:
			return command, "", false
		}
	}
}

// quotedWord reports whether s is exactly one quoted shell word.
func quotedWord(s string) bool {
	quote := s[0]
	if quote == '\'' {
		return strings.Count(s, "'")%2 == 0 && s[len(s)-1] == '\''
	}
	return s[len(s)-1] == quote
}

// IsLikelySandboxDenied ports Codex's is_likely_sandbox_denied heuristic.
func IsLikelySandboxDenied(exitCode int, output string) bool {
	if exitCode == 0 {
		return false
	}
	lower := strings.ToLower(output)
	for _, marker := range []string{"operation not permitted", "permission denied", "read-only file system", "seccomp", "sandbox", "landlock", "failed to write file"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// IsDangerousCommand ports Codex's POSIX dangerous-command rule: a forced rm,
// seen through sudo, env assignments and `sh -c` style scripts. Nesting deeper
// than 8 fails closed.
func IsDangerousCommand(command string) bool { return dangerousScript(command, 0, forcedRm) }

type rmCheck func(args []string) bool

func dangerousScript(script string, depth int, check rmCheck) bool {
	if depth > 8 {
		return true
	}
	for _, segment := range shellSegments(script) {
		if dangerousWords(segment, depth, check) {
			return true
		}
	}
	// Substitutions inside double quotes still execute; inspect their bodies.
	for _, body := range substitutionBodies(script) {
		if dangerousScript(body, depth+1, check) {
			return true
		}
	}
	return false
}

// substitutionBodies returns the bodies of $(...) and `...` spans anywhere in
// the script, including inside double quotes.
func substitutionBodies(script string) []string {
	var bodies []string
	for i := 0; i < len(script); i++ {
		if script[i] == '`' {
			end := strings.IndexByte(script[i+1:], '`')
			if end < 0 {
				break
			}
			bodies = append(bodies, script[i+1:i+1+end])
			i += end + 1
			continue
		}
		if script[i] == '$' && i+1 < len(script) && script[i+1] == '(' {
			depth := 0
			for j := i + 1; j < len(script); j++ {
				if script[j] == '(' {
					depth++
				} else if script[j] == ')' {
					depth--
					if depth == 0 {
						bodies = append(bodies, script[i+2:j])
						i = j
						break
					}
				}
			}
		}
	}
	return bodies
}

func dangerousWords(words []string, depth int, check rmCheck) bool {
	for len(words) > 0 {
		head := filepath.Base(words[0])
		switch {
		case strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "="):
			words = words[1:]
		case head == "sudo" || head == "doas":
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				flag := words[0]
				words = words[1:]
				if (flag == "-u" || flag == "-g" || flag == "-C" || flag == "-h" || flag == "-p") && len(words) > 0 {
					words = words[1:]
				}
			}
		case head == "env":
			words = words[1:]
			for len(words) > 0 && (strings.HasPrefix(words[0], "-") || strings.Contains(words[0], "=")) {
				words = words[1:]
			}
		case head == "command" || head == "exec" || head == "nohup" || head == "time":
			words = words[1:]
		case head == "bash" || head == "sh" || head == "zsh" || head == "dash" || head == "ksh":
			for i := 1; i < len(words); i++ {
				if strings.HasPrefix(words[i], "-") && !strings.HasPrefix(words[i], "--") && strings.Contains(words[i], "c") {
					return i+1 < len(words) && dangerousScript(words[i+1], depth+1, check)
				}
			}
			return false
		case head == "rm":
			return check(words[1:])
		default:
			return false
		}
	}
	return false
}

// shellSegments splits a POSIX shell script into simple-command word lists.
// Control operators, pipes, grouping and substitutions start new segments so
// nested commands are inspected too.
func shellSegments(script string) [][]string {
	var segments [][]string
	var words []string
	var word strings.Builder
	inWord := false
	flushWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	flushSegment := func() {
		flushWord()
		if len(words) > 0 {
			segments = append(segments, words)
			words = nil
		}
	}
	for i := 0; i < len(script); i++ {
		c := script[i]
		switch c {
		case ' ', '\t':
			flushWord()
		case '\n', ';', '&', '|', '(', ')', '{', '}', '`':
			flushSegment()
		case '$':
			if i+1 < len(script) && script[i+1] == '(' {
				flushSegment()
				i++
				continue
			}
			word.WriteByte(c)
			inWord = true
		case '\'':
			end := strings.IndexByte(script[i+1:], '\'')
			if end < 0 {
				word.WriteString(script[i+1:])
				i = len(script)
			} else {
				word.WriteString(script[i+1 : i+1+end])
				i += end + 1
			}
			inWord = true
		case '"':
			i++
			for i < len(script) && script[i] != '"' {
				if script[i] == '\\' && i+1 < len(script) {
					i++
				}
				word.WriteByte(script[i])
				i++
			}
			inWord = true
		case '\\':
			if i+1 < len(script) {
				word.WriteByte(script[i+1])
				i++
			}
			inWord = true
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	flushSegment()
	return segments
}

// rmOptions splits rm arguments into flags and targets.
func rmOptions(args []string) (force, recursive bool, targets []string) {
	options := true
	for _, arg := range args {
		switch {
		case options && arg == "--":
			options = false
		case options && (arg == "--force"):
			force = true
		case options && (arg == "--recursive"):
			recursive = true
		case options && strings.HasPrefix(arg, "--"):
		case options && strings.HasPrefix(arg, "-") && len(arg) > 1:
			force = force || strings.Contains(arg, "f")
			recursive = recursive || strings.ContainsAny(arg, "rR")
		default:
			targets = append(targets, arg)
		}
	}
	return force, recursive, targets
}

func forcedRm(args []string) bool {
	force, _, _ := rmOptions(args)
	return force
}

// IsCatastrophicDeletion is the Full access guard. Full access runs everything
// without review, including forced deletion of temporary and project files,
// but still refuses a recursive or forced rm whose target is the filesystem
// root, the home directory, a top-level system directory, or a target that
// cannot be resolved safely (`.`, `..`, a bare glob, or a bare variable such as
// "$DIR" or "$DIR/" that expands to / when unset). Nesting deeper than 8 fails
// closed.
func IsCatastrophicDeletion(command, home string) bool {
	return dangerousScript(command, 0, func(args []string) bool {
		force, recursive, targets := rmOptions(args)
		if !force && !recursive {
			return false
		}
		for _, target := range targets {
			if protectedDeletionTarget(target, home) {
				return true
			}
		}
		return false
	})
}

var protectedTopLevel = map[string]bool{
	"/Applications": true, "/Library": true, "/System": true, "/Users": true, "/Volumes": true,
	"/bin": true, "/cores": true, "/dev": true, "/etc": true, "/home": true, "/opt": true,
	"/private": true, "/root": true, "/sbin": true, "/usr": true, "/var": true,
	"/private/etc": true, "/private/var": true, "/private/tmp": true, "/tmp": true,
}

func protectedDeletionTarget(target, home string) bool {
	t := strings.TrimSpace(target)
	switch t {
	case "", ".", "..", "./", "../", "*", ".*", "./*", "~", "~/", "~/*":
		return true
	}
	// "$HOME", "${HOME}/", "$DIR/*": a variable with nothing after it.
	if strings.HasPrefix(t, "$") {
		rest := strings.TrimLeft(t[1:], "{")
		end := strings.IndexFunc(rest, func(r rune) bool {
			return !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
		})
		if end < 0 {
			return true
		}
		tail := strings.TrimPrefix(rest[end:], "}")
		if tail == "" || tail == "/" || tail == "/*" || tail == "/.*" {
			return true
		}
		return false
	}
	if strings.HasPrefix(t, "~/") {
		if home == "" {
			return false
		}
		t = home + t[1:]
	}
	if !strings.HasPrefix(t, "/") {
		return false
	}
	cleaned := filepath.Clean(strings.TrimSuffix(strings.TrimSuffix(t, "*"), "/"))
	if cleaned == "/" || cleaned == "." || protectedTopLevel[cleaned] {
		return true
	}
	if home != "" && (cleaned == filepath.Clean(home) || cleaned == filepath.Dir(filepath.Clean(home))) {
		return true
	}
	return false
}
