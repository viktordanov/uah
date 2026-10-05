package bubble_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/term"
)

// selectionOn is the Amber theme's selection background, as drawn.
const selectionOn = "\x1b[48;2;92;70;8m"

// copyDeps report the mouse and copy into copies instead of a clipboard.
func copyDeps(t *testing.T) (*driver, chan string) {
	t.Helper()
	d := deps(t, "simple.jsonl")
	d.Mouse = true
	copies := make(chan string, 8)
	d.CopyText = func(_ context.Context, text string) error { copies <- text; return nil }

	return start(t, d), copies
}

// at is the screen cell where text starts: its column and row.
func (d *driver) at(text string) (x, y int) {
	d.t.Helper()
	for row, line := range strings.Split(d.view(), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), row
		}
	}
	d.t.Fatalf("%q is not on the screen:\n%s", text, d.view())

	return 0, 0
}

func (d *driver) press(x, y int) { d.send(term.MouseClickMsg{X: x, Y: y, Button: term.MouseLeft}) }
func (d *driver) move(x, y int)  { d.send(term.MouseMotionMsg{X: x, Y: y, Button: term.MouseLeft}) }
func (d *driver) release(x, y int) {
	d.send(term.MouseReleaseMsg{X: x, Y: y, Button: term.MouseLeft})
}

// copied waits for the next copy and its notice in the footer.
func (d *driver) copied(copies chan string, notice string) string {
	d.t.Helper()
	var text string
	d.until("a copy", func() bool {
		select {
		case text = <-copies:
			return true
		default:
			return false
		}
	})
	d.waitFor(notice)

	return text
}

// TestTUI_SelectAndCopy: a drag over the transcript selects its text and
// copies it on release, a double click copies a word, esc and a click on
// the composer clear the selection, and the mouse is reported.
func TestTUI_SelectAndCopy(t *testing.T) {
	d, copies := copyDeps(t)
	assert.True(t, d.m.View().Mouse)
	d.typeText("hi there")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.waitIdle()

	x, y := d.at("hi there")
	d.press(x, y)
	d.move(x+3, y)
	assert.Contains(t, d.m.View().Content, selectionOn, "the selection shows while dragging")
	d.move(x+7, y)
	d.release(x+7, y)
	assert.Equal(t, "hi there", d.copied(copies, "copied 1 line"))

	d.key(term.KeyEscape, 0)
	assert.NotContains(t, d.m.View().Content, selectionOn, "esc clears it")

	x, y = d.at("hello")
	for range 2 {
		d.press(x+2, y)
		d.release(x+2, y)
	}
	assert.Equal(t, "hello", d.copied(copies, "copied 1 line"))
	assert.Contains(t, d.m.View().Content, selectionOn)

	_, y = d.at("Ask uah to do anything")
	d.press(4, y)
	d.release(4, y)
	assert.NotContains(t, d.m.View().Content, selectionOn, "a click on the composer clears it")
	assert.Empty(t, copies)
}

// TestTUI_SelectWhileScrolling: dragging to the transcript's top row
// scrolls it and the selection grows with it, and so does the wheel during
// a drag; the copy has every line selected.
func TestTUI_SelectWhileScrolling(t *testing.T) {
	d, copies := copyDeps(t)
	d.send(term.WindowSizeMsg{Width: 100, Height: 60})
	for i, text := range []string{"first message", "second message"} {
		d.typeText(text)
		d.key(term.KeyEnter, 0)
		d.until("the answer", func() bool { return strings.Count(d.view(), "• hello") == i+1 })
		d.waitIdle()
	}
	d.send(term.WindowSizeMsg{Width: 100, Height: 14})
	require.NotContains(t, d.view(), "first message", "the window is too short for both")

	x, y := d.at("second message")
	d.press(x+6, y)
	for range 16 {
		d.move(0, 0) // the top row: each move scrolls a line
	}
	assert.Contains(t, d.view(), "scrolled up")
	assert.Contains(t, d.view(), "first message")
	d.release(0, 0)
	text := d.copied(copies, "copied ")
	assert.True(t, strings.HasSuffix(text, "second"), text)
	assert.Contains(t, text, "hello\n")

	d.key(term.KeyEnd, 0)
	x, y = d.at("second message")
	d.press(x+13, y)
	d.move(x+5, y)
	d.send(term.MouseWheelMsg{X: x + 5, Y: y, Button: term.MouseWheelUp})
	d.release(x+5, y)
	text = d.copied(copies, "copied ")
	assert.True(t, strings.HasSuffix(text, "second message"), "the wheel moved the text under the mouse: %q", text)
	assert.Greater(t, strings.Count(text, "\n"), 0, text)
}

// TestTUI_CopyToast: the copy's toast is drawn over the transcript's last
// row and goes after two seconds: no other row moves, and the footer and
// the status line stay as they were.
func TestTUI_CopyToast(t *testing.T) {
	var elapsed atomic.Int64 // the clock, which commands read in their goroutines
	start0 := time.Now()
	dp := deps(t, "simple.jsonl")
	dp.Mouse, dp.Now = true, func() time.Time { return start0.Add(time.Duration(elapsed.Load())) }
	copies := make(chan string, 8)
	dp.CopyText = func(_ context.Context, text string) error { copies <- text; return nil }
	d := start(t, dp)
	d.typeText("hi there")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.waitIdle()
	before := strings.Split(d.view(), "\n")

	x, y := d.at("hi there")
	d.press(x, y)
	d.move(x+7, y)
	d.release(x+7, y)
	d.copied(copies, "copied 1 line")
	after := strings.Split(d.view(), "\n")
	require.Len(t, after, len(before))
	changed := 0
	for i := range before {
		if before[i] != after[i] {
			changed++
			assert.True(t, strings.HasSuffix(strings.TrimRight(after[i], " "), "copied 1 line"), "the toast's row: %q", after[i])
		}
	}
	assert.Equal(t, 1, changed, "only the toast's row")

	elapsed.Store(int64(2 * time.Second))
	d.until("the toast goes", func() bool { return !strings.Contains(d.view(), "copied") })
	assert.Equal(t, before, strings.Split(d.view(), "\n"))
}

// TestTUI_ScrolledUpStaysPut: scrolled up, output that arrives below (a
// command's) moves nothing on the screen but the pill over the
// transcript's last row, and a click on the pill returns to the bottom.
func TestTUI_ScrolledUpStaysPut(t *testing.T) {
	d, _ := copyDeps(t)
	d.typeText("hi")
	d.key(term.KeyEnter, 0)
	d.waitFor("• hello")
	d.waitIdle()
	for range 3 {
		d.typeText("/help")
		d.key(term.KeyEnter, 0)
	}
	for range 4 {
		d.send(term.MouseWheelMsg{Button: term.MouseWheelUp})
	}
	before := strings.Split(d.view(), "\n")
	require.Contains(t, d.view(), "scrolled up")

	d.typeText("/help")
	d.key(term.KeyEnter, 0)
	after := strings.Split(d.view(), "\n")
	require.Len(t, after, len(before))
	pill := -1
	for i := range before {
		if before[i] != after[i] {
			assert.Contains(t, after[i], "New activity · ↓ Back to bottom · end", "only the pill's row changes")
			pill = i
		}
	}
	require.GreaterOrEqual(t, pill, 0, "the pill shows")

	x, _ := d.at("New activity")
	d.press(x+2, pill)
	assert.NotContains(t, d.view(), "scrolled up", "the click returned to the bottom")
	assert.NotContains(t, d.view(), "New activity")
}
