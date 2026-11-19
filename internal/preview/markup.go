package preview

import (
	"regexp"
	"strings"
)

type Part struct {
	Text string `json:"text,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

// Parse only recognizes a complete, standalone marker outside Markdown code.
// Restricting markers to their own lines makes partial streaming and quoted
// examples unambiguous. Ordinary URLs, XML and paths remain ordinary text.
func Parse(text string) []Part {
	parts := []Part{}
	var plain strings.Builder
	fence := ""
	fenceLength := 0
	inlineTicks := 0
	flush := func() {
		if plain.Len() > 0 {
			parts = append(parts, Part{Text: plain.String()})
			plain.Reset()
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		indented := strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
		if fence != "" {
			plain.WriteString(line)
			if strings.HasPrefix(trimmed, strings.Repeat(fence, fenceLength)) && strings.Trim(trimmed, fence) == "" {
				fence = ""
			}
			continue
		}
		if !indented && inlineTicks == 0 && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			fence = trimmed[:1]
			fenceLength = 0
			for fenceLength < len(trimmed) && trimmed[fenceLength:fenceLength+1] == fence {
				fenceLength++
			}
			plain.WriteString(line)
			continue
		}
		if !indented && inlineTicks == 0 {
			if ref := parseMarker(trimmed); ref != "" {
				flush()
				parts = append(parts, Part{Ref: ref})
				continue
			}
		}
		// Backtick spans may cross lines. Markers inside them remain literal text.
		for i := 0; i < len(line); {
			if line[i] != '`' {
				i++
				continue
			}
			start := i
			for i < len(line) && line[i] == '`' {
				i++
			}
			n := i - start
			if inlineTicks == 0 {
				inlineTicks = n
			} else if inlineTicks == n {
				inlineTicks = 0
			}
		}
		plain.WriteString(line)
	}
	flush()
	return parts
}

// marker is the reference on its own line: the link Markup prints (to the
// Mac's preview server), or the older image form with fennec-preview:.
var marker = regexp.MustCompile(`^(?:!\[(?:\\.|[^\]\\])*\]\(fennec-preview:|!?\[(?:\\.|[^\]\\])*\]\(http://(?:localhost|127\.0\.0\.1):47823/p/[a-z0-9-]{3,40}/)(pv_[a-f0-9]{32})\)$`)

func parseMarker(value string) string {
	if len(value) > 2048 {
		return ""
	}
	match := marker.FindStringSubmatch(value)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}
