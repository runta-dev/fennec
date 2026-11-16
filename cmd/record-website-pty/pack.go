//go:build website_record

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// pack keeps byte payloads intact, trims a private shell setup before its clear,
// and shortens idle gaps. The selected source ranges must be reviewed first.
func pack(input, output string, skipStart, skipEnd int, endMS, duration int64) error {
	if duration <= 1200 || skipStart < 0 || skipEnd < skipStart {
		return fmt.Errorf("invalid recording range or duration")
	}
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var chunks []chunk
	var stream bytes.Buffer
	var previous, clock int64
	for i := 0; ; i++ {
		var c chunk
		if err = decoder.Decode(&c); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if endMS > 0 && c.Time > endMS {
			break
		}
		if i >= skipStart && i < skipEnd {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(c.Data)
		if err != nil {
			return err
		}
		if i == 0 {
			if index := bytes.Index(raw, []byte("\x1b[H\x1b[J")); index >= 0 {
				raw = raw[index:]
			}
		}
		// These are known private identifiers from the recording environment. Refuse
		// publication; never silently mask an unknown credential or personal title.
		for _, private := range []string{"/Users/", "/root/repos/", "MacBook-Pro%", "sk-proj-", "sk-ant-", "API_KEY="} {
			if bytes.Contains(raw, []byte(private)) {
				return fmt.Errorf("private marker in chunk %d", i)
			}
		}
		stream.Write(raw)
		delta := c.Time - previous
		if delta > 650 {
			delta = 650
		}
		if delta < 0 {
			delta = 0
		}
		previous = c.Time
		clock += delta
		c.Time = clock
		c.Data = base64.StdEncoding.EncodeToString(raw)
		chunks = append(chunks, c)
	}
	for _, private := range []string{"/Users/", "/root/repos/", "MacBook-Pro%", "sk-proj-", "sk-ant-", "API_KEY="} {
		if bytes.Contains(stream.Bytes(), []byte(private)) {
			return fmt.Errorf("private marker across recording chunks")
		}
	}
	if len(chunks) == 0 || clock == 0 {
		return fmt.Errorf("empty recording")
	}
	for i := range chunks {
		chunks[i].Time = chunks[i].Time * (duration - 1200) / clock
	}
	payload := struct {
		Cols     int     `json:"cols"`
		Rows     int     `json:"rows"`
		Duration int64   `json:"duration"`
		Chunks   []chunk `json:"chunks"`
	}{100, 32, duration, chunks}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(output, append(data, '\n'), 0644)
}
