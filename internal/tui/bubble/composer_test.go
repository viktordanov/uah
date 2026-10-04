package bubble_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
)

// cursorLine is the screen line the terminal's cursor is on.
func (d *driver) cursorLine() string {
	d.t.Helper()
	v := d.m.View()
	require.NotNil(d.t, v.Cursor, "the composer shows the cursor")
	lines := strings.Split(d.view(), "\n")
	require.Less(d.t, v.Cursor.Y, len(lines))

	return lines[v.Cursor.Y]
}

// rowOf is the screen row of the first line holding text, or -1.
func (d *driver) rowOf(text string) int {
	return slices.IndexFunc(strings.Split(d.view(), "\n"), func(l string) bool { return strings.Contains(l, text) })
}

// TestTUI_NewLinesAfterALongPaste (GitHub #2): after a paste taller than
// the composer, shift+enter and ctrl+j still add lines, nothing of the
// paste is lost, the composer stays 15 rows high (half the window) with
// the cursor in view, and ↑ and ↓ scroll the transcript only at the
// draft's first and last lines, not at the composer's visible edges.
func TestTUI_NewLinesAfterALongPaste(t *testing.T) {
	d, llm, _ := imageDeps(t)
	answer := make([]string, 40)
	for i := range answer {
		answer[i] = fmt.Sprintf("- row %02d", i+1)
	}
	llm.Route("tall", fakellm.Reply{Text: strings.Join(answer, "\n")})
	dr := start(t, d)
	dr.until("the session is open", func() bool { return dr.m.(bubble.Model).Exit().SessionID != "" })
	dr.typeText("tall")
	dr.key(term.KeyEnter, 0)
	dr.waitFor("row 40")
	dr.waitIdle()

	pasted := make([]string, 50)
	for i := range pasted {
		pasted[i] = fmt.Sprintf("pasted %02d", i+1)
		if i < 5 { // lines wider than the window wrap
			pasted[i] += " " + strings.Repeat("x", 150)
		}
	}
	dr.send(term.PasteMsg{Content: strings.Join(pasted, "\n")})
	assert.Contains(t, dr.cursorLine(), "pasted 50")

	dr.key(term.KeyEnter, term.ModShift)
	dr.typeText("after shift")
	assert.Contains(t, dr.cursorLine(), "after shift")
	dr.key('j', term.ModCtrl)
	dr.typeText("after ctrl")
	assert.Contains(t, dr.cursorLine(), "after ctrl")

	screen := dr.view()
	assert.Len(t, strings.Split(screen, "\n"), 30, "the screen keeps its height")
	assert.Equal(t, 13, strings.Count(screen, "pasted "), "15 rows show: 13 pasted lines and the 2 typed")
	assert.NotContains(t, screen, "λ", "the λ scrolled away with the draft's first line")

	answerRow := dr.rowOf("row 40")
	require.GreaterOrEqual(t, answerRow, 0)
	for range 22 { // up to "pasted 30", past the composer's top row
		dr.key(term.KeyUp, 0)
	}
	assert.Contains(t, dr.cursorLine(), "pasted 30")
	assert.Equal(t, answerRow, dr.rowOf("row 40"), "↑ inside the draft moves the cursor only")

	dr.key(term.KeyHome, term.ModCtrl)
	assert.Contains(t, dr.cursorLine(), "λ pasted 01")
	dr.key(term.KeyUp, 0)
	assert.Equal(t, answerRow+1, dr.rowOf("row 40"), "↑ on the draft's first line scrolls the transcript")

	dr.key(term.KeyEnd, term.ModCtrl)
	assert.Contains(t, dr.cursorLine(), "after ctrl")
	dr.key(term.KeyDown, 0)
	assert.Equal(t, answerRow, dr.rowOf("row 40"), "↓ on the draft's last line scrolls back")

	dr.key(term.KeyEnter, 0)
	dr.waitFor("• done") // the route matched the history, and its one reply is spent
	reqs := llm.Requests()
	require.Len(t, reqs, 2)
	want := strings.Join(append(pasted, "after shift", "after ctrl"), "\n")
	assert.Contains(t, reqs[1].UserTexts, want, "the model gets all 52 lines")
}
