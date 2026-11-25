package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Timeline is the single source of truth shared by the Go renderer (music,
// sound effects, voice-over) and the canvas scenes in web/trailer.js.
//
// The whole script is recorded as one continuous ElevenLabs take. Its
// character timestamps place every word; each scene starts just before its
// first word, so cuts follow the voice and the read never restarts.
type Timeline struct {
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FPS       int     `json:"fps"`
	BPM       float64 `json:"bpm"`
	GridScene string  `json:"gridScene"`
	VOStart   float64 `json:"voStart"`
	EndHold   float64 `json:"endHold"`
	Voice     Voice   `json:"voice"`
	Scenes    []Scene `json:"scenes"`

	// Resolved fields.
	Duration float64 `json:"duration"`
	Beat     float64 `json:"beat"`
	Grid0    float64 `json:"grid0"` // first downbeat of the groove: the start of GridScene
	Recorded bool    `json:"recorded"`
	Captions bool    `json:"captions"`

	splits []voSplit
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
	ID   string   `json:"id"`
	Text string   `json:"text"`
	Lead *float64 `json:"lead,omitempty"`
	// PauseBefore opens a gap in the take before this scene's first word: the
	// recording is split between sentences, so the read stays one take.
	PauseBefore float64 `json:"pauseBefore,omitempty"`
	Cues        []Cue   `json:"cues"`

	// Resolved. Start is absolute; everything else is relative to it.
	Start    float64 `json:"start"`
	Dur      float64 `json:"dur"`
	SegStart float64 `json:"segStart"`
	SegEnd   float64 `json:"segEnd"`
	Words    []Word  `json:"words"`
}

type Word struct {
	W     string  `json:"w"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Cue struct {
	Name     string  `json:"name"`
	Word     string  `json:"word,omitempty"`
	Anchor   string  `json:"anchor,omitempty"` // "start" (default) or "end" of Word
	Offset   float64 `json:"offset"`
	SFX      string  `json:"sfx,omitempty"`
	Text     string  `json:"text,omitempty"`
	CPS      float64 `json:"cps,omitempty"`
	Count    int     `json:"count,omitempty"`
	Interval float64 `json:"interval,omitempty"`

	At float64 `json:"at"`
}

// voSplit shifts the take by Shift seconds from take time At onwards.
type voSplit struct{ At, Shift float64 }

// voTake is the recorded voice-over: assets/vo/take.json next to take.mp3.
type voTake struct {
	Text     string    `json:"text"`
	VoiceID  string    `json:"voiceId"`
	ModelID  string    `json:"modelId"`
	Chars    []string  `json:"chars"`
	Starts   []float64 `json:"starts"`
	Ends     []float64 `json:"ends"`
	Duration float64   `json:"duration"`
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

// script is the full voice-over text, recorded in one request.
func (tl *Timeline) script() string {
	parts := make([]string, len(tl.Scenes))
	for i, sc := range tl.Scenes {
		parts[i] = strings.TrimSpace(sc.Text)
	}
	return strings.Join(parts, " ")
}

func readTake(root string) (voTake, bool) {
	var t voTake
	b, err := os.ReadFile(filepath.Join(root, "assets", "vo", "take.json"))
	if err != nil || json.Unmarshal(b, &t) != nil {
		return t, false
	}
	return t, len(t.Starts) > 0
}

// estimateTake fakes character timings so the trailer can be previewed
// before a take exists or after the script changed.
func estimateTake(text string) voTake {
	t := voTake{Text: text}
	at := 0.0
	for _, r := range text {
		d := 0.058
		t.Chars = append(t.Chars, string(r))
		t.Starts = append(t.Starts, at)
		at += d
		t.Ends = append(t.Ends, at)
		switch r {
		case ',':
			at += 0.18
		case '.':
			at += 0.38
		}
	}
	t.Duration = at
	return t
}

func normWord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (tl *Timeline) resolve(root string) error {
	text := tl.script()
	take, ok := readTake(root)
	tl.Recorded = ok && take.Text == text
	if !tl.Recorded {
		take = estimateTake(text)
	}
	if len(take.Starts) != len([]rune(text)) {
		return fmt.Errorf("voice-over alignment has %d characters, script has %d", len(take.Starts), len([]rune(text)))
	}
	tl.Beat = 60 / tl.BPM

	// Words with absolute times.
	runes := []rune(text)
	off := 0
	shift := 0.0
	prevEnd := 0.0
	tl.splits = nil
	absWords := make([][]Word, len(tl.Scenes))
	for si, sc := range tl.Scenes {
		firstWord := true
		seg := []rune(strings.TrimSpace(sc.Text))
		i := off
		end := off + len(seg)
		for i < end {
			for i < end && runes[i] == ' ' {
				i++
			}
			j := i
			for j < end && runes[j] != ' ' {
				j++
			}
			if j > i {
				first, last := i, j-1
				for first < last && !unicode.IsLetter(runes[first]) && !unicode.IsDigit(runes[first]) {
					first++
				}
				if firstWord {
					firstWord = false
					if si > 0 && sc.PauseBefore > 0 {
						shift += sc.PauseBefore
						tl.splits = append(tl.splits, voSplit{At: (prevEnd + take.Starts[first]) / 2, Shift: sc.PauseBefore})
					}
				}
				prevEnd = take.Ends[last]
				absWords[si] = append(absWords[si], Word{
					W:     string(runes[i:j]),
					Start: tl.VOStart + shift + take.Starts[first],
					End:   tl.VOStart + shift + take.Ends[last],
				})
			}
			i = j
		}
		off = end + 1
	}

	for si := range tl.Scenes {
		sc := &tl.Scenes[si]
		ws := absWords[si]
		if len(ws) == 0 {
			return fmt.Errorf("scene %s has no text", sc.ID)
		}
		lead := 0.12
		if sc.Lead != nil {
			lead = *sc.Lead
		}
		sc.Start = ws[0].Start - lead
		if si == 0 {
			sc.Start = 0
		}
	}
	for si := range tl.Scenes {
		sc := &tl.Scenes[si]
		ws := absWords[si]
		if si+1 < len(tl.Scenes) {
			sc.Dur = tl.Scenes[si+1].Start - sc.Start
		} else {
			sc.Dur = ws[len(ws)-1].End - sc.Start + tl.EndHold
		}
		sc.Words = make([]Word, len(ws))
		for i, w := range ws {
			sc.Words[i] = Word{W: w.W, Start: w.Start - sc.Start, End: w.End - sc.Start}
		}
		sc.SegStart = sc.Words[0].Start
		sc.SegEnd = sc.Words[len(ws)-1].End
		for ci := range sc.Cues {
			c := &sc.Cues[ci]
			base := sc.SegStart
			if c.Word != "" {
				w := sc.word(c.Word)
				if w == nil {
					return fmt.Errorf("scene %s cue %s: no word %q", sc.ID, c.Name, c.Word)
				}
				base = w.Start
				if c.Anchor == "end" {
					base = w.End
				}
			}
			c.At = base + c.Offset
		}
		if sc.ID == tl.GridScene {
			tl.Grid0 = sc.Start
		}
	}
	last := tl.Scenes[len(tl.Scenes)-1]
	tl.Duration = last.Start + last.Dur
	return nil
}

func (sc *Scene) word(w string) *Word {
	n := normWord(w)
	for i := range sc.Words {
		if normWord(sc.Words[i].W) == n {
			return &sc.Words[i]
		}
	}
	return nil
}

func (tl *Timeline) scene(id string) *Scene {
	for i := range tl.Scenes {
		if tl.Scenes[i].ID == id {
			return &tl.Scenes[i]
		}
	}
	return nil
}

func (sc *Scene) cue(name string) *Cue {
	for i := range sc.Cues {
		if sc.Cues[i].Name == name {
			return &sc.Cues[i]
		}
	}
	return nil
}

// gridAfter returns the n-th grid point (step q) at or after absolute time t.
// web/trailer.js has the same function; keep them identical.
func (tl *Timeline) gridAfter(t, q float64, n int) float64 {
	k := math.Ceil((t-tl.Grid0)/q - 1e-9)
	return tl.Grid0 + (k+float64(n))*q
}
