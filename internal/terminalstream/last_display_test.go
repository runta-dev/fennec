package terminalstream

import (
	"reflect"
	"testing"
)

func TestLastDisplayRemembersViewerColors(t *testing.T) {
	home := t.TempDir()
	if LastDisplay(home) != nil {
		t.Fatal("no viewer yet must give no display")
	}
	RememberDisplay(home, &DisplayFacts{Foreground: "#00ff00", Background: "nonsense"})
	if LastDisplay(home) != nil {
		t.Fatal("invalid colors must be ignored")
	}
	RememberDisplay(home, &DisplayFacts{Foreground: "#1f2328", Background: "#ffffff", Cursor: "#1f2328", CellWidth: 16, CellHeight: 34})
	want := map[string]any{"fg": "#1f2328", "bg": "#ffffff", "cursor": "#1f2328"}
	if got := LastDisplay(home); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	RememberDisplay(home, &DisplayFacts{Foreground: "#ffffff", Background: "#282c34"})
	if got := LastDisplay(home); got["bg"] != "#282c34" || got["cursor"] != nil {
		t.Fatalf("latest viewer must win: %v", got)
	}
}
