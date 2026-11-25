package agentcmd

// BinaryName keeps product IDs separate from executable names. Never intercept
// Cursor's editor (`cursor`) or the ambiguous `agent` alias shared by vendors.
func BinaryName(id string) string {
	if id == "cursor" {
		return "cursor-agent"
	}
	return id
}

// NativeOnly agents use their own accounts even when Magpie serves other agents.
// agy is one by choice: Magpie would serve it by reverse-proxying Google's Gemini
// service, which can get the Google account suspended (THI-123).
func NativeOnly(id string) bool { return id == "cursor" || id == "agy" }
