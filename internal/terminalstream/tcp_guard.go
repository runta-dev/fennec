package terminalstream

// CapabilityTCPOutputGuard means this resident's broker observes its actual
// SSH TCP socket and pauses bulk output when unsent bytes accumulate.
const CapabilityTCPOutputGuard = "tcp_output_guard_v1"
