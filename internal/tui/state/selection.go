package state

import (
	"strconv"
	"time"

	"github.com/rivo/uniseg"
)

// Selecting transcript text with the mouse, while the TUI reports it: a
// press and a drag select from cell to cell, a double click a word (a run
// of non-space), a triple click a line, and the release copies. Positions
// name the text, an item's drawn line and a cell on it, not a screen row,
// so a selection stays on its text while the transcript scrolls or grows.
// The shell maps the mouse to positions (render.Cache.At) and copies the
// selected text (render.SelectedText); see docs/design/selection.md.

// TextPos is a cell of the transcript: cell Col of line Line of the item
// Key, as the renderer draws it.
type TextPos struct {
	Key       string
	Line, Col int
}

// BannerKey names the banner's lines, which come before every item.
const BannerKey = "banner"

// Selection is the text selected with the mouse, from Anchor, where the
// press was, to Head, both cells included.
type Selection struct {
	Anchor, Head TextPos
	// Dragging is set from the press to the release.
	Dragging bool
	// moved says the selection covers more than the pressed cell.
	moved bool
}

type (
	// MousePress is a press of the left button on the transcript at At,
	// whose line reads Text (without styles); When is its time, which
	// tells a double or triple click.
	MousePress struct {
		At   TextPos
		Text string
		When time.Time
	}
	// MouseDrag moves the pressed mouse to At.
	MouseDrag struct{ At TextPos }
	// MouseRelease lets the button go: a selection is copied.
	MouseRelease struct{}
	// ClearSelection drops the selection, as a click outside the
	// transcript does.
	ClearSelection struct{}
	// Copied reports the selection written to the clipboard at At, which
	// a toast says.
	Copied struct {
		Lines int
		At    time.Time
	}
	// EffCopySelection copies the selected text to the clipboard.
	EffCopySelection struct{}
)

func (EffCopySelection) effect() {}

const (
	// clickWindow is how soon a click on the same cell counts as the next
	// of a double or triple click.
	clickWindow = 500 * time.Millisecond
	// lineEnd is past the end of any line: a line selection's head.
	lineEnd = 1 << 30
)

// clicks counts presses on one cell in a row, for double and triple clicks.
type clicks struct {
	at   TextPos
	when time.Time
	n    int
}

// onSelection handles the mouse's intents; ok is false for other events.
// Esc clears a selection and does nothing else; typing, sending, and
// changing the view clear it and go on.
func (s *State) onSelection(ev any) (effects []Effect, ok bool) {
	switch e := ev.(type) {
	case MousePress:
		s.press(e)
	case MouseDrag:
		if sel := s.Selection; sel != nil && sel.Dragging {
			sel.Head, sel.moved = e.At, sel.moved || e.At != sel.Anchor
		}
	case MouseRelease:
		return s.release(), true
	case ClearSelection:
		s.Selection = nil
	case Copied:
		s.ShowToast(copiedText(e.Lines), e.At)
	case Esc:
		if s.Selection == nil {
			return nil, false
		}
		s.Selection = nil
	case DraftChanged, Submit, Steer, ToggleDetails:
		s.Selection = nil

		return nil, false
	default:
		return nil, false
	}

	return nil, true
}

// onSelectionOrShell gives the selection the first look at every event,
// since keys clear it, and then shell mode.
func (s *State) onSelectionOrShell(ev any) ([]Effect, bool) {
	if effects, ok := s.onSelection(ev); ok {
		return effects, true
	}

	return s.onShell(ev)
}

// press starts a selection: one click at the cell, a second the word
// under it, a third its line.
func (s *State) press(e MousePress) {
	c := s.click
	if c.at == e.At && e.When.Sub(c.when) < clickWindow {
		c.n = c.n%3 + 1
	} else {
		c.n = 1
	}
	c.at, c.when = e.At, e.When
	s.click = c
	sel := &Selection{Anchor: e.At, Head: e.At, Dragging: true}
	switch c.n {
	case 2:
		from, to := wordAt(e.Text, e.At.Col)
		sel.Anchor.Col, sel.Head.Col, sel.moved = from, to, true
	case 3:
		sel.Anchor.Col, sel.Head.Col, sel.moved = 0, lineEnd, true
	}
	s.Selection = sel
}

// release ends the drag and copies what it selected; a plain click
// selects nothing.
func (s *State) release() []Effect {
	sel := s.Selection
	if sel == nil || !sel.Dragging {
		return nil
	}
	sel.Dragging = false
	if !sel.moved {
		s.Selection = nil

		return nil
	}

	return []Effect{EffCopySelection{}}
}

// SelectedCols are the cells of line Line of the item key that the
// selection covers, [from, to); to may lie past the line's end. ok is
// false when the line has none selected.
func (s State) SelectedCols(key string, line int) (from, to int, ok bool) {
	start, end, ok := s.SelectedRange()
	if !ok {
		return 0, 0, false
	}
	at := TextPos{Key: key, Line: line}
	if s.before(at, TextPos{Key: start.Key, Line: start.Line}) || s.before(TextPos{Key: end.Key, Line: end.Line}, at) {
		return 0, 0, false
	}
	from, to = 0, lineEnd
	if key == start.Key && line == start.Line {
		from = start.Col
	}
	if key == end.Key && line == end.Line {
		to = end.Col + 1
	}

	return from, to, from < to
}

// SelectedRange is the selection in transcript order; ok is false with
// none, while a press has not moved yet, or when an item it names is gone.
func (s State) SelectedRange() (start, end TextPos, ok bool) {
	sel := s.Selection
	if sel == nil || !sel.moved || s.Order(sel.Anchor.Key) < -1 || s.Order(sel.Head.Key) < -1 {
		return TextPos{}, TextPos{}, false
	}
	start, end = sel.Anchor, sel.Head
	if s.before(end, start) {
		start, end = end, start
	}

	return start, end, true
}

// Order is an item's place in the transcript: -1 for the banner, and -2
// for a key no item has.
func (s State) Order(key string) int {
	if key == BannerKey {
		return -1
	}
	if i, ok := s.index[key]; ok {
		return i
	}

	return -2
}

// before reports whether a comes before b in the transcript.
func (s State) before(a, b TextPos) bool {
	if oa, ob := s.Order(a.Key), s.Order(b.Key); oa != ob {
		return oa < ob
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}

	return a.Col < b.Col
}

// wordAt is the word, a run of non-space, around cell col of text: its
// first and last cells. On a space it is that one cell.
func wordAt(text string, col int) (from, to int) {
	var spaces []bool // per cell
	g := uniseg.NewGraphemes(text)
	for g.Next() {
		space := g.Str() == " " || g.Str() == "\t"
		for range g.Width() {
			spaces = append(spaces, space)
		}
	}
	if col < 0 || col >= len(spaces) || spaces[col] {
		return col, col
	}
	from, to = col, col
	for from > 0 && !spaces[from-1] {
		from--
	}
	for to < len(spaces)-1 && !spaces[to+1] {
		to++
	}

	return from, to
}

// copiedText is the notice after a copy: "copied 3 lines".
func copiedText(lines int) string {
	if lines == 1 {
		return "copied 1 line"
	}

	return "copied " + strconv.Itoa(lines) + " lines"
}
