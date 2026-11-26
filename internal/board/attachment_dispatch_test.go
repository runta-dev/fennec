package board

import (
	"os"
	"testing"
)

func TestMissingImageDoesNotBlockReadyRequest(t *testing.T) {
	a, b := testStore(t), testStore(t)
	img, err := a.SaveAttachment(attachmentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := a.CreateProject(Project{Name: "Ready work", Queue: 10})
	first, err := a.CreateRequestWithAttachments(p.ID, "me", "Pending image", nil, "", []Attachment{img})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.CreateRequest(p.ID, "me", "Ready text", "", "")
	if err != nil {
		t.Fatal(err)
	}
	exchange(t, a, b)
	if _, err := b.Claim(p.ID, second.Number, "ready-text", "codex"); err != nil {
		t.Fatal("pending image blocked ready work", err)
	}
	for offset := int64(0); offset < img.Size; {
		chunk, err := a.ReadAttachmentChunkSize(img.ID, offset, 32<<10)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.WriteAttachmentChunk(chunk); err != nil {
			t.Fatal(err)
		}
		offset += int64(len(chunk.Data))
	}
	if _, err := b.Claim(p.ID, first.Number, "ready-image", "codex"); err != nil {
		t.Fatal(err)
	}
}

func TestImportCompressesBeforeHashingAndReplicatesWebP(t *testing.T) {
	a, b := testStore(t), testStore(t)
	path := t.TempDir() + "/source.png"
	if err := os.WriteFile(path, attachmentFixture(t), 0600); err != nil {
		t.Fatal(err)
	}
	img, err := a.ImportAttachment(path)
	if err != nil {
		t.Fatal(err)
	}
	if img.MediaType != "image/webp" || !a.HasAttachment(img) {
		t.Fatal("not WebP", img.MediaType)
	}
	for offset := int64(0); offset < img.Size; {
		chunk, err := a.ReadAttachmentChunkSize(img.ID, offset, 32<<10)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.WriteAttachmentChunk(chunk); err != nil {
			t.Fatal(err)
		}
		offset += int64(len(chunk.Data))
	}
	if !b.HasAttachment(img) {
		t.Fatal("WebP did not survive replication")
	}
}
