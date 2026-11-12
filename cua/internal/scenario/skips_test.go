//go:build scenario

package scenario

import "testing"

// Scenarios this VM cannot run. Each records its reason in the report.
const (
	whyOneDisplay = "the tart VM has a single display; a second display (and one at negative coordinates) cannot be attached"
	whyNoQt       = "no Qt app is installed and the VM has no network to fetch one"
	whyNoChrome   = "Google Chrome is not installed and the VM has no network; its dmg would have to be pushed from the host"
	whyNoKitty    = "kitty is not installed and the VM has no network to fetch it"
	whyNoModel    = "grounding needs a vision model endpoint and key; the VM has neither network nor key"
	whyLock       = "locking the VM's only session leaves it at the lock screen, which nothing may type into, so every later scenario would fail"
	whyConfirm    = "the confirm dialog is Fennec.app's UI; fennec-cua alone shows none (confirm mode answers unavailable), and Fennec.app is not built in this VM"
	whyPackaging  = "a packaging check of Fennec.app (embedded daemon, first-run capture alert, permission guide); Fennec.app is not built in this VM"
	whyDriverApp  = "making the bundled cua-driver (a background agent with no windows) the active app needs an activation it does not accept from outside"
)

func TestGroundUnderOtherWindowRefused(t *testing.T) { skip(t, []string{"CAP-RGB-039"}, whyNoModel) }
func TestClickOnNegativeOriginDisplay(t *testing.T)  { skip(t, []string{"CAP-RGB-044"}, whyOneDisplay) }
func TestWindowShotSecondDisplay(t *testing.T)       { skip(t, []string{"CAP-RGB-046"}, whyOneDisplay) }
func TestPointWithoutWindowSecondDisplay(t *testing.T) {
	skip(t, []string{"CAP-RGB-050"}, whyOneDisplay)
}
func TestQtWindowForegroundClick(t *testing.T)       { skip(t, []string{"CAP-RGB-056"}, whyNoQt) }
func TestBackgroundClickFocusesQtField(t *testing.T) { skip(t, []string{"CAP-RGB-058"}, whyNoQt) }
func TestQtBackgroundTypeKeepsField(t *testing.T)    { skip(t, []string{"CAP-RGB-064"}, whyNoQt) }
func TestKittyBackgroundType(t *testing.T)           { skip(t, []string{"CAP-RGB-036"}, whyNoKitty) }
func TestLongChromeTitleTarget(t *testing.T)         { skip(t, []string{"CAP-RGB-071"}, whyNoChrome) }
func TestLaunchChromeProfileArgs(t *testing.T)       { skip(t, []string{"CAP-RGB-072"}, whyNoChrome) }
func TestChromeBackgroundType620(t *testing.T)       { skip(t, []string{"CAP-RGA-013"}, whyNoChrome) }
func TestRecordChromePage(t *testing.T)              { skip(t, []string{"CAP-RGB-082"}, whyNoChrome) }
func TestLockedScreenType(t *testing.T)              { skip(t, []string{"CAP-RGB-065"}, whyLock) }
func TestConfirmRestoresFront(t *testing.T)          { skip(t, []string{"CAP-RGA-079"}, whyConfirm) }
func TestFirstRunCaptureNoRestart(t *testing.T)      { skip(t, []string{"CAP-RGA-091"}, whyPackaging) }
func TestPermissionGuideNaming(t *testing.T)         { skip(t, []string{"CAP-RGA-092"}, whyPackaging) }
func TestKeyAfterDriverActive(t *testing.T)          { skip(t, []string{"CAP-RGA-020"}, whyDriverApp) }
