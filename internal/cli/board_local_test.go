package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// A signed-out desktop must read and write durable projects without even
// having an account service or a cloud channel available.
func TestLocalBoardOfflinePersistence(t *testing.T) {
	home := t.TempDir()
	client := boardClient{s: &daemonServices{home: home}, local: true}
	raw, err := client.call(context.Background(), http.MethodPost, "/v1/projects", map[string]string{"name": "Offline work"})
	if err != nil {
		t.Fatal(err)
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &project); err != nil || project.ID == "" {
		t.Fatalf("invalid project: %s, %v", raw, err)
	}
	_, err = client.call(context.Background(), http.MethodPost, "/v1/projects/"+project.ID+"/requests", map[string]string{"title": "Keep working offline", "author": "me"})
	if err != nil {
		t.Fatal(err)
	}
	restarted := boardClient{s: &daemonServices{home: home}, local: true}
	raw, err = restarted.call(context.Background(), http.MethodGet, "/v1/projects/"+project.ID+"/requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	var requests []struct {
		Title string `json:"title"`
	}
	if err = json.Unmarshal(raw, &requests); err != nil || len(requests) != 1 || requests[0].Title != "Keep working offline" {
		t.Fatalf("lost offline request: %s, %v", raw, err)
	}
	if _, err = restarted.call(context.Background(), http.MethodGet, "/v1/../private", nil); err == nil {
		t.Fatal("accepted invalid path")
	}
}
