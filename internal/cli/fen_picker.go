package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func fenColumns(value string) int {
	columns := 0
	for _, character := range value {
		if character >= 0x1100 && (character <= 0x115f || character >= 0x2e80 && character <= 0xa4cf || character >= 0xac00 && character <= 0xd7a3 || character >= 0xf900 && character <= 0xfaff || character >= 0xfe10 && character <= 0xfe6f || character >= 0xff01 && character <= 0xff60 || character >= 0x1f300) {
			columns += 2
		} else {
			columns++
		}
	}
	return columns
}

func fenClip(value string, columns int) string {
	if columns <= 0 {
		return ""
	}
	if fenColumns(value) <= columns {
		return value
	}
	var result strings.Builder
	used := 0
	for _, character := range value {
		cells := fenColumns(string(character))
		if used+cells > columns-1 {
			break
		}
		result.WriteRune(character)
		used += cells
	}
	return result.String() + "…"
}

func fenPickerFrame(sessions []fenSession, selected, width, height int, confirmation bool, message string) string {
	if width < 12 {
		width = 12
	}
	if height < 5 {
		height = 5
	}
	footer := []string{}
	parts := []string{"↑/↓ j/k select", "enter attach", "x kill", "r refresh", "esc quit"}
	if confirmation {
		parts = []string{"enter/y kill", "esc/n cancel"}
	}
	line := ""
	for _, part := range parts {
		combined := part
		if line != "" {
			combined = line + " · " + part
		}
		if fenColumns(combined) > width-4 && line != "" {
			footer = append(footer, line)
			line = part
		} else {
			line = combined
		}
	}
	if line != "" {
		footer = append(footer, line)
	}
	if len(footer) > height-4 {
		footer = footer[:height-4]
	}
	lines := make([]string, height)
	lines[0] = "  \x1b[1mAttach to session\x1b[0m"
	lines[1] = fmt.Sprintf("  \x1b[2m%d live sessions\x1b[0m", len(sessions))
	capacity := height - len(footer) - 5
	if capacity < 1 {
		capacity = 1
	}
	offset := 0
	if selected >= capacity {
		offset = selected - capacity + 1
	}
	for index := offset; index < len(sessions) && index < offset+capacity; index++ {
		session := sessions[index]
		prefix := fmt.Sprintf("  %d. ", index+1)
		if index == selected {
			prefix = fmt.Sprintf("› %d. ", index+1)
		}
		metadata := session.Receipt.Agent + " · " + session.Status
		available := width - fenColumns(prefix) - fenColumns(metadata) - 4
		if available < 10 {
			metadata = ""
			available = width - fenColumns(prefix) - 2
		}
		content := prefix + fenClip(session.Title, available)
		if metadata != "" {
			content += strings.Repeat(" ", max(2, width-fenColumns(content)-fenColumns(metadata)-2)) + metadata
		}
		if index == selected {
			style := "\x1b[7m"
			if os.Getenv("NO_COLOR") == "" {
				style = "\x1b[30;104m"
			}
			content = style + content + strings.Repeat(" ", max(0, width-1-fenColumns(content))) + "\x1b[0m"
		}
		lines[3+index-offset] = content
	}
	if len(sessions) == 0 {
		lines[3] = "  No active sessions."
	}
	if confirmation && selected < len(sessions) {
		message = "Kill “" + sessions[selected].Title + "”?"
	}
	lines[height-len(footer)-2] = "  " + fenClip(message, width-4)
	for index, part := range footer {
		lines[height-len(footer)+index] = "  \x1b[2m" + fenClip(part, width-4) + "\x1b[0m"
	}
	var frame strings.Builder
	for index, line := range lines {
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[2K%s", index+1, line)
	}
	return frame.String()
}

func fenPick(ctx context.Context, sessions []fenSession, load func() ([]fenSession, error), kill func(fenSession) error, refresh ...func() error) (*fenSession, error) {
	descriptor := int(os.Stdin.Fd())
	previous, err := fenTerminalState(descriptor)
	if err != nil {
		return nil, err
	}
	next := *previous
	next.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG | unix.IEXTEN
	next.Iflag &^= unix.ICRNL | unix.IXON
	next.Cc[unix.VMIN] = 0
	next.Cc[unix.VTIME] = 1
	if err := fenSetTerminalState(descriptor, &next); err != nil {
		return nil, err
	}
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l")
	defer func() {
		fenSetTerminalState(descriptor, previous)
		fmt.Fprint(os.Stdout, "\x1b[0m\x1b[?25h\x1b[?1049l")
	}()
	selected, width, height := 0, 0, 0
	confirmation, dirty := false, true
	message, sequence := "", ""
	var escapeAt time.Time
	nextRefresh := time.Now().Add(time.Second)
	render := func() {
		fmt.Fprint(os.Stdout, fenPickerFrame(sessions, selected, width, height, confirmation, message))
		dirty = false
	}
	for {
		if ctx.Err() != nil {
			return nil, nil
		}
		if !confirmation && len(refresh) > 0 && time.Now().After(nextRefresh) {
			nextRefresh = time.Now().Add(time.Second)
			updated, err := load()
			if err != nil {
				if message != err.Error() {
					message = err.Error()
					dirty = true
				}
			} else if !reflect.DeepEqual(updated, sessions) {
				id := ""
				if selected < len(sessions) {
					id = sessions[selected].Receipt.SessionID
				}
				sessions = updated
				selected = 0
				for index, session := range sessions {
					if session.Receipt.SessionID == id {
						selected = index
					}
				}
				message = ""
				dirty = true
			}
		}
		columns, rows := 80, 24
		if size, err := unix.IoctlGetWinsize(descriptor, unix.TIOCGWINSZ); err == nil && size.Col > 0 && size.Row > 0 {
			columns, rows = int(size.Col), int(size.Row)
		}
		if columns != width || rows != height {
			width, height = columns, rows
			dirty = true
		}
		if dirty {
			render()
		}
		var input [1]byte
		count, err := os.Stdin.Read(input[:])
		if err != nil && err != io.EOF {
			return nil, err
		}
		if count == 0 {
			if sequence == "\x1b" && time.Since(escapeAt) > 100*time.Millisecond {
				if confirmation {
					confirmation = false
					sequence = ""
					dirty = true
				} else {
					return nil, nil
				}
			}
			continue
		}
		key := string(input[0])
		if sequence != "" {
			sequence += key
			if sequence == "\x1b[" || sequence == "\x1bO" {
				continue
			}
			switch sequence {
			case "\x1b[A", "\x1bOA":
				key = "k"
			case "\x1b[B", "\x1bOB":
				key = "j"
			default:
				key = ""
			}
			sequence = ""
		} else if input[0] == 27 {
			sequence = key
			escapeAt = time.Now()
			continue
		}
		if input[0] == 3 || input[0] == 4 {
			return nil, nil
		}
		if confirmation {
			if key == "n" || key == "N" {
				confirmation = false
				dirty = true
				continue
			}
			if key == "\r" || key == "\n" || key == "y" || key == "Y" {
				confirmation = false
				message = "Stopping session…"
				render()
				if err := kill(sessions[selected]); err != nil {
					message = err.Error()
				} else {
					sessions = append(sessions[:selected], sessions[selected+1:]...)
					if selected >= len(sessions) {
						selected = max(0, len(sessions)-1)
					}
					message = "Session stopped."
				}
				dirty = true
			}
			continue
		}
		switch key {
		case "q":
			return nil, nil
		case "j":
			if len(sessions) > 0 {
				selected = (selected + 1) % len(sessions)
				dirty = true
			}
		case "k":
			if len(sessions) > 0 {
				selected = (selected + len(sessions) - 1) % len(sessions)
				dirty = true
			}
		case "x":
			if len(sessions) > 0 {
				confirmation = true
				dirty = true
			}
		case "\r", "\n":
			if len(sessions) > 0 {
				result := sessions[selected]
				return &result, nil
			}
		case "r":
			if len(refresh) > 0 {
				if err := refresh[0](); err != nil {
					message = err.Error()
				} else {
					message = "Refresh requested."
				}
				dirty = true
				continue
			}
			message = "Refreshing sessions…"
			render()
			id := ""
			if len(sessions) > 0 {
				id = sessions[selected].Receipt.SessionID
			}
			updated, err := load()
			if err != nil {
				message = err.Error()
			} else {
				sessions = updated
				selected = 0
				for index, session := range sessions {
					if session.Receipt.SessionID == id {
						selected = index
					}
				}
				message = ""
			}
			dirty = true
		}
	}
}
