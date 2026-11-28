package board

import (
	"encoding/json"
	"testing"
)

func TestProjectChannels(t *testing.T) {
	s := Store{Home: t.TempDir()}
	p, err := s.CreateProject(Project{Name: "Nebula Run", Key: "NR", Color: "#6b7cff"})
	if err != nil {
		t.Fatal(err)
	}
	tags := `"tags":[{"id":"bug","label":"Bug"},{"id":"feedback","label":"Feedback"},{"id":"discord","label":"Discord"},{"id":"slack","label":"Slack"}]`
	p, err = s.UpdateProject(p.ID, json.RawMessage(`{`+tags+`,"channels":[
		{"id":"discord","kind":"discord","source":"Nebula Run · #bug-report","tags":["feedback","discord"],"on":true},
		{"id":"slack","kind":"slack","tags":["slack"],"on":false}]}`))
	if err != nil || len(p.Channels) != 2 || p.Channels[0].Tags[1] != "discord" || p.Channels[1].On {
		t.Fatal(p.Channels, err)
	}
	bad := map[string]string{
		"kind":         `{"channels":[{"id":"a","kind":"fax","tags":[],"on":true}]}`,
		"duplicate id": `{"channels":[{"id":"a","kind":"x","tags":[]},{"id":"a","kind":"x","tags":[]}]}`,
		"id":           `{"channels":[{"id":"A b","kind":"x","tags":[]}]}`,
		"source":       `{"channels":[{"id":"a","kind":"x","source":"a\nb","tags":[]}]}`,
	}
	for name, change := range bad {
		if _, err := s.UpdateProject(p.ID, json.RawMessage(change)); err == nil {
			t.Error(name, "accepted")
		}
	}
	// Deleting a tag drops it from the channels instead of failing.
	p, err = s.UpdateProject(p.ID, json.RawMessage(`{"tags":[{"id":"bug","label":"Bug"},{"id":"discord","label":"Discord"}]}`))
	if err != nil || len(p.Channels[0].Tags) != 1 || p.Channels[0].Tags[0] != "discord" || len(p.Channels[1].Tags) != 0 {
		t.Fatal(p.Channels, err)
	}
	// A channel tag the project doesn't have is dropped too.
	p, err = s.UpdateProject(p.ID, json.RawMessage(`{"channels":[{"id":"x","kind":"x","tags":["nope","bug"],"on":true}]}`))
	if err != nil || len(p.Channels) != 1 || len(p.Channels[0].Tags) != 1 {
		t.Fatal(p.Channels, err)
	}
}
