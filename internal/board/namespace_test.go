package board

import "testing"

func TestNamespacesIsolateImagesJournalsAndThreadMirrors(t *testing.T) {
	home := t.TempDir()
	a := Store{Home: home, Role: "local", Namespace: "computer-a"}
	b := Store{Home: home, Role: "local", Namespace: "computer-b"}
	image, err := a.SaveAttachment(attachmentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if b.HasAttachment(image) {
		t.Fatal("another computer can read the image")
	}
	if _, err := b.AttachmentURL(image.ID); err == nil {
		t.Fatal("another computer resolved the image URL")
	}
	for _, s := range []Store{a, b} {
		if _, err := s.AddStarterProject("Tasks", ""); err != nil {
			t.Fatal(err)
		}
	}
	x, err := a.Sync(Exchange{})
	if err != nil {
		t.Fatal(err)
	}
	y, err := b.Sync(Exchange{})
	if err != nil {
		t.Fatal(err)
	}
	for origin := range x.Seen {
		if _, ok := y.Seen[origin]; ok {
			t.Fatal("computers share a replication cursor")
		}
	}
	if a.ThreadsPath(StarterProjectID) == b.ThreadsPath(StarterProjectID) {
		t.Fatal("computers share thread mirrors")
	}
	restarted := Store{Home: home, Role: "local", Namespace: "computer-a"}
	if !restarted.HasAttachment(image) {
		t.Fatal("switching back lost images")
	}
}
