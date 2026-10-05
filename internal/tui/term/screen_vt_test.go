package term

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// These tests play Screen's frames into a VT emulator and check every cell,
// its content and style, against ultraviolet's own reading of the frame's
// lines, and the cursor. The frames are uah's own (a streamed answer, then
// scrolling; typing in the composer) and random edits, shifts and resizes,
// with each optimization on and off and with synchronized output.

// variant is one way to run a Screen: with or without each optimization,
// and with or without synchronized output.
type variant struct{ noScroll, noSpans, sync bool }

func (v variant) String() string {
	return fmt.Sprintf("noScroll=%v,noSpans=%v,sync=%v", v.noScroll, v.noSpans, v.sync)
}

func (v variant) screen(w, h int) *Screen {
	s := NewScreen(w, h)
	s.noScroll, s.noSpans = v.noScroll, v.noSpans
	s.SetSync(v.sync)

	return s
}

func variants() []variant {
	var out []variant
	for i := range 8 {
		out = append(out, variant{noScroll: i&1 != 0, noSpans: i&2 != 0, sync: i&4 != 0})
	}

	return out
}

// vtTerm is a VT emulator with autowrap off, as uah runs the terminal, and
// whether its cursor shows.
type vtTerm struct {
	em      *vt.Emulator
	visible bool
}

func newVTTerm(w, h int) *vtTerm {
	v := &vtTerm{em: vt.NewEmulator(w, h), visible: true}
	v.em.SetCallbacks(vt.Callbacks{CursorVisibility: func(on bool) { v.visible = on }})
	_, _ = v.em.WriteString("\x1b[?7l")

	return v
}

func (v *vtTerm) resize(w, h int) { v.em.Resize(w, h) }

func (v *vtTerm) write(b []byte) { _, _ = v.em.Write(b) }

// expected is ultraviolet's reading of a frame: the last h lines drawn
// each on its row. The emulator counts widths by grapheme, so the buffer
// does too.
func expected(w, h int, lines []string) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(w, h)
	buf.Method = ansi.GraphemeWidth
	redraw(buf, lines)

	return buf
}

// redraw makes buf ultraviolet's reading of another frame of its size.
func redraw(buf uv.ScreenBuffer, lines []string) {
	if h := buf.Height(); len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	buf.Clear()
	for y, l := range lines {
		uv.NewStyledString(l).Draw(buf, uv.Rect(0, y, buf.Width(), 1))
	}
}

// mismatch returns how the emulator differs from want and the cursor, or
// "" when it matches.
func (v *vtTerm) mismatch(want uv.ScreenBuffer, lines []string, cur *Cursor) string {
	w, h := want.Width(), want.Height()
	if v.em.Width() != w || v.em.Height() != h {
		return fmt.Sprintf("emulator is %dx%d, want %dx%d", v.em.Width(), v.em.Height(), w, h)
	}
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for y := range h {
		for x := range w {
			got, exp := v.em.CellAt(x, y), want.CellAt(x, y)
			if got == nil || exp == nil {
				return fmt.Sprintf("no cell at %d,%d", x, y)
			}
			if got.Content != exp.Content || got.Width != exp.Width || !got.Style.Equal(&exp.Style) || !got.Link.Equal(&exp.Link) {
				line := ""
				if y < len(lines) {
					line = lines[y]
				}

				return fmt.Sprintf("cell %d,%d: got %q (w%d) %+v %+v, want %q (w%d) %+v %+v\nline %q", x, y, got.Content, got.Width, got.Style, got.Link, exp.Content, exp.Width, exp.Style, exp.Link, line)
			}
		}
	}
	if cur == nil {
		if v.visible {
			return "cursor shows, want hidden"
		}

		return ""
	}
	at := uv.Pos(min(max(cur.X, 0), w-1), min(max(cur.Y, 0), h-1))
	if !v.visible {
		return "cursor hidden, want shown"
	}
	if got := v.em.CursorPosition(); got != at {
		return fmt.Sprintf("cursor at %v, want %v", got, at)
	}

	return ""
}

// play draws frames on a fresh Screen and emulator with each optimization
// on and off, and checks each against ultraviolet's reading of it. Half
// the runs use synchronized output; the random frames run every variant.
func play(t *testing.T, w, h int, frames [][]string, cur func(i int) *Cursor) {
	t.Helper()
	for _, v := range []variant{{}, {noScroll: true, sync: true}, {noSpans: true, sync: true}, {noScroll: true, noSpans: true}} {
		t.Run(v.String(), func(t *testing.T) {
			t.Parallel()
			s, term := v.screen(w, h), newVTTerm(w, h)
			want := expected(w, h, nil)
			for i, ls := range frames {
				c := cur(i)
				term.write(s.Frame(ls, c))
				redraw(want, ls)
				if m := term.mismatch(want, ls, c); m != "" {
					t.Fatalf("frame %d: %s", i, m)
				}
			}
		})
	}
}

// The real frames: uah drawing a streamed answer at 140x45, as the profile
// does, and typing in the composer.

const fw, fh = 140, 45

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func apply(s state.State, evs ...any) state.State {
	for _, ev := range evs {
		s, _ = state.Reduce(s, ev)
	}

	return s
}

func base() state.State {
	return apply(state.New(t0), session.SessionOpened{
		At: t0, ID: "3f2a1b2c-0000-4000-8000-000000000000", Engine: "embedded",
		Settings: session.Settings{Provider: "openai-codex", Model: "gpt-6-sol", Effort: "high", Workspace: "/workspace/proj", Sandbox: "workspace-write"},
	})
}

// pieces is the profile's fakesrv answer.
func pieces(n int) []string {
	out := make([]string, 0, n)
	for i := 0; len(out) < n; i++ {
		switch i % 10 {
		case 0:
			out = append(out, fmt.Sprintf("\n\n## Section %d\n\n", i/10+1))
		case 3:
			out = append(out, "\n\n```go\nfunc handle(ctx context.Context, r *Request) error {\n")
		case 4:
			out = append(out, fmt.Sprintf("\tif err := validate(r); err != nil {\n\t\treturn fmt.Errorf(\"step %d: %%w\", err)\n\t}\n", i))
		case 5:
			out = append(out, "\treturn nil\n}\n```\n\n")
		case 6, 7:
			out = append(out, fmt.Sprintf("- item %d: the **store** pages `items`, 256 at a time;\n", i))
		default:
			out = append(out, fmt.Sprintf("The handler checks the session and the token before step %d, ", i))
		}
	}

	return out
}

var streamCursor = NewCursor(2, fh-3)

// streamFrames draws a frame after every second of n pieces and a 100 ms
// tick, as the 16 ms batches and the clock do while the answer streams,
// then pages up and down, and a line at a time, as the scroll phase does.
func streamFrames(n int) [][]string {
	s := apply(base(), core.RunStarted{At: t0, RunID: "r1"}, core.TurnStarted{At: t0, Turn: 1})
	c := render.NewCache(render.Amber)
	f := render.Frame{Width: fw, Height: fh, Composer: "λ ", ComposerHeight: 1}
	var frames [][]string
	for i, p := range pieces(n) {
		at := t0.Add(time.Duration(i) * 50 * time.Millisecond)
		s = apply(s, engine.TextDelta{At: at, ItemID: "msg", Text: p, Final: true}, state.Tick{Now: at})
		if i%2 == 1 {
			out, _ := render.Screen(s, c, f)
			frames = append(frames, strings.Split(out, "\n"))
		}
	}
	for _, d := range []int{22, 22, 22, 1, 1, 1, -1, -1, -22, -22, -22, -1} {
		s = apply(s, state.ScrollBy{Lines: d})
		out, _ := render.Screen(s, c, f)
		frames = append(frames, strings.Split(out, "\n"))
	}

	return frames
}

// typingFrames types a draft into the composer a character at a time below
// a finished answer, then moves the cursor back over it without a change,
// then deletes it from the end.
func typingFrames() (frames [][]string, cursors []*Cursor) {
	s := apply(base(), core.RunStarted{At: t0, RunID: "r1"}, core.TurnStarted{At: t0, Turn: 1})
	for i, p := range pieces(40) {
		s = apply(s, engine.TextDelta{At: t0.Add(time.Duration(i) * time.Millisecond), ItemID: "msg", Text: p, Final: true})
	}
	c := render.NewCache(render.Amber)
	draw := func(draft string, x int) {
		out, row := render.Screen(s, c, render.Frame{Width: fw, Height: fh, Composer: "λ " + draft, ComposerHeight: 1, Draft: draft})
		frames = append(frames, strings.Split(out, "\n"))
		cursors = append(cursors, NewCursor(x, row))
	}
	typed := []rune("fix the λ handler: 日本語 ⠋ ─ ok 🙂 done")
	for n := range len(typed) + 1 {
		d := string(typed[:n])
		draw(d, 2+ansi.StringWidth(d))
	}
	full := string(typed)
	for n := len(typed); n >= 0; n-- {
		draw(full, 2+ansi.StringWidth(string(typed[:n])))
	}
	for n := len(typed); n >= 0; n-- {
		d := string(typed[:n])
		draw(d, 2+ansi.StringWidth(d))
	}

	return frames, cursors
}

func TestScreenMatchesStreamFrames(t *testing.T) {
	frames := streamFrames(200)
	play(t, fw, fh, frames, func(int) *Cursor { return streamCursor })
}

func TestScreenMatchesTypingFrames(t *testing.T) {
	frames, cursors := typingFrames()
	play(t, fw, fh, frames, func(i int) *Cursor { return cursors[i] })
}

// Random frames. A row is spans of styled text, each text a list of
// graphemes, so an edit never splits a grapheme and a combining mark never
// follows an escape (uah's lines never put one there).

// graphemes is the random alphabet. Each draws the same width in the
// emulator and in ultraviolet's buffer, both counting by grapheme: narrow
// text the span rewrite counts, and wide, emoji, combined and other text
// that makes it write the row whole.
var graphemes = []string{
	"a", "b", "c", "x", "y", "z", "A", "Q", "0", "7", " ", " ", " ", ".", ",", "-", "_", "(", ")", "[", "]", "/", "\\", "|", "*", "#", "~", "`", "'", "\"", "%",
	"λ", "α", "Ω", "ж", "é", "ñ", "ß", "—", "–", "…", "•", "“", "”", "→", "←", "↑",
	"─", "│", "┌", "┐", "└", "┘", "├", "█", "▌", "░", "■", "●", "◆", "▸", "▾",
	"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⣿",
	"é", "ä", "ñ", "ộ",
	"日", "本", "語", "中", "한", "ア",
	"🙂", "👍", "🚀", "✅", "👍🏽", "👩‍💻", "🇺🇸",
	"✓", "★", "⚠", "▶", "‼",
}

var sgrs = []string{
	"", "", "", "\x1b[m", "\x1b[0m", "\x1b[1m", "\x1b[2m", "\x1b[3m", "\x1b[4m", "\x1b[7m", "\x1b[9m",
	"\x1b[22m", "\x1b[23m", "\x1b[24m", "\x1b[39m", "\x1b[49m",
	"\x1b[31m", "\x1b[32m", "\x1b[94m", "\x1b[38;5;208m", "\x1b[38;2;10;200;30m", "\x1b[48;5;236m", "\x1b[48;2;40;40;60m",
	"\x1b[0;32m", "\x1b[1;4;35m", "\x1b[4:3m", "\x1b[58;5;196m",
	// OSC 8 hyperlinks, as file links draw them, and their end.
	"\x1b]8;;file://host/w/a.go\x1b\\", "\x1b]8;line=12-20;file://host/w/b%20c.go\x1b\\", "\x1b]8;;\x1b\\",
}

type span struct {
	sgr  string
	text []string
}

type row struct {
	spans []span
	// open leaves the line's style unreset at its end.
	open bool
}

func (r row) String() string {
	var b strings.Builder
	for _, sp := range r.spans {
		b.WriteString(sp.sgr)
		for _, g := range sp.text {
			b.WriteString(g)
		}
	}
	if len(r.spans) > 0 && !r.open {
		b.WriteString("\x1b[m")
	}

	return b.String()
}

// fit drops graphemes from the end until the row is at most w cells wide,
// so the terminal never cuts it (with autowrap off it would overwrite its
// last cell instead).
func (r *row) fit(w int) {
	used := 0
	for i := range r.spans {
		for j, g := range r.spans[i].text {
			used += ansi.StringWidth(g)
			if used > w {
				r.spans[i].text = r.spans[i].text[:j]
				r.spans = r.spans[:i+1]

				return
			}
		}
	}
}

type gen struct {
	r *rand.Rand
	w int
}

func (g gen) text(n int) []string {
	out := make([]string, n)
	for i := range out {
		if g.r.IntN(3) > 0 {
			out[i] = graphemes[g.r.IntN(31)] // mostly ASCII
		} else {
			out[i] = graphemes[g.r.IntN(len(graphemes))]
		}
	}

	return out
}

func (g gen) sgr() string { return sgrs[g.r.IntN(len(sgrs))] }

func (g gen) row() row {
	if g.r.IntN(8) == 0 {
		return row{}
	}
	var r row
	for range 1 + g.r.IntN(4) {
		r.spans = append(r.spans, span{sgr: g.sgr(), text: g.text(g.r.IntN(g.w/2 + 1))})
	}
	r.open = g.r.IntN(6) == 0
	r.fit(g.w)

	return r
}

// edit changes r a little, as a streamed line, a spinner, a clock or the
// composer does.
func (g gen) edit(r row) row {
	r = row{spans: clonespans(r.spans), open: r.open}
	if len(r.spans) == 0 {
		r.spans = []span{{sgr: g.sgr()}}
	}
	i := g.r.IntN(len(r.spans))
	sp := &r.spans[i]
	at := g.r.IntN(len(sp.text) + 1)
	switch g.r.IntN(9) {
	case 0, 1: // insert
		sp.text = append(sp.text[:at], append(g.text(1+g.r.IntN(3)), sp.text[at:]...)...)
	case 2: // delete
		if at < len(sp.text) {
			sp.text = append(sp.text[:at], sp.text[at+1+g.r.IntN(len(sp.text)-at):]...)
		}
	case 3, 4: // replace
		if at < len(sp.text) {
			sp.text[at] = g.text(1)[0]
		}
	case 5: // restyle a span
		sp.sgr = g.sgr()
	case 6: // a new style mid-span
		tail := span{sgr: g.sgr(), text: append([]string(nil), sp.text[at:]...)}
		sp.text = sp.text[:at]
		r.spans = append(r.spans[:i+1], append([]span{tail}, r.spans[i+1:]...)...)
	case 7: // append, as streaming does
		last := &r.spans[len(r.spans)-1]
		last.text = append(last.text, g.text(1+g.r.IntN(6))...)
	case 8:
		return g.row()
	}
	r.fit(g.w)

	return r
}

func clonespans(in []span) []span {
	out := make([]span, len(in))
	for i, sp := range in {
		out[i] = span{sgr: sp.sgr, text: append([]string(nil), sp.text...)}
	}

	return out
}

func strs(rows []row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.String()
	}

	return out
}

// TestScreenRandomFrames draws random frames: rows edited, the rows between
// a fixed header and footer shifted as a growing transcript or a scroll
// moves them, resizes, redraws, more or fewer lines than rows, and the
// cursor moved, shown or hidden alone.
func TestScreenRandomFrames(t *testing.T) {
	const frames = 600
	for vi, v := range variants() {
		t.Run(v.String(), func(t *testing.T) {
			t.Parallel()
			r := rand.New(rand.NewPCG(uint64(vi), 0x5eed))
			w, h := 8+r.IntN(60), 3+r.IntN(18)
			g := gen{r: r, w: w}
			rows := make([]row, h)
			for y := range rows {
				rows[y] = g.row()
			}
			s, term := v.screen(w, h), newVTTerm(w, h)
			var cur *Cursor
			var prev []string
			var seen coverage
			for i := range frames {
				hdr, ftr := min(r.IntN(3), h-1), min(r.IntN(4), h-1)
				var lines []string
				op := r.IntN(100)
				switch {
				case op < 35: // edit a few rows
					for range 1 + r.IntN(3) {
						y := r.IntN(h)
						rows[y] = g.edit(rows[y])
					}
				case op < 60: // shift the middle, edit the footer
					top, bot := hdr, h-ftr // rows top..bot-1 move
					if m := bot - top; m >= 2 {
						d := 1 + r.IntN(m-1)
						mid := rows[top:bot]
						if r.IntN(2) == 0 { // up, new rows at the bottom
							copy(mid, mid[d:])
							for y := m - d; y < m; y++ {
								mid[y] = g.row()
							}
						} else {
							copy(mid[d:], mid[:m-d])
							for y := range d {
								mid[y] = g.row()
							}
						}
					}
					if ftr > 0 && r.IntN(2) == 0 {
						rows[h-1] = g.edit(rows[h-1])
					}
				case op < 70: // the cursor alone
				case op < 75: // a resize
					w, h = 8+r.IntN(60), 3+r.IntN(18)
					g.w = w
					for len(rows) < h {
						rows = append(rows, g.row())
					}
					rows = rows[:h]
					for y := range rows {
						rows[y].fit(w)
					}
					s.Resize(w, h)
					term.resize(w, h)
				case op < 78:
					s.Invalidate()
				case op < 82: // more lines than rows: the top ones drop
					extra := make([]row, 1+r.IntN(3))
					for k := range extra {
						extra[k] = g.row()
					}
					lines = strs(append(extra, rows...))
				case op < 86: // fewer lines than rows: the rest are blank
					k := r.IntN(h)
					clear(rows[k:])
				default: // nothing changed
				}
				moved := op >= 60 && op < 70 || r.IntN(4) == 0
				if moved {
					switch r.IntN(4) {
					case 0:
						cur = nil
					default:
						cur = NewCursor(r.IntN(w+4)-2, r.IntN(h+4)-2)
					}
				}
				if lines == nil {
					lines = strs(rows)
					if op >= 82 && op < 86 {
						for len(lines) > 0 && lines[len(lines)-1] == "" {
							lines = lines[:len(lines)-1]
						}
					}
				}
				out := s.Frame(lines, cur)
				if op >= 86 && !moved && slicesEqual(prev, lines) && out != nil {
					t.Fatalf("frame %d: nothing changed, got %q", i, out)
				}
				seen.count(out)
				term.write(out)
				if m := term.mismatch(expected(w, h, lines), lines, cur); m != "" {
					t.Fatalf("frame %d (op %d, %dx%d): %s\nout %q", i, op, w, h, m, out)
				}
				prev = append(prev[:0], lines...)
			}
			seen.check(t, v)
		})
	}
}

var scrollRegion = regexp.MustCompile(`\x1b\[\d+;\d+r`)

// coverage counts the kinds of frames a run wrote, to check the random
// frames reach every path.
type coverage struct{ unchanged, full, scrolls, spans, cursorOnly int }

func (c *coverage) count(out []byte) {
	switch {
	case out == nil:
		c.unchanged++
	case bytes.Contains(out, []byte("\x1b[2J")):
		c.full++
	case !bytes.Contains(out, []byte("\x1b[m")):
		c.cursorOnly++
	}
	c.scrolls += len(scrollRegion.FindAll(out, -1))
	c.spans += bytes.Count(out, []byte("\x1b[m\x1b[K"))
}

func (c *coverage) check(t *testing.T, v variant) {
	t.Helper()
	t.Logf("%+v", *c)
	assert.Positive(t, c.unchanged, "unchanged frames")
	assert.Positive(t, c.full, "full redraws")
	assert.Positive(t, c.cursorOnly, "cursor-only frames")
	if !v.noScroll {
		assert.Positive(t, c.scrolls, "scrolls")
	}
	if !v.noSpans {
		assert.Positive(t, c.spans, "span rewrites")
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// TestScreenLinksCutAtTheRow: a hyperlink cut with its row, or left open,
// ends with the row; a link before a change makes the row be written
// whole, and a link after it is written from the change.
func TestScreenLinksCutAtTheRow(t *testing.T) {
	const w, h = 20, 4
	open, end := "\x1b]8;line=3;file://host/w/a.go\x1b\\", "\x1b]8;;\x1b\\"
	long := "ab " + open + "\x1b[4m" + strings.Repeat("x", 30) + "\x1b[24m" + end + " tail"
	frames := [][]string{
		{long, "plain row", "", "  READ  " + open + "a.go" + end},
		{long, "plain row two", "c " + open + "unclosed", "  READ  " + open + "a.go" + end + " 1s"},
		{"ab " + open + "x" + end + " changed", "plain", "c " + open + "unclosed!", "  RAN   " + open + "a.go" + end},
	}
	play(t, w, h, frames, func(int) *Cursor { return nil })
}
