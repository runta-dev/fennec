package project

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fennec/internal/sessionid"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

type UploadResult struct {
	Directory string `json:"directory"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
}

// Upload publishes only a locally verified snapshot. Build before opening the
// remote receiver so a local validation failure cannot publish a partial archive.
func Upload(ctx context.Context, client *ssh.Client, id string, manifest Manifest) (UploadResult, error) {
	if !sessionid.Valid(id) {
		return UploadResult{}, fmt.Errorf("invalid snapshot identity")
	}
	// Snapshot transfer IDs are internal archive keys, independent of session names.
	if strings.Contains(id, "-") {
		sum := sha256.Sum256([]byte(id))
		id = fmt.Sprintf("%x", sum[:16])
	}
	archive, err := os.CreateTemp("", "fennec-project-*.tar")
	if err != nil {
		return UploadResult{}, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	if err = WriteArchive(ctx, manifest, archive); err != nil {
		return UploadResult{}, err
	}
	if _, err = archive.Seek(0, 0); err != nil {
		return UploadResult{}, err
	}
	// Do not disclose the local absolute project root to the cloud receiver.
	wire := manifest
	wire.Root = ""
	wire.Excluded = nil
	header, err := json.Marshal(wire)
	if err != nil {
		return UploadResult{}, err
	}
	if len(header) >= 4<<20 {
		return UploadResult{}, fmt.Errorf("manifest exceeds receiver limit")
	}
	session, err := client.NewSession()
	if err != nil {
		return UploadResult{}, err
	}
	defer session.Close()
	session.Stdin = io.MultiReader(strings.NewReader(string(header)+"\n"), archive)
	stop := context.AfterFunc(ctx, func() { session.Close() })
	defer stop()
	quoted := "'" + strings.ReplaceAll(Receiver, "'", "'\"'\"'") + "'"
	raw, err := session.Output("python3 -c " + quoted + " " + id)
	if err != nil {
		return UploadResult{}, fmt.Errorf("project upload unconfirmed; inspect snapshot before retrying")
	}
	var result UploadResult
	if len(raw) > 65536 || json.Unmarshal(raw, &result) != nil || result.Files != len(manifest.Files) || result.Bytes != manifest.Bytes || !strings.HasPrefix(result.Directory, "/") {
		return UploadResult{}, fmt.Errorf("invalid project upload receipt")
	}
	return result, nil
}
