package standalone

import (
	"fmt"
	"io"
	"strings"
)

// Check levels.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
)

// Check is one line of `fennec-cua doctor`.
type Check struct {
	Name   string `json:"name"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
	// Fix is what to do about a warning or failure.
	Fix string `json:"fix,omitempty"`
}

// Facts are what doctor found; Diagnose turns them into checks.
type Facts struct {
	// Exe is this fennec-cua (links resolved); App the standalone app it
	// runs from, empty when it is not inside one (a development build or
	// Fennec.app's copy).
	Exe string
	App string
	// SignatureErr is the app's signature check (nil: verified).
	SignatureErr error
	Quarantined  bool
	// Driver is the cua-driver the daemon would start ("" when none).
	Driver string
	// Chromium is the browser executable ("" when none); ChromiumOff is
	// set when FENNEC_CUA_CHROMIUM=off turned it off.
	Chromium    string
	ChromiumOff bool
	// Command is the fennec-cua the PATH finds ("" when none); CommandOK
	// says it is this one.
	Command   string
	CommandOK bool
	// ConfigErr is the configuration file's load error.
	ConfigErr  error
	ConfigPath string

	// DaemonErr is set when the daemon could not be reached or started.
	DaemonErr error
	// DaemonStarted is set when doctor started it.
	DaemonStarted bool
	// DaemonExe is the executable the running daemon runs from.
	DaemonExe string
	// Permissions are the grants the daemon reports (its own, which are
	// the app's): "granted", "denied" or "unknown".
	Permissions map[string]string
	Stopped     bool
	// DriverRunning is whether the daemon's cua-driver answers.
	DriverRunning bool
	// BrowserAvailable is whether the daemon serves browser tabs.
	BrowserAvailable bool
	// CaptureChecked is set when the daemon took a test capture;
	// CaptureErr is its failure ("" when it worked) and CaptureConsent
	// marks a failure only because macOS has not yet asked whether the app
	// may capture the screen directly.
	CaptureChecked bool
	CaptureErr     string
	CaptureConsent bool
	// ConfigMissing is set when there is no configuration file (defaults).
	ConfigMissing bool
}

// Diagnose lists the checks, most basic first.
func Diagnose(f Facts) []Check {
	var cs []Check
	add := func(name, level, detail, fix string) {
		cs = append(cs, Check{Name: name, Level: level, Detail: detail, Fix: fix})
	}

	switch {
	case f.App == "":
		add("app", Warn, "this fennec-cua ("+f.Exe+") is not inside "+AppName,
			"macOS gives permissions to the app, not to a bare binary; install the standalone archive (see the README) and run the fennec-cua it links")
	case f.SignatureErr != nil:
		add("app", Fail, f.SignatureErr.Error(), "reinstall from a fresh copy of the archive")
	default:
		add("app", OK, f.App+" (signature verifies)", "")
	}
	if f.App != "" {
		if f.Quarantined {
			add("quarantine", Warn, "the app carries the quarantine attribute; macOS may refuse to start the daemon",
				"run `fennec-cua install` (it clears the attribute on the installed copy) or: xattr -dr "+QuarantineAttr+" "+quote(f.App))
		} else {
			add("quarantine", OK, "none on the app", "")
		}
	}
	if f.Driver == "" {
		add("cua-driver", Fail, "no cua-driver beside fennec-cua; desktop input and screenshots are unavailable",
			"reinstall the standalone archive, which ships the pinned cua-driver in Contents/MacOS")
	} else {
		add("cua-driver", OK, f.Driver, "")
	}
	switch {
	case f.ChromiumOff:
		add("browser", Warn, "FENNEC_CUA_CHROMIUM=off turns the browser surface off", "unset FENNEC_CUA_CHROMIUM to use the bundled Chromium")
	case f.Chromium == "":
		add("browser", Warn, "no bundled Chromium; cua.createBrowserTab and the other browser calls fail (native apps still work)",
			"install the full archive (with Chromium.app) to use browser tabs")
	default:
		add("browser", OK, f.Chromium, "")
	}
	switch {
	case f.Command == "":
		add("command", Warn, "fennec-cua is not on PATH", "add the link's directory to PATH, e.g. export PATH=\"$HOME/.local/bin:$PATH\"")
	case !f.CommandOK:
		add("command", Warn, "the fennec-cua on PATH is "+f.Command+", not this one ("+f.Exe+")", "put the installed link first on PATH, or run `fennec-cua install` again")
	default:
		add("command", OK, f.Command, "")
	}
	switch {
	case f.ConfigErr != nil:
		add("config", Fail, f.ConfigPath+": "+f.ConfigErr.Error(), "fix or remove the file; every tool stays closed until it loads")
	case f.ConfigPath != "" && f.ConfigMissing:
		add("config", OK, f.ConfigPath+" (none; defaults apply)", "")
	case f.ConfigPath != "":
		add("config", OK, f.ConfigPath+" (loads)", "")
	}

	if f.DaemonErr != nil {
		add("daemon", Fail, f.DaemonErr.Error(), "run `fennec-cua serve` in a terminal to see why it does not start; its log is daemon.log in the daemon directory")
		return cs
	}
	detail := "running"
	if f.DaemonStarted {
		detail = "started by doctor"
	}
	if f.DaemonExe != "" {
		detail += " from " + f.DaemonExe
	}
	add("daemon", OK, detail, "")
	if f.DaemonExe != "" && f.App != "" && !strings.HasPrefix(f.DaemonExe, strings.TrimSuffix(f.App, "/")+"/") {
		add("daemon app", Warn, "the running daemon is not this app's ("+f.DaemonExe+"); it answers every client on this account",
			"run `fennec-cua quit` and then any fennec-cua command to start this app's daemon")
	}
	for _, p := range Permissions {
		if p.Key == "" {
			continue
		}
		state := f.Permissions[p.Key]
		switch state {
		case "granted":
			add(p.Name, OK, "granted", "")
		default:
			if state == "" {
				state = "unknown"
			}
			add(p.Name, Fail, state+": needed to "+p.Why,
				"turn on \""+DisplayName+"\" in "+p.Pane+" (open \""+p.URL+"\"); `fennec-cua permissions --request` shows the macOS prompt; then `fennec-cua quit`")
		}
	}
	if f.Driver != "" {
		if f.DriverRunning {
			add("cua-driver process", OK, "answering", "")
		} else {
			add("cua-driver process", Fail, "the daemon's cua-driver does not answer", "see daemon.log; `fennec-cua quit` restarts the daemon and its driver on the next command")
		}
	}
	if f.CaptureChecked {
		switch {
		case f.CaptureErr == "":
			add("screen capture", OK, "a test capture worked", "")
		case f.CaptureConsent:
			add("screen capture", Fail, "macOS has not yet asked whether "+DisplayName+" may capture the screen directly (macOS 15 and later ask once), so screenshots fail; the accessibility tree works",
				"run `fennec-cua permissions --request` and choose Allow in the macOS alert")
		default:
			add("screen capture", Fail, "a test capture failed: "+f.CaptureErr,
				"check Screen Recording for \""+DisplayName+"\", then `fennec-cua quit`; daemon.log has the driver's messages")
		}
	}
	if f.Chromium != "" && !f.ChromiumOff {
		if f.BrowserAvailable {
			add("browser surface", OK, "available (starts on the first browser call)", "")
		} else {
			add("browser surface", Warn, "the daemon has no browser surface", "check daemon.log for Chromium errors")
		}
	}
	if f.Stopped {
		add("stop", Warn, "fennec-cua is stopped: every call is refused", "run `fennec-cua resume` when the user agrees")
	}
	return cs
}

// Healthy reports whether no check failed.
func Healthy(cs []Check) bool {
	for _, c := range cs {
		if c.Level == Fail {
			return false
		}
	}
	return true
}

// Render writes the checks as text.
func Render(w io.Writer, cs []Check) {
	for _, c := range cs {
		fmt.Fprintf(w, "%-4s  %-18s %s\n", c.Level, c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "      %-18s fix: %s\n", "", c.Fix)
		}
	}
}

func quote(s string) string {
	if strings.ContainsAny(s, " '\"$") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
