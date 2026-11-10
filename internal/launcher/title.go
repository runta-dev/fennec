package launcher

import (
	"bytes"
	"fennec/internal/sessionid"
	"io"
	"strings"
)

func CleanSessionTitle(title, sessionID string) string {
	if !sessionid.Valid(sessionID) {
		return title
	}
	separator := strings.LastIndex(title, " | ")
	if separator < 0 {
		return title
	}
	suffix := title[separator+3:]
	if suffix == sessionID {
		return title[:separator]
	}
	for _, ellipsis := range []string{"...", "…"} {
		if strings.HasSuffix(suffix, ellipsis) {
			prefix := strings.TrimSuffix(suffix, ellipsis)
			if len(prefix) >= 8 && len(prefix) < len(sessionID) && strings.HasPrefix(sessionID, prefix) {
				return title[:separator]
			}
		}
	}
	return title
}

type SessionTitleWriter struct {
	Writer    io.Writer
	SessionID string
	pending   []byte
}

func (writer *SessionTitleWriter) Write(data []byte) (int, error) {
	var output bytes.Buffer
	for _, value := range data {
		if len(writer.pending) == 0 {
			if value == 27 {
				writer.pending = append(writer.pending, value)
			} else {
				output.WriteByte(value)
			}
			continue
		}
		if len(writer.pending) == 1 {
			if value == ']' {
				writer.pending = append(writer.pending, value)
				continue
			}
			output.WriteByte(27)
			if value == 27 {
				continue
			}
			output.WriteByte(value)
			writer.pending = nil
			continue
		}
		writer.pending = append(writer.pending, value)
		terminated := value == 7 || (len(writer.pending) > 2 && writer.pending[len(writer.pending)-2] == 27 && value == '\\')
		if terminated {
			end := len(writer.pending) - 1
			if value == '\\' {
				end--
			}
			body := string(writer.pending[2:end])
			if strings.HasPrefix(body, "0;") || strings.HasPrefix(body, "2;") {
				output.WriteString("\x1b]" + body[:2] + CleanSessionTitle(body[2:], writer.SessionID))
				output.Write(writer.pending[end:])
			} else {
				output.Write(writer.pending)
			}
			writer.pending = nil
		} else if len(writer.pending) > 8192 {
			output.Write(writer.pending)
			writer.pending = nil
		}
	}
	if output.Len() > 0 {
		_, err := writer.Writer.Write(output.Bytes())
		if err != nil {
			return len(data), err
		}
	}
	return len(data), nil
}

func (writer *SessionTitleWriter) Flush() error {
	if len(writer.pending) == 0 {
		return nil
	}
	_, err := writer.Writer.Write(writer.pending)
	writer.pending = nil
	return err
}
