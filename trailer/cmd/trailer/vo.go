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

// recordVO reads the whole script with ElevenLabs in a single request, so the
// delivery is one continuous take, and stores assets/vo/take.mp3 with its
// character timestamps in take.json. It skips the request when the script and
// voice are unchanged. The API key is read from ELEVENLABS_API_KEY and is
// never written anywhere.
func recordVO(root string, force bool) error {
	key := os.Getenv("ELEVENLABS_API_KEY")
	if key == "" {
		return fmt.Errorf("ELEVENLABS_API_KEY is not set")
	}
	tl, err := loadTimeline(root)
	if err != nil {
		return err
	}
	text := tl.script()
	if t, ok := readTake(root); ok && !force && t.Text == text &&
		t.VoiceID == tl.Voice.VoiceID && t.ModelID == tl.Voice.ModelID {
		fmt.Printf("voice-over up to date (%.2fs)\n", t.Duration)
		return nil
	}
	audio, take, err := synthesize(key, tl.Voice, text)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "assets", "vo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "take.mp3"), audio, 0o644); err != nil {
		return err
	}
	meta, _ := json.Marshal(take)
	if err := os.WriteFile(filepath.Join(dir, "take.json"), append(meta, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("recorded %.2fs, %d characters\n", take.Duration, len(take.Chars))
	return nil
}

func synthesize(key string, v Voice, text string) ([]byte, voTake, error) {
	var take voTake
	body := map[string]any{
		"text":     text,
		"model_id": v.ModelID,
		"voice_settings": map[string]any{
			"stability":         v.Stability,
			"similarity_boost":  v.SimilarityBoost,
			"style":             v.Style,
			"speed":             v.Speed,
			"use_speaker_boost": true,
		},
	}
	b, _ := json.Marshal(body)
	url := "https://api.elevenlabs.io/v1/text-to-speech/" + v.VoiceID + "/with-timestamps?output_format=mp3_44100_192"
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("xi-api-key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
	if err != nil {
		return nil, take, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, take, err
	}
	if resp.StatusCode != http.StatusOK {
		if len(raw) > 400 {
			raw = raw[:400]
		}
		return nil, take, fmt.Errorf("ElevenLabs %s: %s", resp.Status, raw)
	}
	var out struct {
		Audio     string `json:"audio_base64"`
		Alignment struct {
			Chars  []string  `json:"characters"`
			Starts []float64 `json:"character_start_times_seconds"`
			Ends   []float64 `json:"character_end_times_seconds"`
		} `json:"alignment"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, take, err
	}
	audio, err := base64.StdEncoding.DecodeString(out.Audio)
	if err != nil {
		return nil, take, err
	}
	a := out.Alignment
	if len(a.Ends) == 0 || len(a.Starts) != len(a.Ends) {
		return nil, take, fmt.Errorf("no alignment in response")
	}
	take = voTake{
		Text: text, VoiceID: v.VoiceID, ModelID: v.ModelID,
		Chars: a.Chars, Starts: a.Starts, Ends: a.Ends, Duration: a.Ends[len(a.Ends)-1],
	}
	return audio, take, nil
}
