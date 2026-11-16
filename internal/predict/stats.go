package predict

import (
	"regexp"
	"strings"
)

var keyNames = map[KeyKind]string{
	KeyUnknown: "other", KeyText: "text", KeyPaste: "paste", KeyEnter: "enter",
	KeyNewline: "newline", KeyBackspace: "backspace", KeyDelete: "delete",
	KeyLeft: "left", KeyRight: "right", KeyUp: "up", KeyDown: "down",
	KeyHome: "home", KeyEnd: "end", KeyTab: "tab", KeyEscape: "esc",
	KeyCtrl: "ctrl", KeyAlt: "alt", KeyImagePaste: "image", KeyNeutral: "neutral",
	KeyWordLeft: "word-left", KeyWordRight: "word-right",
	KeyAltBackspace: "alt-backspace", KeyPasteFragment: "paste",
}

// Name names the key for usage statistics. It never includes typed text,
// except the first character of text that starts a command menu.
func (k Key) Name() string {
	switch {
	case k.Kind == KeyCtrl && k.Rune >= 'a' && k.Rune <= 'z':
		return "ctrl+" + string(k.Rune)
	case k.Kind == KeyText && (strings.HasPrefix(k.Text, "/") || strings.HasPrefix(k.Text, "@") || strings.HasPrefix(k.Text, "!")):
		return "text" + k.Text[:1]
	}
	if name, ok := keyNames[k.Kind]; ok {
		return name
	}
	return "other"
}

const semver = `(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)`

// bannerVersions match the version in each agent's own startup banner, so a
// version number in the transcript (another tool's, a project's) is not
// taken for the agent's.
var bannerVersions = map[string]*regexp.Regexp{
	"claude":   regexp.MustCompile(`Claude Code v` + semver),
	"codex":    regexp.MustCompile(`OpenAI Codex \(v` + semver + `\)`),
	"grok":     regexp.MustCompile(`Grok Build\s+` + semver + `\s*$`),
	"opencode": regexp.MustCompile(`^\s*` + semver + `\s*$`), // alone, bottom right
	"pi":       regexp.MustCompile(`█\s+v` + semver),
	"kimi":     regexp.MustCompile(`│\s+Version:\s+` + semver),
}

// bannerVersion returns the version in the agent's startup banner on the
// screen, or "".
func bannerVersion(agent string, s *Screen) string {
	re := bannerVersions[agent]
	if re == nil {
		return ""
	}
	from := 0
	if agent == "opencode" {
		from = max(0, s.Rows-3)
	}
	for r := from; r < s.Rows; r++ {
		if m := re.FindStringSubmatch(s.RowText(r, 0, s.Cols)); m != nil {
			return m[1]
		}
	}
	return ""
}
