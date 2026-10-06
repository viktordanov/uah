package render

import (
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/tui/state"
)

// transcriptLines are an item's lines as the transcript shows them: its own
// lines (ownLines), after a blank line when it or the item above stands
// apart (spaced). The blank line is the item's, so the backtrack fade and
// text selection map it like its other lines.
func (c *Cache) transcriptLines(s state.State, i, w int) []string {
	lines := c.ownLines(s, i, w)
	if len(lines) == 0 || lines[0] == "" || !c.spaced(s, i, w, lines) {
		return lines
	}

	return append([]string{""}, lines...)
}

// spaced reports whether item i, drawn as lines, needs a blank line above
// it: it or the nearest item above that draws anything stands apart, and
// no blank line is there yet. The first item drawn gets none.
func (c *Cache) spaced(s state.State, i, w int, lines []string) bool {
	for j := i - 1; j >= 0; j-- {
		above := c.ownLines(s, j, w)
		if len(above) == 0 {
			continue
		}

		return above[len(above)-1] != "" && (apart(s.Items[i], lines) || apart(s.Items[j], above))
	}

	return false
}

// apart is an item with a blank line above and below it: your message on
// its band, and a tool call of more than one line (a file edit with its
// diff, or a call with a second line), while one-line calls stay together.
func apart(it state.Item, lines []string) bool {
	return it.Kind == state.KindUser || (it.Kind == state.KindTool && (len(it.Diff) > 0 || len(lines) > 1))
}

// ownLines are an item's lines, with the message selected to go back to
// (state.Backtrack) marked, as Codex highlights it.
func (c *Cache) ownLines(s state.State, i, w int) []string {
	it := s.Items[i]
	if s.Backtrack != nil && it.Key == s.Backtrack.Key {
		return c.styles.bandLines("▶ ", it.Text, c.styles.selected.Render("  ↵ edit from here"), w)
	}

	return c.lines(it, w, s.Now, view{reasoning: s.ShowReasoning, details: s.Details})
}

// focusKey is the item the window holds still on: the search's match, or
// else the message selected to go back to; "" for none.
func focusKey(s state.State) string {
	if key := s.Find.Key(); key != "" {
		return key
	}
	if s.Backtrack != nil {
		return s.Backtrack.Key
	}

	return ""
}

// backtrackScroll is how far the transcript scrolls up so the focused
// item (focusKey) shows a third of the way down the window, or its top
// when it is taller; ok is false when nothing is focused.
func backtrackScroll(s state.State, c *Cache, w, height int) (scroll int, ok bool) {
	key := focusKey(s)
	if key == "" {
		return 0, false
	}
	below := 0
	for i, it := range slices.Backward(s.Items) {
		n := len(c.transcriptLines(s, i, w))
		if it.Key != key {
			below += n

			continue
		}
		room := height - min(height/3, max(height-n, 0))

		return max(below+n-room, 0), true
	}

	return 0, false
}

// fade dims the window's lines outside the selected message while going
// back, so the message stands out. The selected message is lines [from,
// from+n) of the transcript, and the window starts at its line start. The
// cache keeps the items' own lines; fading draws over copies each frame, so
// entering or leaving the selection renders nothing again.
func (st *Styles) fade(window []string, start, from, n int) {
	for j, l := range window {
		if k := start + j; l != "" && (k < from || k >= from+n) {
			window[j] = st.faded(l)
		}
	}
}

// faded is a line in the dim color: its colors and weights give way to the
// theme's dim, and only backgrounds, such as the band, stay.
func (st *Styles) faded(line string) string {
	return st.dimOn + sgr.ReplaceAllStringFunc(line, st.fadeSGR) + "\x1b[m"
}

// fadeSGR keeps an SGR's resets and backgrounds and drops the rest; after
// a reset the dim foreground comes back.
func (st *Styles) fadeSGR(seq string) string {
	params := strings.Split(seq[2:len(seq)-1], ";")
	var kept []string
	reset := false
	for i := 0; i < len(params); i++ {
		switch p := params[i]; p {
		case "", "0":
			kept, reset = append(kept, "0"), true
		case "38", "48", "58":
			n := extendedColorLen(params[i+1:])
			if p == "48" {
				kept = append(kept, params[i:i+1+n]...)
			}
			i += n
		default:
			if isBackground(p) {
				kept = append(kept, p)
			}
		}
	}
	if reset {
		kept = append(kept, st.dimOn[2:len(st.dimOn)-1])
	}
	if len(kept) == 0 {
		return ""
	}

	return "\x1b[" + strings.Join(kept, ";") + "m"
}

// extendedColorLen is how many parameters follow 38, 48, or 58: 5;n or
// 2;r;g;b.
func extendedColorLen(rest []string) int {
	switch {
	case len(rest) > 0 && rest[0] == "5":
		return min(2, len(rest))
	case len(rest) > 0 && rest[0] == "2":
		return min(4, len(rest))
	}

	return 0
}

// isBackground reports whether an SGR parameter sets or clears the
// background: 40–47, 49, or 100–107.
func isBackground(p string) bool {
	return (len(p) == 2 && p[0] == '4' && p[1] != '8') || (len(p) == 3 && p[:2] == "10" && p[2] <= '7')
}
