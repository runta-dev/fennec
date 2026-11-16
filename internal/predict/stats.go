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

var versionPattern = regexp.MustCompile(`(?:^|[\s(])v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)(?:$|[\s)])`)

// screenVersion returns the first version number drawn on the screen.
func screenVersion(s *Screen) string {
	for r := 0; r < s.Rows; r++ {
		if m := versionPattern.FindStringSubmatch(s.RowText(r, 0, s.Cols)); m != nil {
			return m[1]
		}
	}
	return ""
}
