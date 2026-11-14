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
func NativeOnly(id string) bool { return id == "cursor" }
