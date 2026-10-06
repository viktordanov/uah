// Package citations removes the citation markers OpenAI models write into
// their text after a web search, such as "citeturn2view0":
// private-use characters that a client turns into links to the sources.
// uah has no source to link to, so it hides them, where they would
// otherwise show as stray symbols and "citeturn2view0".
package citations

import "strings"

// The markers: a citation starts with Start, its kind and each reference
// follow Sep, and End closes it.
const (
	Start = ''
	End   = ''
	Sep   = ''
)

// Strip returns text without its citation markers: each span from Start
// to End goes, and so does a Start whose End has not arrived yet, as
// while an answer streams. A space left doubled before punctuation or
// another space by a removed span is dropped too.
func Strip(text string) string {
	if !strings.ContainsAny(text, string([]rune{Start, End, Sep})) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	rest := text
	for {
		i := strings.IndexRune(rest, Start)
		if i < 0 {
			b.WriteString(dropMarks(rest))

			break
		}
		b.WriteString(dropMarks(rest[:i]))
		rest = rest[i+len(string(Start)):]
		j := strings.IndexRune(rest, End)
		if j < 0 {
			break // still streaming: its end comes later
		}
		rest = rest[j+len(string(End)):]
		if strings.HasSuffix(b.String(), " ") && (rest == "" || strings.ContainsAny(rest[:1], " .,;:!?)\n")) {
			s := strings.TrimSuffix(b.String(), " ")
			b.Reset()
			b.WriteString(s)
		}
	}

	return b.String()
}

// dropMarks removes stray markers outside a span.
func dropMarks(s string) string {
	return strings.Map(func(r rune) rune {
		if r == Start || r == End || r == Sep {
			return -1
		}

		return r
	}, s)
}
