package transcript

import (
	"encoding/xml"
	"io"
	"strings"
)

// EventKind recognizes an entire Fennec device event, never XML quoted inside
// an ordinary user message. Older servers return these as user messages, so
// mobile clients also classify cached transcripts at the presentation boundary.
func EventKind(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "<fennec-event") {
		return ""
	}
	var event struct {
		XMLName    xml.Name `xml:"fennec-event"`
		Type       string   `xml:"type,attr"`
		Device     string   `xml:"device,attr"`
		Generation string   `xml:"generation,attr"`
	}
	decoder := xml.NewDecoder(strings.NewReader(text))
	if decoder.Decode(&event) != nil || event.XMLName.Space != "" || event.Type != "mac-online" || event.Device == "" || event.Generation == "" {
		return ""
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return event.Type
		}
		if err != nil {
			return ""
		}
		if data, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(data)) != "" {
			return ""
		}
	}
}
