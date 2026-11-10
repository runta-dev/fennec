package launcher

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReceiptRoundtripAndPermissions(t *testing.T) {
	root := t.TempDir()
	r := LaunchReceipt{RuntimeID: "01a1b016-11bc-7e63-b3d6-c4955e17b73b", Address: "t7", SessionID: strings.Repeat("a", 32), Agent: "codex"}
	if err := SaveReceipt(root, r); err != nil {
		t.Fatal(err)
	}
	got, err := ReadReceipt(root, r.SessionID)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatal(got, err)
	}
	info, _ := os.Stat(filepath.Join(root, r.SessionID+".json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("receipt permissions")
	}
}
func TestReceiptRejectsTraversal(t *testing.T) {
	if _, err := ReadReceipt(t.TempDir(), "../elsewhere"); err == nil {
		t.Fatal("traversal accepted")
	}
}
