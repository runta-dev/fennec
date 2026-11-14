package terminalinput

import (
	"strconv"
	"strings"
)

// ImagePasteShortcut accepts traditional Ctrl-V and its Kitty/modifyOtherKeys
// encodings, including repeat events but never key releases or Alt-V.
func ImagePasteShortcut(data []byte) bool {
	if string(data) == "\x16" {
		return true
	}
	s := string(data)
	if !strings.HasPrefix(s, "\x1b[") || len(s) < 5 {
		return false
	}
	parts := strings.Split(s[2:len(s)-1], ";")
	key, modifier := "", ""
	switch s[len(s)-1] {
	case 'u':
		if len(parts) != 2 {
			return false
		}
		key, modifier = parts[0], parts[1]
	case '~':
		if len(parts) != 3 || parts[0] != "27" {
			return false
		}
		key, modifier = parts[2], parts[1]
	default:
		return false
	}
	event := strings.Split(modifier, ":")
	if len(event) > 2 || (len(event) == 2 && event[1] != "1" && event[1] != "2") {
		return false
	}
	mod, err := strconv.Atoi(event[0])
	if err != nil || (mod != 5 && mod != 6) {
		return false
	}
	key = strings.Split(key, ":")[0]
	return key == "118" || key == "86" || key == "22"
}
