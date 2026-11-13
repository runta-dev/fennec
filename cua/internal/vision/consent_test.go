package vision

import (
	"errors"
	"strings"
	"testing"
)

func TestConsentReason(t *testing.T) {
	driverText := "Desktop screenshot failed: direct_capture_consent_required: screen capture skipped. Ask the user to run `cua-driver permissions grant`"
	err := explain(&CaptureError{Reason: driverText})
	if !ConsentRequired(err) {
		t.Fatalf("not recognised: %v", err)
	}
	if msg := err.Error(); strings.Contains(msg, "cua-driver permissions grant") || !strings.Contains(msg, "fennec-cua permissions --request") {
		t.Fatalf("message: %s", msg)
	}
	if ConsentRequired(errors.New("direct_capture_consent_required")) || ConsentRequired(&CaptureError{Reason: "no image"}) {
		t.Fatal("other errors taken for consent")
	}
	if e := explain(&CaptureError{Reason: "boom"}); e.Reason != "boom" {
		t.Fatalf("rewrote %q", e.Reason)
	}
}
