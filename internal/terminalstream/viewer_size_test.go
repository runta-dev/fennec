package terminalstream

import "testing"

// FEN-37: a hidden viewer's 0x0 or 1x1 must not shrink the agent's terminal.
func TestViewerSizeIgnoresDegenerateSizes(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {1, 1}, {1, 2}, {4, 200}, {120, 19}} {
		if _, _, ok := viewerSize(size[0], size[1]); ok {
			t.Fatalf("viewerSize(%d, %d) accepted", size[0], size[1])
		}
	}
	if rows, columns, ok := viewerSize(40, 120); !ok || rows != 40 || columns != 120 {
		t.Fatalf("viewerSize(40, 120) = %d, %d, %v", rows, columns, ok)
	}
	if rows, columns, ok := viewerSize(5000, 5000); !ok || rows != 1000 || columns != 1000 {
		t.Fatalf("viewerSize(5000, 5000) = %d, %d, %v", rows, columns, ok)
	}
}
