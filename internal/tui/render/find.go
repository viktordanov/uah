package render

import (
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/tui/state"
)

// markMatches draws the search's query (state.Find) over the window's
// rows of the items that contain it: on the accent in the shown item, on
// a dimmer tint in the others. It runs after fade, so the matches are the
// only color left. Only the rows on screen are looked at.
func (c *Cache) markMatches(s state.State, out []string) {
	f := s.Find
	if f == nil || f.Query == "" || len(f.Keys) == 0 {
		return
	}
	shown := f.Key()
	query := []rune(strings.ToLower(f.Query))
	for j, ref := range c.rows {
		if ref.Key == "" || !slices.Contains(f.Keys, ref.Key) {
			continue
		}
		on := c.styles.tintOn
		if ref.Key == shown {
			on = c.styles.matchOn
		}
		spans := cellMatches(ansi.Strip(out[j]), query)
		for _, sp := range slices.Backward(spans) {
			out[j] = c.styles.highlight(out[j], sp[0], sp[1], on)
		}
	}
}

// cellMatches are the cells [from, to) of plain where query, lower case,
// occurs, ignoring case, without overlaps.
func cellMatches(plain string, query []rune) [][2]int {
	if len(query) == 0 {
		return nil
	}
	text := []rune(plain)
	cells := make([]int, len(text)+1) // the cell each rune starts at
	for i, r := range text {
		cells[i+1] = cells[i] + ansi.StringWidth(string(r))
	}
	var out [][2]int
	for i := 0; i+len(query) <= len(text); i++ {
		if !foldAt(text[i:], query) {
			continue
		}
		out = append(out, [2]int{cells[i], cells[i+len(query)]})
		i += len(query) - 1
	}

	return out
}

// foldAt reports whether text starts with query, ignoring case.
func foldAt(text, query []rune) bool {
	for k, q := range query {
		if unicode.ToLower(text[k]) != q {
			return false
		}
	}

	return true
}
