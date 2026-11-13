package bench

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"fennec/cua/internal/vm"
)

// macOSWorld starts every task from an author-made VM snapshot. Most of
// what those snapshots hold that a task needs is created by the task's own
// pre_command; the rest is not published. A setup step recreates such state
// in a fresh clone of the golden image before the pre_command runs, for the
// tasks whose feasibility class is "setup" (state fully determined by the
// task and its grader: a Dock layout, a reminder, a Contacts group, the
// "me" card, an app past its first-launch screens) or "rebuild" (synthetic
// files standing in for the snapshot's benchmark_files folder, where the
// task and its grader only depend on names, kinds and counts). The step
// name is the task's reason code in bench/sets/feasibility.tsv.
//
// Setup steps hold no task text: names a grader looks for are read from
// the task's own grading commands in the local checkout at run time.

// BackupDir is where the upstream snapshot keeps the originals its
// pre_commands copy from (~/Benchmark_Backup).
const BackupDir = HomeDir + "/Benchmark_Backup"

// SetupSteps lists every step name.
var SetupSteps = []string{
	"dock", "reminder", "contacts-group", "me-card", "music-primed",
	"files-zipped", "files-separated-images", "files-sources", "files-faces", "files-skeleton",
}

// RunSetup runs one setup step for a task in the VM.
func RunSetup(ctx context.Context, s *vm.Session, step string, t Task) error {
	sh := func(script string, timeout time.Duration) error {
		if out, ok := guestShell(ctx, s, script, timeout); !ok {
			return fmt.Errorf("setup %s: %s", step, clip(out, 500))
		}
		return nil
	}
	switch step {
	case "dock":
		return sh(dockScript(), 2*time.Minute)
	case "reminder":
		name, err := gradedName(t)
		if err != nil {
			return err
		}
		return sh(osa(`tell application "Reminders" to make new reminder with properties {name:`+asString(name)+`}`)+
			"; sleep 2; "+osa(`tell application "Reminders" to quit`), 2*time.Minute)
	case "contacts-group":
		name, err := gradedName(t)
		if err != nil {
			return err
		}
		return sh(osa(`tell application "Contacts"`, `make new group with properties {name:`+asString(name)+`}`, `save`, `end tell`)+
			"; sleep 2; "+osa(`tell application "Contacts" to quit`), 2*time.Minute)
	case "me-card":
		// The upstream account's full name, which the Contacts "me" card
		// and its exported file take.
		const upstreamUser = "ec2-user"
		return sh(osa(`tell application "Contacts"`, `set first name of my card to `+asString(upstreamUser), `set middle name of my card to ""`, `set last name of my card to ""`, `save`, `end tell`)+
			"; sleep 2; "+osa(`tell application "Contacts" to quit`), 2*time.Minute)
	case "music-primed":
		// Return accepts "Welcome to Apple Music", then "Continue" on the
		// notifications pitch (macOS 26). The snapshot's Music had been
		// used before.
		script := `open -a Music; sleep 12`
		for range 2 {
			script += `; ` + osa(`tell application "Music" to activate`, `delay 2`, `tell application "System Events" to key code 36`) + `; sleep 5`
		}
		script += `; ` + osa(`tell application "Music" to quit`) + `; sleep 4`
		return sh(script, 3*time.Minute)
	case "files-zipped", "files-separated-images", "files-sources", "files-faces", "files-skeleton":
		return installBenchmarkFiles(ctx, s)
	}
	return fmt.Errorf("unknown setup step %q", step)
}

// osa runs AppleScript lines with osascript.
func osa(lines ...string) string {
	var b strings.Builder
	b.WriteString("osascript")
	for _, l := range lines {
		b.WriteString(" -e " + vm.Quote(l))
	}
	return b.String()
}

func asString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// gradedName reads the name the first grading command looks for with
// grep -q "NAME".
var grepName = regexp.MustCompile(`grep -q "([^"]+)"`)

func gradedName(t Task) (string, error) {
	for _, g := range t.Grading {
		if m := grepName.FindStringSubmatch(g.Command); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("%s: no grep -q \"NAME\" in its grading commands", t.ID)
}

// dockApps is the macOS 15 default Dock that the upstream snapshot's
// com.apple.dock.plist holds, on macOS 26: Apps replaces Launchpad, and
// Phone and Games (new in macOS 26) and the iWork apps (not installed) are
// left out.
var dockApps = []string{
	"/System/Applications/Apps.app", "/Applications/Safari.app", "/System/Applications/Messages.app",
	"/System/Applications/Mail.app", "/System/Applications/Maps.app", "/System/Applications/Photos.app",
	"/System/Applications/FaceTime.app", "/System/Applications/Calendar.app", "/System/Applications/Contacts.app",
	"/System/Applications/Reminders.app", "/System/Applications/Notes.app", "/System/Applications/Freeform.app",
	"/System/Applications/TV.app", "/System/Applications/Music.app", "/System/Applications/App Store.app",
	"/System/Applications/System Settings.app", "/System/Applications/iPhone Mirroring.app",
}

// dockScript sets the Dock and saves it where the pre_commands copy it
// from.
func dockScript() string {
	var tiles []string
	for _, app := range dockApps {
		url := "file://" + strings.ReplaceAll(app, " ", "%20") + "/"
		tiles = append(tiles, vm.Quote(`<dict><key>tile-data</key><dict><key>file-data</key><dict><key>_CFURLString</key><string>`+url+
			`</string><key>_CFURLStringType</key><integer>15</integer></dict></dict><key>tile-type</key><string>file-tile</string></dict>`))
	}
	return "defaults write com.apple.dock persistent-apps -array " + strings.Join(tiles, " ") +
		"; defaults delete com.apple.dock recent-apps 2>/dev/null; killall Dock; sleep 5; mkdir -p " + vm.Quote(BackupDir) +
		" && defaults export com.apple.dock " + vm.Quote(BackupDir+"/com.apple.dock.plist")
}

// installBenchmarkFiles writes the synthetic benchmark_files folder to
// ~/Benchmark_Backup, where the pre_commands copy it from. Only the parts
// some feasible task depends on exist: Multimedia/Zipped (one zip holding
// a folder with one text file), Multimedia/Separated_images (jpg/ with one
// image, png/ with three), Multimedia/Sources (6 jpg, 10 png, one each of
// txt, md and docx) and Multimedia/Faces (000004.png beside three jpg).
// The images are plain generated pictures; the docx is made by textutil.
func installBenchmarkFiles(ctx context.Context, s *vm.Session) error {
	payload, err := benchmarkFilesTar()
	if err != nil {
		return err
	}
	dest := BackupDir + "/benchmark_files"
	script := "rm -rf " + vm.Quote(dest) + " && mkdir -p " + vm.Quote(dest) + " && tar -xf - -C " + vm.Quote(dest) +
		" && textutil -convert docx -output " + vm.Quote(dest+"/Multimedia/Sources/summary.docx") + " " + vm.Quote(dest+"/Multimedia/Sources/summary.txt") +
		" && mv " + vm.Quote(dest+"/Multimedia/Sources/summary.txt") + " " + vm.Quote(dest+"/Multimedia/Sources/notes.txt")
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	if err := s.Run(cctx, script, bytes.NewReader(payload), &out, &out); err != nil {
		return fmt.Errorf("setup benchmark_files: %v: %s", err, clip(out.String(), 500))
	}
	return nil
}

// benchmarkFilesTar builds the folder as a tar stream; it is deterministic.
func benchmarkFilesTar() ([]byte, error) {
	files := map[string][]byte{}
	pic := func(n int, kind string) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 160, 120))
		c := color.RGBA{uint8(40 + 37*n%200), uint8(90 + 53*n%150), uint8(160 + 29*n%90), 255}
		for y := 0; y < 120; y++ {
			for x := 0; x < 160; x++ {
				if (x/20+y/20+n)%2 == 0 {
					img.Set(x, y, c)
				} else {
					img.Set(x, y, color.RGBA{250, 250, 245, 255})
				}
			}
		}
		var b bytes.Buffer
		if kind == "png" {
			_ = png.Encode(&b, img)
		} else {
			_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 85})
		}
		return b.Bytes()
	}
	text := []byte("A short sample text for the benchmark's file tasks.\n\nIt has two paragraphs and nothing else.\n")

	// Zipped: one zip whose folder holds the text file the unzip grader
	// looks for.
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for _, name := range []string{"unzipped_contents/", "unzipped_contents/Matterhorn Man.txt"} {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)}
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(name, "/") {
			if _, err := w.Write(text); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	files["Multimedia/Zipped/unzipped_contents.zip"] = zb.Bytes()

	// Separated_images: one jpg and three png in their own folders.
	files["Multimedia/Separated_images/jpg/000003.jpg"] = pic(3, "jpg")
	for i := range 3 {
		files[fmt.Sprintf("Multimedia/Separated_images/png/%06d.png", i)] = pic(i, "png")
	}

	// Sources: 6 jpg, 10 png and three documents (the docx is converted
	// from summary.txt in the VM).
	for i := 1; i <= 6; i++ {
		files[fmt.Sprintf("Multimedia/Sources/photo-%d.jpg", i)] = pic(10+i, "jpg")
	}
	for i := 1; i <= 10; i++ {
		files[fmt.Sprintf("Multimedia/Sources/image-%02d.png", i)] = pic(20+i, "png")
	}
	files["Multimedia/Sources/summary.txt"] = text
	files["Multimedia/Sources/readme.md"] = []byte("# Sample\n\nA short markdown file.\n")

	// Faces: the png the preview tasks open, beside three jpg.
	files["Multimedia/Faces/000004.png"] = pic(4, "png")
	for i := 1; i <= 3; i++ {
		files[fmt.Sprintf("Multimedia/Faces/portrait-%d.jpg", i)] = pic(30+i, "jpg")
	}

	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	dirs := map[string]bool{}
	mtime := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range names {
		parts := strings.Split(n, "/")
		for i := 1; i < len(parts); i++ {
			d := strings.Join(parts[:i], "/") + "/"
			if !dirs[d] {
				dirs[d] = true
				if err := tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: mtime}); err != nil {
					return nil, err
				}
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), ModTime: mtime}); err != nil {
			return nil, err
		}
		if _, err := io.Copy(tw, bytes.NewReader(files[n])); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Apps that tasks outside the original selection script by name, with
// their bundle ids. The golden image grants Automation for the original
// selection's apps; a trial whose commands name one of these grants it too.
var extraScriptedApps = map[string]string{
	"Script Editor": "com.apple.ScriptEditor2", "Music": "com.apple.Music", "Stickies": "com.apple.Stickies",
	"QuickTime Player": "com.apple.QuickTimePlayerX", "Automator": "com.apple.Automator",
	"Disk Utility": "com.apple.DiskUtility", "TextEdit": "com.apple.TextEdit", "Maps": "com.apple.Maps",
	"Font Book": "com.apple.FontBook", "Weather": "com.apple.weather", "Stocks": "com.apple.stocks",
}

// scriptedBy lists the bundle ids of extraScriptedApps that a task's
// commands address with tell application "NAME".
func scriptedBy(t Task) []string {
	all := t.Pre
	for _, g := range t.Grading {
		all += "\n" + g.Command
	}
	if t.InProcess != nil {
		all += "\n" + t.InProcess.Command
	}
	var ids []string
	for name, id := range extraScriptedApps {
		if strings.Contains(all, `application "`+name+`"`) || strings.Contains(all, `app "`+name+`"`) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// grantAutomation lets the guest agent and osascript send Apple events to
// the given apps without a consent prompt, as the golden image does for its
// own list.
func grantAutomation(ctx context.Context, s *vm.Session, bundleIDs []string) error {
	if len(bundleIDs) == 0 {
		return nil
	}
	agent, err := s.Output(ctx, "readlink -f \"$(command -v tart-guest-agent)\"")
	if err != nil {
		return err
	}
	for _, client := range []string{strings.TrimSpace(agent), "/usr/bin/osascript"} {
		for _, app := range bundleIDs {
			if err := s.GrantUserTCC(ctx, "kTCCServiceAppleEvents", client, app); err != nil {
				return err
			}
		}
	}
	return nil
}
