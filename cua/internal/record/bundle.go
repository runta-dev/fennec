package record

import "strings"

// bundleStem is the name of the outermost app bundle in an executable's
// path, without .app: "System Settings" for
// /System/Applications/System Settings.app/Contents/MacOS/System Settings.
func bundleStem(path string) string {
	for _, part := range strings.Split(path, "/") {
		if strings.HasSuffix(part, ".app") && len(part) > len(".app") {
			return strings.TrimSuffix(part, ".app")
		}
	}
	return ""
}
