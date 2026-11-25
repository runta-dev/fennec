package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Timeline is the single source of truth shared by the Go renderer (audio,
// voice-over placement, sound effects) and the canvas scenes in web/trailer.js.
// Scene lengths stretch to fit the recorded voice-over, so pictures and sound
// stay in sync whenever a line is re-recorded.
type Timeline struct {
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       int     `json:"fps"`
	Crossfade float64 `json:"crossfade"`
	Voice     Voice   `json:"voice"`
	Scenes    []Scene `json:"scenes"`

	// Resolved fields.
	Duration float64 `json:"duration"`
	Captions bool    `json:"captions"`
}

type Voice struct {
	VoiceID         string  `json:"voiceId"`
	VoiceName       string  `json:"voiceName"`
	ModelID         string  `json:"modelId"`
	Stability       float64 `json:"stability"`
	SimilarityBoost float64 `json:"similarityBoost"`
	Style           float64 `json:"style"`
	Speed           float64 `json:"speed"`
}

type Scene struct {
	ID     string  `json:"id"`
	MinDur float64 `json:"minDur"`
	Lead   float64 `json:"lead"`
	Tail   float64 `json:"tail"`
	Lines  []Line  `json:"lines"`
	Cues   []Cue   `json:"cues"`

	// Resolved, in seconds. Start is absolute; line and cue times are
	// relative to the scene start.
	Start float64 `json:"start"`
	Dur   float64 `json:"dur"`
}

type Line struct {
	ID   string  `json:"id"`
	Text string  `json:"text"`
	Gap  float64 `json:"gap,omitempty"`

	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Recorded bool    `json:"recorded"`
}

type Cue struct {
	Name     string  `json:"name"`
	Line     string  `json:"line,omitempty"`
	Anchor   string  `json:"anchor,omitempty"` // "start" (default) or "end" of Line
	Offset   float64 `json:"offset"`
	SFX      string  `json:"sfx,omitempty"`
	Text     string  `json:"text,omitempty"`
	CPS      float64 `json:"cps,omitempty"`
	Count    int     `json:"count,omitempty"`
	Interval float64 `json:"interval,omitempty"`

	At float64 `json:"at"`
}

// voMeta is written next to each recorded line by the vo command.
type voMeta struct {
	Text     string  `json:"text"`
	VoiceID  string  `json:"voiceId"`
	ModelID  string  `json:"modelId"`
	Duration float64 `json:"duration"`
}

func loadTimeline(root string) (*Timeline, error) {
	b, err := os.ReadFile(filepath.Join(root, "timeline.json"))
	if err != nil {
		return nil, err
	}
	var tl Timeline
	if err := json.Unmarshal(b, &tl); err != nil {
		return nil, fmt.Errorf("timeline.json: %w", err)
	}
	return &tl, nil
}

// estimateSpeech guesses how long an unrecorded line takes to say, so the
// trailer can be previewed before any voice-over exists.
func estimateSpeech(text string) float64 {
	words := len(strings.Fields(text))
	commas := strings.Count(text, ",") + strings.Count(text, ".") - 1
	if commas < 0 {
		commas = 0
	}
	return float64(words)/2.55 + 0.18*float64(commas) + 0.25
}

// resolve lays scenes end to end and fills in line and cue times.
func (tl *Timeline) resolve(root string) error {
	t := 0.0
	for si := range tl.Scenes {
		sc := &tl.Scenes[si]
		sc.Start = t
		cursor := sc.Lead
		for li := range sc.Lines {
			ln := &sc.Lines[li]
			if li > 0 {
				gap := ln.Gap
				if gap == 0 {
					gap = 0.35
				}
				cursor += gap
			}
			dur := estimateSpeech(ln.Text)
			if m, ok := readVOMeta(root, ln.ID); ok && m.Text == ln.Text {
				dur = m.Duration
				ln.Recorded = true
			}
			ln.Start = cursor
			ln.End = cursor + dur
			cursor = ln.End
		}
		sc.Dur = cursor + sc.Tail
		if sc.Dur < sc.MinDur {
			sc.Dur = sc.MinDur
		}
		for ci := range sc.Cues {
			c := &sc.Cues[ci]
			base := 0.0
			if c.Line != "" {
				ln := sc.line(c.Line)
				if ln == nil {
					return fmt.Errorf("scene %s cue %s: unknown line %s", sc.ID, c.Name, c.Line)
				}
				base = ln.Start
				if c.Anchor == "end" {
					base = ln.End
				}
			}
			c.At = base + c.Offset
		}
		t += sc.Dur
	}
	tl.Duration = t
	return nil
}

func (sc *Scene) line(id string) *Line {
	for i := range sc.Lines {
		if sc.Lines[i].ID == id {
			return &sc.Lines[i]
		}
	}
	return nil
}

func readVOMeta(root, id string) (voMeta, bool) {
	var m voMeta
	b, err := os.ReadFile(filepath.Join(root, "assets", "vo", id+".json"))
	if err != nil {
		return m, false
	}
	if json.Unmarshal(b, &m) != nil || m.Duration <= 0 {
		return m, false
	}
	return m, true
}
