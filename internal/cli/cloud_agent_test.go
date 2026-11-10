package cli

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestCloudAgentLaunchThenAttachPreservesIdentity(t *testing.T) {
	request := launchProjectRequest{RuntimeID: "runtime", SessionID: "stable-id", Agent: "codex", Directory: "/project", Arguments: []string{"--model", "p/m"}}
	attached := false
	err := cloudAgentWith(request, func(r io.Reader, w io.Writer) error {
		var got launchProjectRequest
		json.NewDecoder(r).Decode(&got)
		if !reflect.DeepEqual(got, request) {
			t.Fatal("launch envelope changed")
		}
		_, e := io.WriteString(w, `{"address":"t7","state":"live"}`)
		return e
	}, func(runtime, address, session string) error {
		attached = true
		if runtime != "runtime" || address != "t7" || session != "stable-id" {
			t.Fatal("wrong attach target")
		}
		return nil
	})
	if err != nil || !attached {
		t.Fatal(err)
	}
}
func TestCloudAgentNeverAttachesAfterUnknownLaunch(t *testing.T) {
	for _, failure := range []bool{true, false} {
		called := false
		err := cloudAgentWith(launchProjectRequest{}, func(_ io.Reader, w io.Writer) error {
			if failure {
				return errors.New("sync failed")
			}
			io.WriteString(w, `{"address":"t7","state":"finished"}`)
			return nil
		}, func(string, string, string) error { called = true; return nil })
		if err == nil || called {
			t.Fatal("unconfirmed launch attached")
		}
	}
}
