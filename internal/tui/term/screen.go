package term

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Screen is the line renderer: it remembers each row the terminal shows and
// writes only what changed. A frame is a list of lines, one per row, each
// a string with SGR styling. The terminal runs with autowrap off, so no
// width is measured to keep a line on its row: a line the terminal counts
// wider than the row stays on it (its last cells overwrite the last
// column), and every write erases what was there first, so one it counts
// narrower leaves nothing behind. A line wider than the row by uah's own
// count is cut to the row (clip).
//
// A line may hold OSC 8 hyperlinks: they take no cells in any measure,
// a row that holds one before its change is written whole (only SGR
// sequences count as safe before a change), and each written row ends any
// link it opened.
//
// Three things keep a frame small:
//   - Rows whose line did not change are not written.
//   - A changed row is written from the first cell that changed, when
//     everything before it is text whose width all terminals agree on;
//     otherwise the whole row is.
//   - When the rows moved up or down (a transcript that grows at the
//     bottom, or a scroll), the terminal moves them itself in a scroll
//     region (DECSTBM with SU or SD), and only the rows that came in are
//     written.
//
// A frame is one write, hidden from view while it draws with synchronized
// output (mode 2026) when the terminal supports it, and the cursor hidden
// until it is placed.
type Screen struct {
	w, h int
	// shown is each row's line as last written; nil forces a full redraw.
	shown []string
	// cursor is where the cursor was left, and whether it shows.
	cursor     Position
	cursorShow bool
	// sync wraps a frame in mode 2026.
	sync bool
	// noScroll and noSpans turn the scroll and span optimizations off, for
	// tests and measurements.
	noScroll, noSpans bool
	buf               bytes.Buffer
	out               []byte
}

// NewScreen returns a screen w by h cells that shows nothing yet.
func NewScreen(w, h int) *Screen {
	s := &Screen{w: max(w, 1), h: max(h, 1)}
	s.Invalidate()

	return s
}

// Resize sets the screen's size and forgets what it shows, so the next
// frame draws it all.
func (s *Screen) Resize(w, h int) {
	s.w, s.h = max(w, 1), max(h, 1)
	s.Invalidate()
}

// Invalidate forgets what the terminal shows, so the next frame clears it
// and draws it all, as after the terminal was used by another program.
// The cursor may show then too, so the next frame hides it unless it
// places it.
func (s *Screen) Invalidate() { s.shown, s.cursorShow = nil, true }

// SetSync turns synchronized output (mode 2026) on or off.
func (s *Screen) SetSync(on bool) { s.sync = on }

// Size returns the screen's size.
func (s *Screen) Size() (w, h int) { return s.w, s.h }

// Frame returns the bytes that turn what the terminal shows into lines,
// with the cursor at cur, or hidden when cur is nil. Lines past the
// screen's height are dropped from the top. It returns nil when nothing
// changed. The bytes are valid until the next call.
func (s *Screen) Frame(lines []string, cur *Cursor) []byte {
	if len(lines) > s.h {
		lines = lines[len(lines)-s.h:]
	}
	s.rows(lines)
	show := cur != nil
	var at Position
	if show {
		at = Position{X: min(max(cur.X, 0), s.w-1), Y: min(max(cur.Y, 0), s.h-1)}
	}
	drawn := s.buf.Len() > 0
	if !drawn && show == s.cursorShow && (!show || at == s.cursor) {
		return nil
	}
	s.out = s.wrap(s.out[:0], drawn, show, at)
	s.cursor, s.cursorShow = at, show

	return s.out
}

// rows writes into buf what turns the shown rows into lines.
func (s *Screen) rows(lines []string) {
	s.buf.Reset()
	full := s.shown == nil
	if full {
		s.shown = make([]string, s.h)
		s.buf.WriteString("\x1b[m\x1b[H\x1b[2J")
	} else if !s.noScroll {
		s.scroll(lines)
	}
	for y := range s.h {
		line := ""
		if y < len(lines) {
			line = lines[y]
		}
		if line == s.shown[y] && !full {
			continue
		}
		if !full {
			s.writeRow(y, s.shown[y], line)
		} else if line != "" {
			s.move(y, 0)
			s.writeClipped(line, s.w)
		}
		s.shown[y] = line
	}
}

// wrap appends the frame to out: the rows written (drawn), inside
// synchronized output and with the cursor hidden, then the cursor placed
// at at or hidden.
func (s *Screen) wrap(out []byte, drawn, show bool, at Position) []byte {
	if drawn && s.sync {
		out = append(out, "\x1b[?2026h"...)
	}
	if drawn && s.cursorShow {
		out = append(out, "\x1b[?25l"...)
	}
	out = append(out, s.buf.Bytes()...)
	if show {
		out = appendMove(out, at.Y, at.X)
		if drawn || !s.cursorShow {
			out = append(out, "\x1b[?25h"...)
		}
	} else if !drawn && s.cursorShow {
		out = append(out, "\x1b[?25l"...)
	}
	if drawn && s.sync {
		out = append(out, "\x1b[?2026l"...)
	}

	return out
}

// writeRow rewrites row y, which shows old, to show line: from the first
// cell that differs when the text before it is safe to count, else whole.
func (s *Screen) writeRow(y int, old, line string) {
	if !s.noSpans {
		if at, col, sgr, ok := divergence(old, line); ok && col < s.w {
			// The erase goes first: with autowrap off the cursor stays on
			// the last column after writing it, so an erase after a line
			// that fills the row would clear its last cell.
			s.move(y, col)
			s.buf.WriteString("\x1b[m\x1b[K")
			s.buf.WriteString(sgr)
			s.writeClipped(line[at:], s.w-col)

			return
		}
	}
	s.move(y, 0)
	s.buf.WriteString("\x1b[m\x1b[2K")
	s.writeClipped(line, s.w)
}

// writeClipped writes line cut to w cells, then ends its styles and any
// hyperlink (OSC 8) it opened, so a link cut with the row, or left open,
// does not run on into the next row written.
func (s *Screen) writeClipped(line string, w int) {
	s.buf.WriteString(clip(line, w))
	if strings.Contains(line, "\x1b]8;") {
		s.buf.WriteString("\x1b]8;;\x1b\\")
	}
	s.buf.WriteString("\x1b[m")
}

// clip cuts line to w cells, so a line wider than the row does not
// overwrite its last column (autowrap is off). The renderer keeps lines
// within the width; this is the safety net. A line of at most w bytes
// cannot be wider, so most lines are not measured.
func clip(line string, w int) string {
	if len(line) <= w {
		return line
	}

	return ansi.Truncate(line, w, "")
}

func (s *Screen) move(y, x int) { s.buf.Write(appendMove(s.buf.AvailableBuffer(), y, x)) }

func appendMove(b []byte, y, x int) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(y+1), 10)
	if x > 0 {
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(x+1), 10)
	}

	return append(b, 'H')
}

// divergence finds where line first differs from old: the byte offset in
// line, the column it starts on, and the SGR sequences in force there. ok
// is false when the common part holds anything whose width or effect a
// terminal might count otherwise (a rune outside the narrow ranges, an
// escape other than SGR) or when the change starts inside a grapheme that
// may extend backwards, so the caller writes the whole row.
func divergence(old, line string) (at, col int, sgr string, ok bool) {
	n := min(len(old), len(line))
	i := 0
	for i < n && old[i] == line[i] {
		i++
	}
	// Walk the common prefix token by token; at is the last token
	// boundary at or before i.
	reset := 0 // where the SGR state was last reset
	for p := 0; p < i; {
		c := line[p]
		if c == 0x1b {
			end, isSGR := sgrEnd(line, p)
			if !isSGR {
				return 0, 0, "", false
			}
			if end > i {
				break // the change is inside this sequence
			}
			if end-p <= 4 && (line[p:end] == "\x1b[m" || line[p:end] == "\x1b[0m") {
				reset = end
			}
			p = end
			at = p

			continue
		}
		r, size := utf8.DecodeRuneInString(line[p:])
		if !narrow(r) {
			return 0, 0, "", false
		}
		if p+size > i {
			break // the change is inside this rune
		}
		p += size
		col++
		at = p
	}
	// The rune that starts the change must not join the cell before it
	// (a combining mark, a joiner, a variation selector), in either line.
	if !startsCell(line, at) || !startsCell(old, at) {
		return 0, 0, "", false
	}

	return at, col, collectSGR(line[reset:at]), true
}

// startsCell reports whether s at i is at its end, an escape, or a rune
// that begins a cell of its own.
func startsCell(s string, i int) bool {
	if i >= len(s) || s[i] == 0x1b {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[i:])

	return narrow(r)
}

// collectSGR returns the SGR sequences in s, which holds only SGR
// sequences and narrow text.
func collectSGR(s string) string {
	var b []byte
	for p := 0; p < len(s); {
		if s[p] != 0x1b {
			p++

			continue
		}
		end, _ := sgrEnd(s, p)
		b = append(b, s[p:end]...)
		p = end
	}

	return string(b)
}

// sgrEnd returns the end of the escape sequence at s[p] and whether it is
// a complete SGR sequence (CSI params m).
func sgrEnd(s string, p int) (end int, ok bool) {
	if p+1 >= len(s) || s[p+1] != '[' {
		return p + 1, false
	}
	for q := p + 2; q < len(s); q++ {
		c := s[q]
		switch {
		case c >= '0' && c <= '9', c == ';', c == ':':
		case c == 'm':
			return q + 1, true
		default:
			return q + 1, false
		}
	}

	return len(s), false
}

// narrow reports whether every terminal draws r in exactly one cell, by
// itself: printable ASCII and Latin, Greek, Cyrillic, general punctuation,
// arrows, box drawing, blocks, geometric shapes, and braille (uah's
// spinner). Anything else, such as CJK, emoji, or a combining mark, makes
// the row be written whole.
func narrow(r rune) bool {
	if r >= 0x20 && r < 0x7f {
		return true
	}
	switch r {
	case 0xad, 0x25b6, 0x25c0, 0x25fb, 0x25fc, 0x25fd, 0x25fe: // soft hyphen; emoji-capable shapes
		return false
	}
	for _, span := range narrowRanges {
		if r >= span[0] && r < span[1] {
			return true
		}
	}

	return false
}

// narrowRanges are the ranges narrow accepts beyond ASCII, [from, to).
var narrowRanges = [...][2]rune{
	{0xa0, 0x300},    // Latin-1 and Latin Extended
	{0x370, 0x483},   // Greek and Cyrillic letters
	{0x2010, 0x2028}, // punctuation: – — ‘ ’ “ ” • …
	{0x2030, 0x205f}, // ‰ ′ and more punctuation
	{0x2190, 0x2200}, // arrows
	{0x2500, 0x2600}, // box drawing, blocks, geometric shapes
	{0x2800, 0x2900}, // braille
}

// scroll finds the shift k that keeps the most rows, and when it keeps more
// than leaving the rows in place does, moves the rows between the first and
// the last kept one with the scroll region and updates shown to match.
func (s *Screen) scroll(lines []string) {
	n := min(len(lines), s.h)
	same := 0
	for y := range n {
		if lines[y] == s.shown[y] {
			same++
		}
	}
	best, bestK := same, 0
	for k := 1 - n; k < n; k++ {
		if k == 0 {
			continue
		}
		kept := 0
		for y := max(0, -k); y < n && y+k < s.h; y++ {
			if lines[y] != "" && lines[y] == s.shown[y+k] {
				kept++
			}
		}
		if kept > best+2 {
			best, bestK = kept, k
		}
	}
	if bestK == 0 {
		return
	}
	// The region: from the first row whose line moved to the last.
	top, bot := -1, -1
	for y := max(0, -bestK); y < n && y+bestK < s.h; y++ {
		if lines[y] != "" && lines[y] == s.shown[y+bestK] {
			if top < 0 {
				top = y
			}
			bot = y
		}
	}
	// Rows top..bot take their lines from top+k..bot+k, so the region spans
	// both.
	k := bestK
	rt, rb := min(top, top+k), max(bot, bot+k)
	b := append(s.buf.AvailableBuffer(), "\x1b[m\x1b["...)
	b = strconv.AppendInt(b, int64(rt+1), 10)
	b = append(b, ';')
	b = strconv.AppendInt(b, int64(rb+1), 10)
	b = append(b, "r\x1b["...)
	if k > 0 {
		b = strconv.AppendInt(b, int64(k), 10)
		b = append(b, 'S')
		copy(s.shown[rt:rb+1-k], s.shown[rt+k:rb+1])
		clear(s.shown[rb+1-k : rb+1])
	} else {
		b = strconv.AppendInt(b, int64(-k), 10)
		b = append(b, 'T')
		copy(s.shown[rt-k:rb+1], s.shown[rt:rb+1+k])
		clear(s.shown[rt : rt-k])
	}
	b = append(b, "\x1b[r"...)
	s.buf.Write(b)
}
