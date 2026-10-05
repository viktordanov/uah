package bubble_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
)

// readA runs the simple fixture, whose agent lists the workspace and reads
// a.txt, with file links set to mode, and writes a.txt with text.
func readA(t *testing.T, d bubble.Deps, mode, text string) *driver {
	t.Helper()
	d.Mouse, d.FileLinks = true, mode
	dr := start(t, d)
	dr.typeText("hi")
	dr.key(term.KeyEnter, 0)
	dr.waitFor("• hello")
	dr.waitIdle()
	ws := dr.m.(bubble.Model).Workspace()
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a.txt"), []byte(text), 0o600))

	return dr
}

// readCell is the screen cell of the READ line's path.
func (d *driver) readCell() (x, y int) {
	d.t.Helper()
	for row, line := range strings.Split(d.view(), "\n") {
		if i := strings.Index(line, "READ           a.txt"); i >= 0 {
			return ansi.StringWidth(line[:i]) + len("READ           "), row
		}
	}
	d.t.Fatalf("no READ line:\n%s", d.view())

	return 0, 0
}

// clickAt presses and lets go of the left button at a cell.
func (d *driver) clickAt(x, y int) {
	d.press(x, y)
	d.release(x, y)
}

// TestTUI_ClickPeeksAtAFile: a click on a READ line's path opens the
// overlay with the file; the wheel and keys scroll it, keys go to it and
// not the composer, and esc closes it. A drag over the path selects it
// instead.
func TestTUI_ClickPeeksAtAFile(t *testing.T) {
	var text strings.Builder
	for i := range 120 {
		text.WriteString("line ")
		text.WriteString(strings.Repeat("x", i%7))
		text.WriteString("\n")
	}
	d := readA(t, deps(t, "simple.jsonl"), "peek", "hello from a.txt\n"+text.String())
	view := d.m.View().Content
	assert.Contains(t, view, "\x1b]8;;file://", "the path is a hyperlink")
	x, y := d.readCell()
	d.clickAt(x+1, y)
	d.waitFor("hello from a.txt")
	assert.Contains(t, d.view(), "╭─ a.txt ─", "the overlay's title")
	assert.Contains(t, d.view(), "1-", "where it is")
	assert.Nil(t, d.m.View().Cursor, "no cursor under the overlay")

	d.typeText("j")
	d.key(term.KeyPgDown, 0)
	assert.NotContains(t, d.view(), "hello from a.txt", "scrolled")
	assert.Empty(t, d.m.(bubble.Model).Draft(), "keys go to the overlay")
	d.send(term.MouseWheelMsg{X: 50, Y: 10, Button: term.MouseWheelUp})
	d.typeText("g")
	assert.Contains(t, d.view(), "hello from a.txt", "g goes to the top")
	d.key(term.KeyEscape, 0)
	assert.NotContains(t, d.view(), "hello from a.txt", "esc closes it")
	assert.NotNil(t, d.m.View().Cursor)

	d.clickAt(x, y)
	d.waitFor("hello from a.txt")
	d.clickAt(0, 0)
	assert.NotContains(t, d.view(), "hello from a.txt", "a click outside closes it")

	copies := make(chan string, 1)
	d2 := deps(t, "simple.jsonl")
	d2.CopyText = func(_ context.Context, s string) error { copies <- s; return nil }
	dr := readA(t, d2, "peek", "x\n")
	x, y = dr.readCell()
	dr.press(x, y)
	dr.move(x+4, y)
	dr.release(x+4, y)
	assert.Equal(t, "a.txt", dr.copied(copies, "copied 1 line"), "a drag selects")
	assert.NotContains(t, dr.view(), "╭─ a.txt", "and opens nothing")
}

// TestTUI_ClickOpensInTheEditor: with file_links = "editor", a windowed
// editor starts on its own at the file, and a terminal editor runs with
// the terminal released, as ctrl+g's; with "open", the system's opener
// gets the file. A missing file is a notice.
func TestTUI_ClickOpensInTheEditor(t *testing.T) {
	var mu sync.Mutex
	var launched [][]string
	launch := func(_ context.Context, args []string) error {
		mu.Lock()
		defer mu.Unlock()
		launched = append(launched, args)

		return nil
	}
	last := func() []string {
		mu.Lock()
		defer mu.Unlock()
		if len(launched) == 0 {
			return nil
		}

		return launched[len(launched)-1]
	}

	t.Setenv("VISUAL", "code --wait")
	d := deps(t, "simple.jsonl")
	d.Launch = launch
	dr := readA(t, d, "editor", "a\n")
	ws := dr.m.(bubble.Model).Workspace()
	x, y := dr.readCell()
	dr.clickAt(x, y)
	dr.until("code started", func() bool { return last() != nil })
	dr.waitFor("opened a.txt in code")
	assert.Equal(t, []string{"code", filepath.Join(ws, "a.txt")}, last(), "no --wait, no line for a whole file")

	d = deps(t, "simple.jsonl")
	d.Launch = launch
	dr = readA(t, d, "open", "a\n")
	ws = dr.m.(bubble.Model).Workspace()
	x, y = dr.readCell()
	dr.clickAt(x, y)
	dr.until("the opener", func() bool { got := last(); return got != nil && got[len(got)-1] == filepath.Join(ws, "a.txt") })
	assert.Contains(t, []string{"open", "xdg-open"}, last()[0])

	require.NoError(t, os.Chmod(filepath.Join(ws, "a.txt"), 0o700))
	n := len(launched)
	dr.clickAt(x+2, y)
	dr.until("the refusal", func() bool { return strings.Contains(dr.m.(bubble.Model).ToastText(), "would run") })
	assert.Len(t, launched, n, "an executable file is not handed to the opener")

	require.NoError(t, os.Remove(filepath.Join(ws, "a.txt")))
	dr.clickAt(x+1, y) // another cell: not a double click

	dr.waitFor("can't open a.txt")

	d = deps(t, "simple.jsonl")
	fe := useFakeEditor(t, &d, "keep")
	dr = readA(t, d, "editor", "kept\n")
	ws = dr.m.(bubble.Model).Workspace()
	x, y = dr.readCell()
	dr.clickAt(x, y)
	dr.until("the editor started", func() bool { return fe.run != nil })
	dr.edit(fe)
	got := readSeen(t, fe.record)
	assert.Equal(t, filepath.Join(ws, "a.txt"), got.Path, "the terminal editor ran on the file")
	assert.Equal(t, "kept\n", got.Text)
}

// TestTUI_DoubleClickOnALinkSelects: with peek, the default, a double click
// on a path selects its word and opens nothing.
func TestTUI_DoubleClickOnALinkSelects(t *testing.T) {
	copies := make(chan string, 1)
	d := deps(t, "simple.jsonl")
	d.CopyText = func(_ context.Context, s string) error { copies <- s; return nil }
	dr := readA(t, d, "peek", "x\n")
	x, y := dr.readCell()
	dr.clickAt(x+1, y)
	dr.clickAt(x+1, y)
	assert.Equal(t, "a.txt", dr.copied(copies, "copied 1 line"), "the word")
	dr.pump(700 * time.Millisecond)
	assert.NotContains(t, dr.view(), "╭─ a.txt", "no overlay after the double click's window")
}
