package cloudops

import (
	"testing"

	"fennec/internal/setup"
)

// The cloud reports Antigravity CLI's migrated sign-in to the Mac's agent
// card, but lists or sets no models for it.
func TestAgyCloudAccountOperationAllowed(t *testing.T) {
	selection := &setup.NativeModelSelection{Agent: "agy"}
	if err := (Operation{Kind: "native_account", NativeModel: selection}).Validate(); err != nil {
		t.Fatal("agy account status rejected:", err)
	}
	for _, kind := range []string{"native_models", "native_model_set"} {
		if (Operation{Kind: kind, NativeModel: selection}).Validate() == nil {
			t.Fatalf("%s accepted for agy", kind)
		}
	}
}
