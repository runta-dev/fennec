package board

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// How a message refers to several requests after one KEY-n (THI-117, THI-131).
// desktop/src/app/thread_links.rs (`ref_chain`) reads the same grammar to turn
// each number into a link; keep the two in step.
//
//   - "THI-73/103/107": a slash right after a number. Each number only has to
//     end at a non-word character, as a lone KEY-n does.
//   - Lists: "THI-107、117、119、121", "THI-41, 59, 61", "THI-39 / 63 / 69",
//     "THI-117 和 119", "THI-41, 59 and 61", ranges "THI-107~121" (only the
//     two ends count). A later item may repeat the key ("THI-5, THI-7") or
//     start with '#'.
//   - A bare list item must look like the end of a reference, so counts,
//     dates and versions after a reference are not read: "THI-41, 3 files",
//     "THI-41、3 个文件", "THI-131, 2026-11-25", "THI-41, 0.1.13" keep only
//     THI-41. The list stops at the first item that fails.
//   - The key is case-sensitive: lowercase thi-35 is a branch or worktree
//     name (fix/thi-35-…), not a reference.

// refListSeps separate list items; spaces may surround them.
var refListSeps = []string{",", "，", "、", "/", "&", "~", "～", "–", "—", "和", "及", "与", "或"}

// refListWords separate list items only with spaces on both sides.
var refListWords = []string{"and", "or"}

// refUnits are words that make a number after a reference a count, not a
// request ("THI-41、3 个文件").
const refUnits = "个条次份行项天秒分时处位张件列页字号年月日周倍万千百元块台名人遍轮步%‰°"

func refWord(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func refDigit(s string, i int) bool { return i < len(s) && s[i] >= '0' && s[i] <= '9' }

func skipBlanks(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// refNumber reads 1 to 7 digits at i.
func refNumber(s string, i int) (int, int, bool) {
	j := i
	for refDigit(s, j) {
		j++
	}
	if j == i || j-i > 7 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(s[i:j])
	return n, j, err == nil
}

// refSep is the length of the list separator at i (blanks around it
// included), or 0.
func refSep(s string, i int) int {
	j := skipBlanks(s, i)
	for _, sep := range refListSeps {
		if strings.HasPrefix(s[j:], sep) {
			return skipBlanks(s, j+len(sep)) - i
		}
	}
	if j > i {
		for _, w := range refListWords {
			if strings.HasPrefix(s[j:], w) && j+len(w) < len(s) && (s[j+len(w)] == ' ' || s[j+len(w)] == '\t') {
				return skipBlanks(s, j+len(w)) - i
			}
		}
	}
	return 0
}

// refListEnd reports whether a bare list item may end at i.
func refListEnd(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	c := s[i]
	if refWord(c) || strings.IndexByte(".:-", c) >= 0 && refDigit(s, i+1) {
		return false
	}
	if refSep(s, i) > 0 {
		return true
	}
	j := skipBlanks(s, i)
	if j >= len(s) || s[j] == '\n' || s[j] == '\r' {
		return true
	}
	if refWord(s[j]) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[j:])
	return !strings.ContainsRune(refUnits, r)
}

// refChain reads the request numbers in s, which starts right after "KEY-":
// each number with the byte span of its text in s, and how many bytes the
// reference uses. ok is false when s does not start with a reference.
func refChain(s, key string) (refs [][3]int, used int, ok bool) {
	n, end, found := refNumber(s, 0)
	if !found || end < len(s) && refWord(s[end]) {
		return nil, 0, false
	}
	refs = append(refs, [3]int{n, 0, end})
	used = end
	for {
		j, loose := used+1, false
		if !strings.HasPrefix(s[used:], "/") {
			sep := refSep(s, used)
			if sep == 0 {
				break
			}
			j, loose = used+sep, true
		}
		start := j
		if strings.HasPrefix(s[j:], key+"-") {
			j, loose = j+len(key)+1, false
		} else if loose && strings.HasPrefix(s[j:], "#") {
			j++
		}
		n, end, found := refNumber(s, j)
		if !found {
			break
		}
		if loose && !refListEnd(s, end) || !loose && end < len(s) && refWord(s[end]) {
			break
		}
		refs = append(refs, [3]int{n, start, end})
		used = end
	}
	return refs, used, true
}

// refPattern finds references to requests of the queue with this key.
type refPattern struct {
	key  string
	head *regexp.Regexp
}

// mentionPattern matches where a reference to a request of the queue with
// this key starts: KEY- after a non-word character other than '-'.
func mentionPattern(key string) refPattern {
	return refPattern{key, regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(key) + `-`)}
}

// mentions lists the request numbers a text refers to, each once, with the
// byte offset of the first digit of the KEY-n its reference starts with.
func mentions(p refPattern, text string) [][2]int {
	seen := map[int]bool{}
	out := [][2]int{}
	at := 0
	for at < len(text) {
		m := p.head.FindStringIndex(text[at:])
		if m == nil {
			break
		}
		digits := at + m[1]
		refs, used, ok := refChain(text[digits:], p.key)
		if !ok {
			at = digits
			continue
		}
		for _, r := range refs {
			if r[0] > 0 && !seen[r[0]] {
				seen[r[0]] = true
				out = append(out, [2]int{r[0], digits})
			}
		}
		at = digits + used
	}
	return out
}
