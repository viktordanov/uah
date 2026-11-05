package markdown

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Web links are drawn inside OSC 8 hyperlinks, so a terminal opens the
// whole URL from any of its fragments: a Markdown link's text and its URL
// lead to its destination, and a bare http or https URL in the text to
// itself. The target is found in the source, before the wrap, and carry
// opens it again on every line the wrap gives the link.

// Hyperlink starts an OSC 8 hyperlink to uri with params ("line=12"):
// "ESC ] 8 ; params ; uri ST". The URI goes as given: a web URL's ";" is
// reserved, so "%3B" would change where it leads.
func Hyperlink(params, uri string) string {
	return "\x1b]8;" + params + ";" + uri + "\x1b\\"
}

// LinkClose ends a hyperlink.
const LinkClose = "\x1b]8;;\x1b\\"

// maxTarget is the longest link target drawn, in bytes; a longer URL is
// drawn as text alone.
const maxTarget = 4096

// webTarget is dest as a link target: an http or https URL with a host, at
// most maxTarget bytes, without control characters or spaces. Characters
// beyond ASCII are percent-encoded, so only printable ASCII reaches the
// terminal. ok is false for any other destination.
func webTarget(dest string) (string, bool) {
	if dest == "" || len(dest) > maxTarget || !utf8.ValidString(dest) {
		return "", false
	}
	ascii := true
	for _, r := range dest {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", false
		}
		ascii = ascii && r < utf8.RuneSelf
	}
	u, err := url.Parse(dest)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", false
	}
	if ascii {
		return dest, true
	}
	var b strings.Builder
	for i := range len(dest) {
		if c := dest[i]; c >= utf8.RuneSelf {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	if b.Len() > maxTarget {
		return "", false
	}

	return b.String(), true
}

// webURL is a bare URL of a text: its bytes [from, to) and its target.
type webURL struct {
	from, to int
	target   string
}

// webURLs finds the bare http and https URLs in text, at most one in each
// run of non-space characters. A URL starts at "http://" or "https://"
// not inside a word; it ends before trailing sentence punctuation
// (.,;:!?'") and before a closing bracket that opens nowhere in it, so
// "(see https://x.com/a)." ends at "a" and ".../Foo_(bar)" keeps its ")".
func webURLs(text string) []webURL {
	if !strings.Contains(text, "://") {
		return nil
	}
	var out []webURL
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) {
			i += n

			continue
		}
		end := i
		for end < len(text) {
			r, n := utf8.DecodeRuneInString(text[end:])
			if unicode.IsSpace(r) {
				break
			}
			end += n
		}
		if u, ok := wordURL(text[i:end]); ok {
			u.from, u.to = u.from+i, u.to+i
			out = append(out, u)
		}
		i = end
	}

	return out
}

// wordURL is the URL in a run of non-space characters: at its first "://",
// after "http" or "https" in any case.
func wordURL(word string) (webURL, bool) {
	sep := strings.Index(word, "://")
	at := -1
	switch {
	case sep >= 5 && strings.EqualFold(word[sep-5:sep], "https"):
		at = sep - 5
	case sep >= 4 && strings.EqualFold(word[sep-4:sep], "http"):
		at = sep - 4
	}
	if at < 0 || at > 0 && isWordByte(word[at-1]) {
		return webURL{}, false
	}
	end := at + urlEnd(word[at:])
	target, ok := webTarget(word[at:end])
	if !ok {
		return webURL{}, false
	}

	return webURL{from: at, to: end, target: target}, true
}

// isWordByte reports whether c can end a word or a URL scheme, so a URL
// right after it is part of something else ("xhttps://").
func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.' || c == '_'
}

// urlEnd is where a URL candidate ends once trailing punctuation and
// unmatched closing brackets are cut off.
func urlEnd(s string) int {
	const opens, closes = "([{<", ")]}>"
	var balance [4]int
	for i := range len(s) {
		if k := strings.IndexByte(opens, s[i]); k >= 0 {
			balance[k]++
		} else if k := strings.IndexByte(closes, s[i]); k >= 0 {
			balance[k]--
		}
	}
	end := len(s)
	for end > 0 {
		c := s[end-1]
		if k := strings.IndexByte(closes, c); k >= 0 {
			if balance[k] >= 0 {
				break
			}
			balance[k]++
		} else if strings.IndexByte(".,;:!?'\"", c) < 0 {
			break
		}
		end--
	}

	return end
}
