package composer

import (
	"slices"
	"unicode"
)

// Key is a key press: its name as ultraviolet's Key.String gives it, the
// names Bubble Tea uses ("ctrl+k", "alt+backspace", "shift+enter", or the
// text itself, "a"), and the text it types, if any.
type Key struct {
	Name string
	Text string
}

// Op is an editing operation a key is bound to.
type Op int

// The operations, named as textarea's key bindings.
const (
	OpNone Op = iota
	OpCharacterForward
	OpCharacterBackward
	OpWordForward
	OpWordBackward
	OpLineNext
	OpLinePrevious
	OpDeleteWordBackward
	OpDeleteWordForward
	OpDeleteAfterCursor
	OpDeleteBeforeCursor
	OpInsertNewline
	OpDeleteCharacterBackward
	OpDeleteCharacterForward
	OpLineStart
	OpLineEnd
	OpPageUp
	OpPageDown
	OpPaste
	OpInputBegin
	OpInputEnd
	OpCapitalizeWordForward
	OpLowercaseWordForward
	OpUppercaseWordForward
	OpTransposeCharacterBackward
	OpSelectCharacterForward
	OpSelectCharacterBackward
	OpSelectWordForward
	OpSelectWordBackward
	OpSelectLineUp
	OpSelectLineDown
	OpSelectAll
	OpCopySelection
	OpYank
)

// KeyMap maps key names to operations. A key it does not name types its
// text.
type KeyMap map[string]Op

// DefaultKeyMap is textarea's default key map (bubbles v2.2.1), with uah's
// new-line keys, shift+enter and ctrl+j, in place of enter and ctrl+m:
// enter sends.
func DefaultKeyMap() KeyMap {
	km := KeyMap{}
	bind := func(op Op, keys ...string) {
		for _, k := range keys {
			km[k] = op
		}
	}
	bind(OpCharacterForward, "right", "ctrl+f")
	bind(OpCharacterBackward, "left", "ctrl+b")
	bind(OpWordForward, "alt+right", "ctrl+right", "alt+f")
	bind(OpWordBackward, "alt+left", "ctrl+left", "alt+b")
	bind(OpLineNext, "down", "ctrl+n")
	bind(OpLinePrevious, "up", "ctrl+p")
	bind(OpDeleteWordBackward, "alt+backspace", "ctrl+w", "ctrl+backspace")
	bind(OpDeleteWordForward, "alt+delete", "alt+d", "ctrl+delete")
	bind(OpDeleteAfterCursor, "ctrl+k")
	bind(OpDeleteBeforeCursor, "ctrl+u")
	bind(OpInsertNewline, "shift+enter", "ctrl+j")
	bind(OpDeleteCharacterBackward, "backspace", "ctrl+h")
	bind(OpDeleteCharacterForward, "delete", "ctrl+d")
	bind(OpLineStart, "home", "ctrl+a")
	bind(OpLineEnd, "end", "ctrl+e")
	bind(OpPageUp, "pgup")
	bind(OpPageDown, "pgdown")
	bind(OpPaste, "ctrl+v")
	bind(OpInputBegin, "alt+<", "ctrl+home")
	bind(OpInputEnd, "alt+>", "ctrl+end")
	bind(OpCapitalizeWordForward, "alt+c")
	bind(OpLowercaseWordForward, "alt+l")
	bind(OpUppercaseWordForward, "alt+u")
	bind(OpTransposeCharacterBackward, "ctrl+t")
	bind(OpSelectCharacterForward, "shift+right")
	bind(OpSelectCharacterBackward, "shift+left")
	bind(OpSelectWordForward, "ctrl+shift+right", "alt+shift+right", "alt+shift+f")
	bind(OpSelectWordBackward, "ctrl+shift+left", "alt+shift+left", "alt+shift+b")
	bind(OpSelectLineUp, "shift+up")
	bind(OpSelectLineDown, "shift+down")
	bind(OpSelectAll, "ctrl+g")
	bind(OpCopySelection, "ctrl+shift+c")
	bind(OpYank, "ctrl+y")

	return km
}

// Action is what Press asks of the caller, which does the I/O.
type Action int

const (
	// ActionNone needs nothing.
	ActionNone Action = iota
	// ActionPaste asks for the clipboard's text, given back with Paste.
	ActionPaste
	// ActionCopy asks to copy SelectedText to the clipboard.
	ActionCopy
)

// Press applies a key. A blurred composer ignores it.
func (c *Composer) Press(k Key) Action {
	if !c.focus {
		return ActionNone
	}
	act := c.press(k)
	c.fit()

	return act
}

func (c *Composer) press(k Key) Action { //nolint:gocyclo,cyclop // a dispatch switch over a closed set
	op := c.KeyMap[k.Name]
	killing := c.killing
	c.killing = false
	if c.edits(op) && c.deleteSelection() {
		return ActionNone // a deletion deletes the selection only
	}
	if kills(op) {
		c.kill(op, killing)

		return ActionNone
	}
	switch op {
	case OpNone:
		if k.Text != "" {
			c.deleteSelection()
			c.insert(sanitize(k.Text))
		}
	case OpInsertNewline:
		c.deleteSelection()
		c.splitLine()
	case OpPaste:
		c.deleteSelection()

		return ActionPaste
	case OpCopySelection:
		if c.HasSelection() {
			return ActionCopy
		}
	case OpDeleteCharacterBackward:
		c.deleteClusterBackward()
	case OpDeleteCharacterForward:
		c.deleteClusterForward()
	case OpYank:
		if len(c.killed) > 0 {
			c.deleteSelection()
			c.insert(slices.Clone(c.killed))
		}
	case OpCapitalizeWordForward:
		c.wordForward(func(i int, r rune) rune {
			if i == 0 {
				return unicode.ToTitle(r)
			}

			return r
		})
	case OpLowercaseWordForward:
		c.wordForward(func(_ int, r rune) rune { return unicode.ToLower(r) })
	case OpUppercaseWordForward:
		c.wordForward(func(_ int, r rune) rune { return unicode.ToUpper(r) })
	case OpTransposeCharacterBackward:
		c.transpose()
	case OpSelectCharacterForward:
		c.selectWith(c.clusterForward)
	case OpSelectCharacterBackward:
		c.selectWith(func() { c.clusterBackward(false) })
	case OpSelectWordForward:
		c.selectWith(func() { c.wordForward(nil) })
	case OpSelectWordBackward:
		c.selectWith(c.wordBackward)
	case OpSelectLineUp:
		c.selectWith(func() { c.moveRows(-1) })
	case OpSelectLineDown:
		c.selectWith(func() { c.moveRows(1) })
	case OpSelectAll:
		c.SelectAll()
	default:
		c.ClearSelection()
		c.move(op)
	}

	return ActionNone
}

// kills reports whether op deletes text that ctrl+y can yank back: a word,
// or the line after or before the cursor.
func kills(op Op) bool {
	switch op {
	case OpDeleteWordBackward, OpDeleteWordForward, OpDeleteAfterCursor, OpDeleteBeforeCursor:
		return true
	}

	return false
}

// kill applies a kill and keeps what it deleted for ctrl+y. A kill right
// after another adds to its text, after it for a kill forward and before
// it for one backward, as Emacs does, so ctrl+k ctrl+k yanks both.
func (c *Composer) kill(op Op, after bool) {
	before := []rune(c.Value())
	switch op {
	case OpDeleteWordBackward:
		c.deleteWordBackward()
	case OpDeleteWordForward:
		c.deleteWordForward()
	case OpDeleteAfterCursor:
		if c.col >= len(c.cur()) {
			c.joinBelow()
		} else {
			c.deleteRange(c.col, len(c.cur()))
		}
	case OpDeleteBeforeCursor:
		if c.col == 0 {
			c.joinAbove()
		} else {
			c.deleteRange(0, c.col)
		}
	}
	at := c.cursorOffset()
	gone := before[at : at+len(before)-len([]rune(c.Value()))]
	if len(gone) == 0 {
		c.killing = after
		return
	}
	switch {
	case !after:
		c.killed = slices.Clone(gone)
	case op == OpDeleteWordBackward || op == OpDeleteBeforeCursor:
		c.killed = append(slices.Clone(gone), c.killed...)
	default:
		c.killed = append(c.killed, gone...)
	}
	c.killing = true
}

// cursorOffset is the cursor's rune offset in Value.
func (c *Composer) cursorOffset() int {
	n := c.col
	for _, l := range c.lines[:c.row] {
		n += len(l.text) + 1
	}

	return n
}

// edits reports whether op deletes text, so that it deletes the selection
// instead.
func (c *Composer) edits(op Op) bool {
	switch op {
	case OpDeleteCharacterBackward, OpDeleteCharacterForward, OpDeleteWordBackward,
		OpDeleteWordForward, OpDeleteAfterCursor, OpDeleteBeforeCursor:
		return true
	}

	return false
}

// move applies a cursor motion.
func (c *Composer) move(op Op) {
	switch op {
	case OpCharacterForward:
		c.clusterForward()
	case OpCharacterBackward:
		c.clusterBackward(false)
	case OpWordForward:
		c.wordForward(nil)
	case OpWordBackward:
		c.wordBackward()
	case OpLineNext:
		c.moveRows(1)
	case OpLinePrevious:
		c.moveRows(-1)
	case OpLineStart:
		c.setCol(0)
	case OpLineEnd:
		c.setCol(len(c.cur()))
	case OpInputBegin:
		c.row = 0
		c.setCol(0)
	case OpInputEnd:
		c.row = len(c.lines) - 1
		c.setCol(len(c.cur()))
	case OpPageUp:
		c.pageUp()
	case OpPageDown:
		c.pageDown()
	}
}

// clusterAt is the index of the cluster at the cursor in its line's layout.
func (c *Composer) clusterAt() (*layout, int) {
	lay := c.layout(c.row)

	return lay, lay.cluster(c.col)
}

// clusterForward moves one cluster right, or to the next line's start.
func (c *Composer) clusterForward() {
	lay, g := c.clusterAt()
	switch {
	case g < lay.clusters():
		c.setCol(lay.starts[g+1])
	case c.row < len(c.lines)-1:
		c.row++
		c.setCol(0)
	}
}

// clusterBackward moves one cluster left, or to the previous line's end;
// with inside, on to its last cluster, as textarea's characterLeft.
func (c *Composer) clusterBackward(inside bool) {
	if c.col == 0 && c.row > 0 {
		c.row--
		c.setCol(len(c.cur()))
		if !inside {
			return
		}
	}
	if lay, g := c.clusterAt(); g > 0 {
		c.setCol(lay.starts[g-1])
	}
}

// spaceAt reports whether cluster g of the cursor's line is whitespace.
func (c *Composer) spaceAt(lay *layout, g int) bool { return lay.spaces[g] }

// wordBackward moves to the start of the word before the cursor, across
// lines, as textarea's wordLeft.
func (c *Composer) wordBackward() {
	for {
		row, col := c.row, c.col
		c.clusterBackward(true)
		if c.row == row && c.col == col {
			return
		}
		if lay, g := c.clusterAt(); g < lay.clusters() && !c.spaceAt(lay, g) {
			break
		}
	}
	for {
		lay, g := c.clusterAt()
		if g == 0 || c.spaceAt(lay, g-1) {
			return
		}
		c.setCol(lay.starts[g-1])
	}
}

// wordForward moves past the end of the next word, across lines, as
// textarea's wordRight. fn, if set, maps the word's runes on the way, the
// index counting the word's runes.
func (c *Composer) wordForward(fn func(i int, r rune) rune) {
	for {
		lay, g := c.clusterAt()
		if g < lay.clusters() && !c.spaceAt(lay, g) {
			break
		}
		if c.row == len(c.lines)-1 && g == lay.clusters() {
			return
		}
		c.clusterForward()
	}
	lay, g := c.clusterAt()
	end := g
	for end < lay.clusters() && !c.spaceAt(lay, end) {
		end++
	}
	if fn != nil {
		text := slices.Clone(c.cur())
		from, to := lay.starts[g], lay.starts[end]
		for i := from; i < to; i++ {
			text[i] = fn(i-from, text[i])
		}
		c.setLine(c.row, text)
		lay = c.layout(c.row)
		end = lay.cluster(to)
	}
	c.setCol(lay.starts[end])
}

// deleteClusterBackward deletes the cluster before the cursor, or joins
// the line to the one above.
func (c *Composer) deleteClusterBackward() {
	if c.col == 0 {
		c.joinAbove()

		return
	}
	lay, g := c.clusterAt()
	c.deleteRange(lay.starts[g-1], c.col)
}

// deleteClusterForward deletes the cluster at the cursor, or joins the
// line below.
func (c *Composer) deleteClusterForward() {
	lay, g := c.clusterAt()
	if g >= lay.clusters() {
		c.joinBelow()

		return
	}
	c.deleteRange(c.col, lay.starts[g+1])
}

// deleteWordBackward deletes the spaces before the cursor and the word
// before them, or joins the line to the one above.
func (c *Composer) deleteWordBackward() {
	if c.col == 0 {
		c.joinAbove()

		return
	}
	lay, g := c.clusterAt()
	from := g
	for from > 0 && c.spaceAt(lay, from-1) {
		from--
	}
	for from > 0 && !c.spaceAt(lay, from-1) {
		from--
	}
	c.deleteRange(lay.starts[from], c.col)
}

// deleteWordForward deletes the spaces after the cursor and the word after
// them, or joins the line below.
func (c *Composer) deleteWordForward() {
	lay, g := c.clusterAt()
	if g >= lay.clusters() {
		c.joinBelow()

		return
	}
	to := g
	for to < lay.clusters() && c.spaceAt(lay, to) {
		to++
	}
	for to < lay.clusters() && !c.spaceAt(lay, to) {
		to++
	}
	c.deleteRange(c.col, lay.starts[to])
}

// transpose swaps the clusters before and at the cursor, and moves the
// cursor right; at the line's end, it swaps the last two, as textarea's
// transposeLeft.
func (c *Composer) transpose() {
	lay, g := c.clusterAt()
	if g == 0 || lay.clusters() < 2 {
		return
	}
	if g == lay.clusters() {
		g--
	}
	text := c.cur()
	a := text[lay.starts[g-1]:lay.starts[g]]
	b := text[lay.starts[g]:lay.starts[g+1]]
	swapped := slices.Concat(text[:lay.starts[g-1]], b, a, text[lay.starts[g+1]:])
	c.setLine(c.row, swapped)
	c.setCol(lay.starts[g-1] + len(b) + len(a))
}

// moveRows moves the cursor delta visual rows down, or up when negative,
// across lines, keeping its cell column: the larger of the goal column
// and the cursor's own, as textarea's. On the first row ↑ stays, and on
// the last ↓ stays.
func (c *Composer) moveRows(delta int) {
	goal := max(c.goal, c.LineInfo().CharOffset)
	lay, g := c.clusterAt()
	r := lay.row(g)
	for ; delta > 0; delta-- {
		switch {
		case r+1 < len(lay.rows):
			r++
		case c.row < len(c.lines)-1:
			c.row++
			lay, r = c.layout(c.row), 0
		}
	}
	for ; delta < 0; delta++ {
		switch {
		case r > 0:
			r--
		case c.row > 0:
			c.row--
			lay = c.layout(c.row)
			r = len(lay.rows) - 1
		}
	}
	// The cursor stays on row r: at a cell boundary at or past the goal,
	// and before the row's end unless it is the line's last row.
	g, last := lay.rows[r], lay.rowEnd(r)
	if r+1 < len(lay.rows) {
		last--
	}
	for cells := 0; cells < goal && g < last; g++ {
		cells += lay.cells[g]
	}
	c.col, c.goal = lay.starts[g], goal
}

// pageUp moves the cursor to the first visible row, or a page up from it.
func (c *Composer) pageUp() {
	if off := c.offset - c.cursorRow(); off < 0 {
		c.moveRows(off)

		return
	}
	c.moveRows(-c.height)
}

// pageDown moves the cursor to the last visible row, or a page down from
// it.
func (c *Composer) pageDown() {
	if off := c.cursorRow() - c.offset; off < c.height-1 {
		c.moveRows(c.height - 1 - off)

		return
	}
	c.moveRows(c.height)
}
