package view

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text limits of the agent's view.
const (
	// NameChars caps a name or label in the outline and in vision lists.
	NameChars = 80
	// VisionValueChars caps a value in a vision element list.
	VisionValueChars = 60
	// MaxFound is the most elements a find returns.
	MaxFound = 20
	// OutlineBudget is the default character budget of one outline.
	OutlineBudget = 60000
)

// objectReplacement marks an embedded object in accessibility text.
const objectReplacement = '\uFFFC'

// StripObjects drops embedded-object markers (U+FFFC) from screen text.
func StripObjects(s string) string {
	if !strings.ContainsRune(s, objectReplacement) {
		return s
	}
	return strings.ReplaceAll(s, string(objectReplacement), "")
}

// Norm folds a name for comparison: lower case, without label punctuation
// (underscores, ellipses, a trailing colon), whitespace collapsed.
func Norm(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r == '_' || r == '…' {
			return -1
		}
		return r
	}, s)
	s = strings.ReplaceAll(s, "...", "")
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ":")
	return OneLine(s)
}

// OneLine joins the words of s with single spaces.
func OneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Quote escapes at most max characters of s for a double-quoted outline field
// and says whether it cut s. Quotes, backslashes and every control or line
// separator character are escaped, so screen text cannot end the field or
// start a new line.
func Quote(s string, max int) (string, bool) {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == max {
			return b.String(), true
		}
		n++
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || r == utf8.RuneError {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String(), false
}

// Field renders s as a quoted field of at most max characters; a cut field
// is followed by an ellipsis outside the quotes.
func Field(s string, max int) string {
	q, cut := Quote(StripObjects(s), max)
	if cut {
		return `"` + q + `"…`
	}
	return `"` + q + `"`
}

// Clip keeps at most max characters of s: its first ones, or with tail its
// last ones. It never splits a character.
func Clip(s string, max int, tail bool) string {
	if max <= 0 {
		return ""
	}
	n := utf8.RuneCountInString(s)
	if n <= max {
		return s
	}
	if !tail {
		i := 0
		for k := range s {
			if i == max {
				return s[:k]
			}
			i++
		}
		return s
	}
	skip := n - max
	i := 0
	for k := range s {
		if i == skip {
			return s[k:]
		}
		i++
	}
	return ""
}

// FormatNumber prints a number: whole numbers without decimals, negative
// zero as 0.
func FormatNumber(v float64) string {
	if v == 0 {
		return "0"
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ValueString renders an accessibility value: strings as they are, numbers
// through FormatNumber, booleans as true or false. It returns nil for no
// value.
func ValueString(v any) *string {
	var s string
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		s = x
	case bool:
		s = strconv.FormatBool(x)
	case int:
		s = strconv.Itoa(x)
	case int32:
		s = strconv.FormatInt(int64(x), 10)
	case int64:
		s = strconv.FormatInt(x, 10)
	case float32:
		s = FormatNumber(float64(x))
	case float64:
		s = FormatNumber(x)
	default:
		return nil
	}
	return &s
}

// words splits a name into lower-case words; a '+' belongs to its word
// (notepad++), other punctuation separates words.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+')
	})
}

// AppMatches says whether want names the app called app: each word of want
// equals a word of app, begins one with at least three characters (the rest
// going on with a letter or digit, so "notepad" never names "notepad++"), or
// ends one with at least four. Matching ignores case; a blank want matches
// nothing.
func AppMatches(want, app string) bool {
	ws := words(want)
	if len(ws) == 0 {
		return false
	}
	have := words(app)
	for _, w := range ws {
		if !wordIn(w, have) {
			return false
		}
	}
	return true
}

func wordIn(w string, have []string) bool {
	n := utf8.RuneCountInString(w)
	for _, h := range have {
		if h == w {
			return true
		}
		if n >= 3 && strings.HasPrefix(h, w) {
			next, _ := utf8.DecodeRuneInString(h[len(w):])
			if unicode.IsLetter(next) || unicode.IsDigit(next) {
				return true
			}
		}
		if n >= 4 && strings.HasSuffix(h, w) {
			return true
		}
	}
	return false
}

// TitleHasWords says whether every word of want is a whole word of title, in
// order and next to each other ("Mail" is not in "Gmail - Inbox").
func TitleHasWords(title, want string) bool {
	ws, ts := words(want), words(title)
	if len(ws) == 0 {
		return false
	}
outer:
	for i := 0; i+len(ws) <= len(ts); i++ {
		for j, w := range ws {
			if ts[i+j] != w {
				continue outer
			}
		}
		return true
	}
	return false
}
