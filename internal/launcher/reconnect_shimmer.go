package launcher

import (
	"fennec/internal/terminalstream"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

type reconnectShimmer struct {
	enabled                bool
	rgb                    bool
	foreground, background [3]float64
}

func newReconnectShimmer(display *terminalstream.DisplayFacts) reconnectShimmer {
	style := reconnectShimmer{enabled: term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"}
	parse := func(value string) ([3]float64, bool) {
		var color [3]float64
		if len(value) != 7 || value[0] != '#' {
			return color, false
		}
		n, err := strconv.ParseUint(value[1:], 16, 24)
		if err != nil {
			return color, false
		}
		return [3]float64{float64(n >> 16), float64((n >> 8) & 255), float64(n & 255)}, true
	}
	if display != nil {
		foreground, fgOK := parse(display.Foreground)
		background, bgOK := parse(display.Background)
		colorTerm := strings.ToLower(os.Getenv("COLORTERM"))
		style.rgb = fgOK && bgOK && (colorTerm == "truecolor" || colorTerm == "24bit" || strings.Contains(os.Getenv("TERM"), "direct"))
		style.foreground, style.background = foreground, background
	}
	return style
}

// Use Codex's two-second sweep, ten-character padding, and five-character
// cosine falloff: https://github.com/openai/codex/blob/main/codex-rs/tui/src/shimmer.rs
// Blend toward the terminal's actual foreground, so light and dark themes keep
// their contrast. Only the label animates; counters and key hints stay steady.
func (s reconnectShimmer) render(label string, elapsed time.Duration) string {
	if !s.enabled || label == "" {
		return label
	}
	characters := []rune(label)
	position := math.Mod(elapsed.Seconds(), 2) / 2 * float64(len(characters)+20)
	var out strings.Builder
	last := ""
	for i, character := range characters {
		distance := math.Abs(float64(i+10) - position)
		intensity := 0.0
		if distance <= 5 {
			intensity = 0.5 * (1 + math.Cos(math.Pi*distance/5))
		}
		color := "\x1b[2m"
		if s.rgb {
			var rgb [3]int
			for c := range rgb {
				weight := 0.55 + 0.45*intensity
				rgb[c] = int(math.Round(s.foreground[c]*weight + s.background[c]*(1-weight)))
			}
			color = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", rgb[0], rgb[1], rgb[2])
		} else if intensity >= 0.6 {
			color = "\x1b[22;1m"
		} else if intensity >= 0.2 {
			color = "\x1b[22m"
		}
		// Clear bold before entering dim mode on terminals without RGB support.
		if color != last {
			if !s.rgb {
				out.WriteString("\x1b[22m")
			}
			out.WriteString(color)
			last = color
		}
		out.WriteRune(character)
	}
	out.WriteString("\x1b[22;39m")
	return out.String()
}

func (s reconnectShimmer) line(spinner, details string, columns int, elapsed time.Duration) string {
	const label = "Reconnecting"
	prefix := spinner + " "
	plain := []rune(prefix + label + details)
	plain = plain[:min(len(plain), max(0, columns-1))]
	start, end := min(len(plain), len([]rune(prefix))), min(len(plain), len([]rune(prefix+label)))
	return string(plain[:start]) + s.render(string(plain[start:end]), elapsed) + string(plain[end:])
}
