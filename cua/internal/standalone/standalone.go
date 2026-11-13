// Package standalone holds what the standalone fennec-cua distribution
// needs beyond the daemon: the app's layout and Info.plist, installing and
// uninstalling it for one user, the macOS permissions it needs, and the
// checks `fennec-cua doctor` runs.
//
// The distribution is one app bundle, FennecCUA.app:
//
//	Contents/Info.plist                 com.runta.fennec.cua, LSUIElement
//	Contents/MacOS/fennec-cua           the daemon, MCP bridge and CLI
//	Contents/MacOS/cua-driver           the pinned cua-driver (input, capture)
//	Contents/MacOS/cua-cursor-theme     cua-driver's cursor helper
//	Contents/Helpers/Chromium.app       the agent browser (optional)
//	Contents/Resources/licenses/        licences and third-party notices
//	Contents/Resources/skills/fennec-cua/  the agent skill
//	Contents/Resources/MANIFEST.json    versions, pins and digests
//
// macOS attributes the Accessibility and Screen Recording grants to the
// app: the daemon is started through LaunchServices (`open -a`), and
// cua-driver runs as its child, so one pair of grants covers both.
package standalone

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Names in the distribution.
const (
	AppName     = "FennecCUA.app"
	DisplayName = "Fennec CUA"
	BundleID    = "com.runta.fennec.cua"
	BinName     = "fennec-cua"
	SkillName   = "fennec-cua"
)

// Layout is where an install puts the app and the command link.
type Layout struct {
	// AppDir holds AppName (default ~/Applications).
	AppDir string
	// BinDir holds the fennec-cua link (default ~/.local/bin).
	BinDir string
}

// DefaultLayout is the per-user layout under home.
func DefaultLayout(home string) Layout {
	return Layout{AppDir: filepath.Join(home, "Applications"), BinDir: filepath.Join(home, ".local", "bin")}
}

// App is the installed app bundle.
func (l Layout) App() string { return filepath.Join(l.AppDir, AppName) }

// Exe is the installed fennec-cua executable.
func (l Layout) Exe() string { return Executable(l.App()) }

// Link is the command link on the PATH.
func (l Layout) Link() string { return filepath.Join(l.BinDir, BinName) }

// Executable is fennec-cua inside an app bundle.
func Executable(app string) string { return filepath.Join(app, "Contents", "MacOS", BinName) }

// SkillDir is the agent skill inside an app bundle.
func SkillDir(app string) string {
	return filepath.Join(app, "Contents", "Resources", "skills", SkillName)
}

// InfoPlist is the app's Info.plist. The usage descriptions are what macOS
// shows in its permission prompts.
func InfoPlist(version string) []byte {
	v := xmlEscape(version)
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleDisplayName</key>
	<string>` + DisplayName + `</string>
	<key>CFBundleIconFile</key>
	<string>FennecCUA</string>
	<key>CFBundleExecutable</key>
	<string>` + BinName + `</string>
	<key>CFBundleIdentifier</key>
	<string>` + BundleID + `</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>` + DisplayName + `</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>` + v + `</string>
	<key>CFBundleVersion</key>
	<string>` + v + `</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSAppleEventsUsageDescription</key>
	<string>Fennec CUA sends Apple Events only when an action an agent asked for needs to talk to another app.</string>
	<key>NSScreenCaptureUsageDescription</key>
	<string>Fennec CUA captures app windows so the agents you connect can see the apps they operate for you.</string>
</dict>
</plist>
`)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// IsApp reports whether dir is a standalone fennec-cua app bundle.
func IsApp(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "Contents", "Info.plist"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "<string>"+BundleID+"</string>")
}

// Permission is one macOS privacy grant the app uses.
type Permission struct {
	// Key is the daemon status key ("accessibility", "screen_recording"),
	// empty when the daemon does not report it.
	Key      string
	Name     string
	Pane     string
	URL      string
	Why      string
	Required bool
}

// Permissions are the grants, in the order to give them.
var Permissions = []Permission{
	{
		Key: "accessibility", Name: "Accessibility", Required: true,
		Pane: "System Settings > Privacy & Security > Accessibility",
		URL:  "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
		Why:  "read apps' accessibility trees (getAXState) and send clicks, keys and typing",
	},
	{
		Key: "screen_recording", Name: "Screen Recording", Required: true,
		Pane: "System Settings > Privacy & Security > Screen & System Audio Recording",
		URL:  "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
		Why:  "capture window screenshots (getScreenshot, getAXStateAndScreenshot)",
	},
	{
		Name: "Automation", Required: false,
		Pane: "System Settings > Privacy & Security > Automation",
		URL:  "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation",
		Why:  "not needed for normal use; macOS asks only if an action has to send Apple Events to another app",
	},
}

// PermissionGuide is the text that tells the user how to grant the
// permissions to the app at app.
func PermissionGuide(app string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "macOS permissions for %q (%s, %s):\n", DisplayName, app, BundleID)
	for i, p := range Permissions {
		need := "required"
		if !p.Required {
			need = "optional"
		}
		fmt.Fprintf(&b, "  %d. %s (%s): %s\n     %s\n     open %q\n", i+1, p.Name, need, p.Why, p.Pane, p.URL)
	}
	b.WriteString(`Turn "` + DisplayName + `" on in each pane. If it is not listed, run
  fennec-cua permissions --request
which asks macOS to show its prompts and to list the app (or use "+" in the pane and pick
` + app + `). After a Screen Recording change, restart the daemon:
  fennec-cua quit   (the next command starts it again)
On macOS 15 and later, with Screen Recording on, run once more:
  fennec-cua permissions --request
macOS then asks whether the app may capture the screen directly; choose Allow.
Close any macOS prompt still on screen, then check with: fennec-cua doctor
`)
	return b.String()
}
