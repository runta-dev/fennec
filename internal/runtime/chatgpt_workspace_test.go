package runtime

import (
	"net"
	"path/filepath"
	"testing"

	"fennec/internal/sessionid"
)

// ChatGPT's cloud workspaces use the same word-pair names as every other
// Fennec session (~/.fen/desert-true) and stay stable per Mac directory.
func TestChatGPTWorkspacesUseSessionNames(t *testing.T) {
	home := t.TempDir()
	open := func() *chatgptPathFilter {
		local, _ := net.Pipe()
		t.Cleanup(func() { local.Close() })
		return NewChatGPTPathFilter(local, home, "").(*chatgptPathFilter)
	}
	first := open()
	cloud, err := first.workspace("/Users/me/repos/mockuplabs")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(cloud) != filepath.Join(home, ".fen") || !sessionid.Memorable(filepath.Base(cloud)) {
		t.Fatalf("workspace %q is not ~/.fen/<word>-<word>", cloud)
	}
	// Another connection (ChatGPT reconnected) reuses it and maps it back.
	second := open()
	again, err := second.workspace("/Users/me/repos/mockuplabs")
	if err != nil || again != cloud {
		t.Fatalf("workspace not reused: %q %v", again, err)
	}
	third := open()
	if mac, ok := mapBack(third, cloud); !ok || mac != "/Users/me/repos/mockuplabs" {
		t.Fatalf("cloud path not mapped back: %q %v", mac, ok)
	}
	other, err := first.workspace("/Users/me/repos/other")
	if err != nil || other == cloud {
		t.Fatalf("distinct directories share a workspace: %q %v", other, err)
	}
}

func mapBack(filter *chatgptPathFilter, cloud string) (string, bool) {
	out := filter.toMac([]byte(`{"id":1,"result":{"cwd":"` + cloud + `"}}`))
	var mac string
	if n := len(`{"id":1,"result":{"cwd":"`); len(out) > n {
		mac = string(out[n : len(out)-3])
	}
	return mac, mac != cloud && mac != ""
}
