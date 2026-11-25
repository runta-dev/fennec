// Package cloudarchive exports a cloud computer's queues and sessions to one
// archive file and imports it on another cloud computer (THI-165).
//
// The user moves their core assets by hand: export on the old computer, import
// on the new one. Credentials, agent binaries and code repositories are not
// part of it. The archive is a zstd-compressed tar stream:
//
//	manifest.json                       what the archive holds (read first)
//	board/journal.json                  every board mutation (projects, requests)
//	board/attachments/<sha256>.<ext>    request images
//	board/knowledge/<queue>/<path>      each queue's knowledge bundle
//	sessions/<id>/launch.json           ~/.local/fennec/launches/<id>.json
//	sessions/<id>/workdir/<path>        ~/.fen/<id>, without build output
//	sessions/<id>/conversations/<n>/bundle.json
//	sessions/<id>/conversations/<n>/files/<path>
//	checksums.json                      sha256 of every entry above (read last)
//
// Paths inside are relative to the home folder, so an archive from a Runta
// computer (/root) imports on a self-hosted one (/home/fennec) and back.
package cloudarchive

import (
	"regexp"
	"time"
)

const (
	Format    = "fennec-cloud-archive"
	Version   = 1
	Extension = ".fenarchive"

	manifestName  = "manifest.json"
	checksumsName = "checksums.json"
	journalName   = "board/journal.json"
)

// Manifest opens the archive, so a preview needs only the first entry.
type Manifest struct {
	Format     string    `json:"format"`
	Version    int       `json:"version"`
	Created    time.Time `json:"created"`
	SourceHome string    `json:"source_home"`
	// Sessions is false for a queues-only archive.
	Sessions bool   `json:"sessions"`
	Counts   Counts `json:"counts"`
}

// Counts are taken before the files are written. Conversations are counted
// in the trailer, because they are found while writing.
type Counts struct {
	Projects    int   `json:"projects"`
	Requests    int   `json:"requests"`
	Events      int   `json:"events"`
	Attachments int   `json:"attachments"`
	Knowledge   int   `json:"knowledge"`
	Sessions    int   `json:"sessions"`
	Launches    int   `json:"launches"`
	Bytes       int64 `json:"bytes"`
}

// Trailer closes the archive. Import rejects an archive whose entries do not
// match it exactly.
type Trailer struct {
	Files         map[string]string `json:"files"`
	Conversations int               `json:"conversations"`
	Skipped       []Skip            `json:"skipped,omitempty"`
}

// Skip is a file the export left out, and why.
type Skip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Result is what an import changed on the target computer.
type Result struct {
	Manifest            Manifest `json:"manifest"`
	ProjectsAdded       int      `json:"projects_added"`
	RequestsAdded       int      `json:"requests_added"`
	RequestsExisting    int      `json:"requests_existing"`
	EventsApplied       int      `json:"events_applied"`
	AttachmentsAdded    int      `json:"attachments_added"`
	KnowledgeAdded      int      `json:"knowledge_added"`
	KnowledgeUnchanged  int      `json:"knowledge_unchanged"`
	KnowledgeConflicts  []string `json:"knowledge_conflicts,omitempty"`
	SessionsRestored    int      `json:"sessions_restored"`
	SessionsExisting    int      `json:"sessions_existing"`
	LaunchesRestored    int      `json:"launches_restored"`
	ConversationsCopied int      `json:"conversations_copied"`
	ConversationsFailed int      `json:"conversations_failed"`
	Skipped             []Skip   `json:"skipped,omitempty"`
}

var (
	sessionID    = regexp.MustCompile(`^(?:[a-z]{3,12}-[a-z]{3,12}|[a-f0-9]{32})$`)
	attachmentID = regexp.MustCompile(`^[a-f0-9]{64}\.(?:png|jpg)$`)
)

// excludedDirectories are rebuilt from source and never worth carrying.
var excludedDirectories = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, "target": true, ".cache": true, ".next": true,
}

// maxFile skips single files that would dominate the archive.
const maxFile = 1 << 30
