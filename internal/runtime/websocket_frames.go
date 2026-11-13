package runtime

// ParseWebSocketFrame reads one complete frame from buffer, unmasking client
// frames. It returns size 0 when the buffer does not yet hold a whole frame.
func ParseWebSocketFrame(buffer []byte) (opcode byte, fin bool, payload []byte, size int, err error) {
	frame, size, err := parseWebSocketFrame(buffer)
	return frame.opcode, frame.fin, frame.payload, size, err
}

// EncodeWebSocketFrame returns one final frame; server frames are unmasked.
func EncodeWebSocketFrame(opcode byte, payload []byte, masked bool) []byte {
	return encodeWebSocketFrame(opcode, payload, masked)
}
