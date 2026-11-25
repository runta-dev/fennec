package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// recordVO asks ElevenLabs to read every line in the timeline and stores the
// result in assets/vo/<line>.mp3 with a <line>.json sidecar. Lines whose text
// and voice are unchanged are skipped, so re-running only pays for edits.
// The API key is read from ELEVENLABS_API_KEY and never written anywhere.
func recordVO(root string, force bool, only map[string]bool) error {
	key := os.Getenv("ELEVENLABS_API_KEY")
	if key == "" {
		return fmt.Errorf("ELEVENLABS_API_KEY is not set")
	}
	tl, err := loadTimeline(root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "assets", "vo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var all []Line
	for _, sc := range tl.Scenes {
		all = append(all, sc.Lines...)
	}
	for i, ln := range all {
		if len(only) > 0 && !only[ln.ID] {
			continue
		}
		if m, ok := readVOMeta(root, ln.ID); ok && !force && m.Text == ln.Text &&
			m.VoiceID == tl.Voice.VoiceID && m.ModelID == tl.Voice.ModelID {
			fmt.Printf("%s: up to date (%.2fs)\n", ln.ID, m.Duration)
			continue
		}
		prev, next := "", ""
		if i > 0 {
			prev = all[i-1].Text
		}
		if i+1 < len(all) {
			next = all[i+1].Text
		}
		audio, dur, err := synthesize(key, tl.Voice, ln.Text, prev, next)
		if err != nil {
			return fmt.Errorf("%s: %w", ln.ID, err)
		}
		if err := os.WriteFile(filepath.Join(dir, ln.ID+".mp3"), audio, 0o644); err != nil {
			return err
		}
		meta, _ := json.MarshalIndent(voMeta{Text: ln.Text, VoiceID: tl.Voice.VoiceID, ModelID: tl.Voice.ModelID, Duration: dur}, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, ln.ID+".json"), append(meta, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s: recorded %.2fs  %q\n", ln.ID, dur, ln.Text)
	}
	return nil
}

func synthesize(key string, v Voice, text, prev, next string) ([]byte, float64, error) {
	body := map[string]any{
		"text":          text,
		"model_id":      v.ModelID,
		"previous_text": prev,
		"next_text":     next,
		"voice_settings": map[string]any{
			"stability":         v.Stability,
			"similarity_boost":  v.SimilarityBoost,
			"style":             v.Style,
			"speed":             v.Speed,
			"use_speaker_boost": true,
		},
	}
	b, _ := json.Marshal(body)
	url := "https://api.elevenlabs.io/v1/text-to-speech/" + v.VoiceID + "/with-timestamps?output_format=mp3_44100_128"
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("xi-api-key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		if len(raw) > 400 {
			raw = raw[:400]
		}
		return nil, 0, fmt.Errorf("ElevenLabs %s: %s", resp.Status, raw)
	}
	var out struct {
		Audio     string `json:"audio_base64"`
		Alignment struct {
			Ends []float64 `json:"character_end_times_seconds"`
		} `json:"alignment"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, 0, err
	}
	audio, err := base64.StdEncoding.DecodeString(out.Audio)
	if err != nil {
		return nil, 0, err
	}
	dur := 0.0
	if n := len(out.Alignment.Ends); n > 0 {
		dur = out.Alignment.Ends[n-1]
	}
	if dur <= 0 {
		return nil, 0, fmt.Errorf("no alignment in response")
	}
	return audio, dur, nil
}
