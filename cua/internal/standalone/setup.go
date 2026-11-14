package standalone

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// SetupName is the setup window's program in the app's Contents/MacOS:
// a small AppKit app (native/setup) that walks the user through the
// permissions and shows how to connect an agent.
const SetupName = "fennec-cua-setup"

// NoSetupEnv set to 1 keeps the daemon and install from opening the setup
// window (test machines, scripted installs).
const NoSetupEnv = "FENNEC_CUA_NO_SETUP"

// SetupHelper is the setup window's program inside app, "" when the app
// has none (an older or core build).
func SetupHelper(app string) string {
	p := filepath.Join(app, "Contents", "MacOS", SetupName)
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
		return p
	}
	return ""
}

// SetupDisabled reports whether NoSetupEnv turns the automatic window off.
func SetupDisabled() bool {
	v := strings.TrimSpace(os.Getenv(NoSetupEnv))
	return v != "" && v != "0"
}

// OpenSetup shows the setup window of app from anywhere (a terminal, an
// agent): it opens the app through LaunchServices without arguments,
// which the app answers by running its setup window, so macOS attributes
// the window, and the grants it checks, to the app and not to the caller.
func OpenSetup(app string) error {
	if SetupHelper(app) == "" {
		return fmt.Errorf("%s has no setup window; reinstall a newer archive", app)
	}
	if out, err := exec.Command("/usr/bin/open", "-n", "-a", app).CombinedOutput(); err != nil {
		return fmt.Errorf("open %s: %v %s", app, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StartSetupChild starts the setup window as a child of a process the app
// is responsible for (the daemon), detached so it outlives it.
func StartSetupChild(app string) error {
	helper := SetupHelper(app)
	if helper == "" {
		return errors.New("no setup window in " + app)
	}
	cmd := exec.Command(helper)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck // reap it; its exit status means nothing here
	return nil
}

// ExecSetup replaces this process (the app opened by the user) with the
// setup window. It returns only on failure.
func ExecSetup(app string) error {
	helper := SetupHelper(app)
	if helper == "" {
		return errors.New("no setup window in " + app)
	}
	return syscall.Exec(helper, []string{helper}, os.Environ())
}
