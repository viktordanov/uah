package render_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// pinned draws a window as the shell does, through one cache: each frame
// reports the window back while it is pinned (state.Anchored), as
// bubble.Model.anchor does after each update.
type pinned struct {
	t    *testing.T
	s    state.State
	c    *render.Cache
	w, h int
}

func pin(t *testing.T, s state.State, w, h int) *pinned {
	t.Helper()

	return &pinned{t: t, s: s, c: render.NewCache(render.Amber), w: w, h: h}
}

// draw lays out a frame, reports it while pinned, and returns its rows,
// without styles, as drawn after the report.
func (p *pinned) draw() []string {
	p.t.Helper()
	raw := p.frame()
	if p.s.Pinned() {
		at, scroll := p.c.Anchor()
		p.s = apply(p.s, state.Anchored{At: at, Scroll: scroll, Width: p.w})
		raw = p.frame()
	}
	lines := strings.Split(raw, "\n")
	require.Len(p.t, lines, p.h, "the screen is exactly the terminal's height")
	for i, l := range lines {
		require.LessOrEqual(p.t, ansi.StringWidth(l), p.w, "row %d is wider than the screen: %q", i, l)
		lines[i] = strings.TrimRight(ansi.Strip(l), " ")
	}

	return lines
}

func (p *pinned) frame() string {
	out, _ := render.Screen(p.s, p.c, render.Frame{Width: p.w, Height: p.h, Composer: "λ ", ComposerHeight: 1})

	return out
}

func (p *pinned) apply(evs ...any) { p.s = apply(p.s, evs...) }

// lastRow is the screen row of the window's last transcript line.
func (p *pinned) lastRow() int {
	for y := range p.h {
		if p.c.Edge(y) > 0 {
			return y - 1
		}
	}

	return p.h - 1
}

// firstRow is the screen row of the window's first line.
func (p *pinned) firstRow() int {
	for y := range p.h {
		if p.c.Edge(y) == 0 {
			return y - 1
		}
	}

	return 0
}

// without is lines without row i.
func without(lines []string, i int) []string {
	return append(append([]string{}, lines[:i]...), lines[i+1:]...)
}

// longRun is a live run's transcript of 60 notices after a streamed answer
// that is still being written, so the working line already shows.
func longRun() state.State {
	s := apply(base(),
		session.InputQueued{At: t0, Input: core.UserInput{ID: "m1", Text: "go"}},
		session.InputSent{At: t0, IDs: []string{"m1"}},
		core.RunStarted{At: t0, RunID: "r1"},
		core.TurnStarted{At: t0, Turn: 1},
		engine.TextDelta{At: t0, ItemID: "early", Text: "an early answer"},
	)
	for i := range 60 {
		s = apply(s, session.Notice{At: t0, Level: "info", Message: fmt.Sprintf("line %02d", i)})
	}

	return s
}

// TestScroll_AnchoredWindow: scrolled up, the window stays on its text
// while output streams in below, new items come, and an item above it
// grows; the pill says so on the last row, and the rest of the screen is
// as it was.
func TestScroll_AnchoredWindow(t *testing.T) {
	p := pin(t, longRun(), 100, 24)
	p.draw()
	p.apply(state.ScrollBy{Lines: 10})
	before := p.draw()
	pillRow := p.lastRow()
	require.Contains(t, strings.Join(before, "\n"), "line 49", "scrolled up ten lines")
	require.NotContains(t, strings.Join(before, "\n"), "New activity")

	for _, step := range []struct {
		name string
		evs  []any
	}{
		{"a streamed answer", []any{engine.TextDelta{At: t0, ItemID: "late", Text: "streaming\nmore\nand more"}}},
		{"it grows", []any{engine.TextDelta{At: t0, ItemID: "late", Text: "\n\nanother paragraph"}}},
		{"new items", []any{session.Notice{At: t0, Level: "info", Message: "new"}, core.ToolCalled{At: t0, CallID: "c1", Name: "Bash", Label: "ls"}}},
		{"an item above grows", []any{engine.TextDelta{At: t0, ItemID: "early", Text: "\n\nwith\nmore\nlines"}}},
	} {
		p.apply(step.evs...)
		after := p.draw()
		assert.Equal(t, without(before, pillRow), without(after, pillRow), step.name)
		assert.Contains(t, after[pillRow], "New activity · ↓ Back to bottom · end", step.name)
	}
	assert.Greater(t, p.s.Scroll, 10, "the lines below the window")

	p.apply(state.ScrollToBottom{})
	bottom := strings.Join(p.draw(), "\n")
	assert.Contains(t, bottom, "another paragraph", "end follows the bottom again")
	assert.NotContains(t, bottom, "New activity")
}

// TestScroll_AnchorAcrossLayouts: a resize and ctrl+t keep the anchor's
// line on the window's bottom row; a rewind that cuts the anchor's item
// falls back to the lines scrolled, and the next frame anchors again.
func TestScroll_AnchorAcrossLayouts(t *testing.T) {
	s := base()
	for i := range 60 {
		if i == 30 {
			s = apply(s, core.UserMessage{At: t0, ID: "m2", Text: "a later message"})
		}
		s = apply(s, session.Notice{At: t0, Level: "info", Message: fmt.Sprintf("line %02d", i)})
	}
	// Below the window: lines that wrap with the width, and a run the
	// detailed view draws more of.
	s = apply(s,
		session.Notice{At: t0, Level: "info", Message: strings.Repeat("wrapping words ", 20)},
		core.RunStarted{At: t0, RunID: "r1"},
		core.TurnStarted{At: t0, Turn: 1},
		core.ModelResponded{At: t0, Turn: 1},
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusOK}},
		session.Idle{At: t0},
	)
	p := pin(t, s, 100, 24)
	p.apply(state.ScrollBy{Lines: 10})
	p.draw()
	anchor, _ := p.c.Anchor()
	text := func() string {
		_, line, ok := p.c.At(0, p.lastRow(), false)
		require.True(t, ok)

		return strings.TrimSpace(line)
	}
	want := text()
	require.Contains(t, want, "line 5")

	p.w = 60
	p.draw()
	assert.Equal(t, want, text(), "narrower")
	p.w, p.h = 120, 30
	p.draw()
	assert.Equal(t, want, text(), "wider and taller")
	p.apply(state.ToggleDetails{})
	p.draw()
	assert.Equal(t, want, text(), "the detailed view")
	at, _ := p.c.Anchor()
	assert.Equal(t, anchor, at)
	assert.False(t, p.s.NewBelow, "the same output in another layout")
	p.apply(state.ToggleDetails{})

	p.apply(engine.Rewound{At: t0, MessageID: "m2"})
	assert.False(t, p.s.Pinned(), "the cut follows the bottom")
	assert.Empty(t, p.s.Anchor)
	assert.Contains(t, strings.Join(p.draw(), "\n"), "line 29", "what is left")
}

// TestScroll_AnchorLeaves: an anchor whose item leaves (a stream reset
// drops a streamed answer) falls back to the lines scrolled, and the next
// frame anchors on what is left.
func TestScroll_AnchorLeaves(t *testing.T) {
	p := pin(t, apply(longRun(), engine.TextDelta{At: t0, ItemID: "late", Text: "a\nb\nc\nd\ne\nf\ng\nh"}), 100, 24)
	p.apply(state.ScrollBy{Lines: 3})
	p.draw()
	require.True(t, strings.HasPrefix(p.s.Anchor.Key, "stream:"), "anchored in the streamed answer: %+v", p.s.Anchor)

	p.apply(engine.StreamReset{At: t0})
	_, ok := p.s.Item(p.s.Anchor.Key)
	require.False(t, ok, "the answer left")
	assert.Contains(t, strings.Join(p.draw(), "\n"), "line 5", "the window is drawn")
	_, ok = p.s.Item(p.s.Anchor.Key)
	assert.True(t, ok, "anchored again: %+v", p.s.Anchor)
}

// TestNotes: the pill and the toast are drawn over the window's rows, so
// the screen has as many rows as without them and no row is wider; with
// both, the toast takes the window's first row.
func TestNotes(t *testing.T) {
	for _, w := range []int{120, 60, 40, 12} {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			p := pin(t, longRun(), w, 20)
			p.apply(state.ScrollBy{Lines: 10})
			plain := p.draw()
			p.apply(engine.TextDelta{At: t0, ItemID: "late", Text: "new"})
			pill := p.draw()
			last := p.lastRow()
			assert.Equal(t, without(plain, last), without(pill, last), "only the last row changes")
			assert.Contains(t, pill[last], "↓", "the pill")

			p.apply(state.Copied{Lines: 3, At: t0})
			both := p.draw()
			first := p.firstRow()
			assert.Equal(t, without(without(pill, last), first), without(without(both, last), first))
			if w >= 20 {
				assert.Contains(t, both[first], "copied 3 lines")
				assert.True(t, strings.HasSuffix(both[first], "copied 3 lines"), "on the right: %q", both[first])
			}

			p.apply(state.ScrollToBottom{})
			toast := p.draw()
			if w >= 20 {
				assert.True(t, strings.HasSuffix(toast[p.lastRow()], "copied 3 lines"), "alone, on the last row: %q", toast[p.lastRow()])
			}
			p.apply(state.Tick{Now: t0.Add(3 * time.Second)})
			assert.NotContains(t, strings.Join(p.draw(), "\n"), "copied", "gone")
		})
	}
}
