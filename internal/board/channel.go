package board

import (
	"strings"
	"unicode/utf8"
)

// A Channel is a place outside Fennec whose messages become requests of the
// project, such as a Discord channel or the web widget (FEN-217, FEN-228).
// There is no triage step: each message is a new request with the channel's
// tags, handled by the project's agent as usual. The ingest service
// (FEN-224) reads these; Settings › Channels edits them.
type Channel struct {
	// ID is unique within the project, e.g. "discord" or "discord-2".
	ID string `json:"id"`
	// Kind is the adapter: one of ChannelKinds.
	Kind string `json:"kind"`
	// Source says where messages come from, for people: a server and its
	// channels, a site's domains. Empty until the connection is set up.
	Source string `json:"source,omitempty"`
	// Tags are project tag ids given to every request from this channel:
	// Discord #bug-report → feedback + discord, an internal Slack → slack.
	Tags []string `json:"tags"`
	// On is false while the channel is paused: nothing is taken in.
	On bool `json:"on"`
}

// ChannelKinds are the adapters a channel can use.
var ChannelKinds = map[string]bool{"discord": true, "widget": true, "slack": true, "x": true}

const maxChannels = 20

func validChannels(channels []Channel, tags map[string]bool) error {
	if len(channels) > maxChannels {
		return ErrInvalid
	}
	ids := map[string]bool{}
	for _, ch := range channels {
		if !tagID.MatchString(ch.ID) || ids[ch.ID] || !ChannelKinds[ch.Kind] ||
			utf8.RuneCountInString(ch.Source) > 200 || strings.ContainsAny(ch.Source, "\x00\r\n") || len(ch.Tags) > 12 {
			return ErrInvalid
		}
		ids[ch.ID] = true
		for _, t := range ch.Tags {
			if !tags[t] {
				return ErrInvalid
			}
		}
	}
	return nil
}

// pruneChannelTags drops channel tags the project no longer has, so deleting
// a tag in Settings › General doesn't make the project invalid.
func pruneChannelTags(channels []Channel, tags []Tag) []Channel {
	if len(channels) == 0 {
		return channels
	}
	have := map[string]bool{}
	for _, t := range tags {
		have[t.ID] = true
	}
	out := make([]Channel, len(channels))
	for i, ch := range channels {
		kept := []string{}
		for _, t := range ch.Tags {
			if have[t] {
				kept = append(kept, t)
			}
		}
		ch.Tags = kept
		out[i] = ch
	}
	return out
}
