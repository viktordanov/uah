package render_test

import (
	"fmt"
	"runtime"
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// selectRun is a finished run with a message, a tool call, and an answer
// with a code block; keys are its message's and answer's items.
func selectRun(t *testing.T) (s state.State, msg, answer string) {
	t.Helper()
	s = apply(base(),
		core.RunStarted{At: t0, RunID: "r1"},
		core.UserMessage{At: t0, ID: "m1", Text: "show me 界面 main   "},
		core.ToolCalled{At: t0, CallID: "c1", Name: "Bash", Label: "ls"},
		core.ToolFinished{At: t0, CallID: "c1", Name: "Bash", OK: true, Detail: "exit 0"},
		core.AssistantMessage{At: t0, Text: "Here it is:\n\n```go\nfunc main() {}\n```\n\n```diff\n+ added\n```\n\nDone.", Final: true},
		core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: "r1"}, Status: core.StatusOK}},
		session.Idle{At: t0},
	)
	for _, it := range s.Items {
		switch it.Kind {
		case state.KindUser:
			msg = it.Key
		case state.KindAssistant:
			answer = it.Key
		default:
		}
	}
	require.NotEmpty(t, msg)
	require.NotEmpty(t, answer)

	return s, msg, answer
}

func pos(key string, line, col int) state.TextPos {
	return state.TextPos{Key: key, Line: line, Col: col}
}

// selecting drags from one position to another and lets go.
func selecting(s state.State, from, to state.TextPos) state.State {
	return apply(s, state.MousePress{At: from, When: t0}, state.MouseDrag{At: to}, state.MouseRelease{})
}

// TestSelectionOverlay: the selection is drawn on the theme's selection
// background over the window's lines, in both themes, and clearing it
// draws the lines as before from the same cache.
func TestSelectionOverlay(t *testing.T) {
	s, msg, answer := selectRun(t)
	for _, tc := range []struct {
		name  string
		theme render.Theme
	}{{"selection", render.Amber}, {"selection-light", render.AmberLight}} {
		t.Run(tc.name, func(t *testing.T) {
			c := render.NewCache(tc.theme)
			before := rawScreen(s, c)
			selected := selecting(s, pos(msg, 2, 8), pos(answer, 3, 6))
			golden(t, tc.name, selectionMarks(rawScreen(selected, c), tc.theme.Selection))
			assert.Equal(t, before, rawScreen(apply(selected, state.ClearSelection{}), c), "clearing draws the lines as before")
		})
	}
}

// selectionMarks is a screen's text with the cells on the selection
// background between [ and ].
func selectionMarks(out string, sel color.Color) string {
	r, g, b, _ := sel.RGBA()
	on := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		var b strings.Builder
		inside := false
		for _, part := range sgrSeq.Split(strings.ReplaceAll(l, on, "\x1b[m\x00"), -1) {
			if strings.HasPrefix(part, "\x00") {
				b.WriteString("[")
				inside, part = true, part[1:]
			} else if inside {
				b.WriteString("]")
				inside = false
			}
			b.WriteString(part)
		}
		lines[i] = strings.TrimRight(b.String(), " ")
	}

	return strings.Join(lines, "\n") + "\n"
}

// TestSelectedText: copying takes the text as drawn, without the λ and •
// columns, the band's padding before code and the fence's language, or
// trailing spaces, with wide characters cut by their cells.
func TestSelectedText(t *testing.T) {
	s, msg, answer := selectRun(t)
	c := render.NewCache(render.Amber)
	f := render.Frame{Width: 100, Height: 24}
	copied := func(from, to state.TextPos) (string, int) { return render.SelectedText(selecting(s, from, to), c, f) }

	text, lines := copied(pos(msg, 0, 0), pos(msg, 3, 99))
	assert.Equal(t, "show me 界面 main", text, "the whole band: the message, without its λ, band rows, or trailing spaces")
	assert.Equal(t, 1, lines)

	text, _ = copied(pos(msg, 2, 11), pos(msg, 2, 15))
	assert.Equal(t, "界面 m", text, "from the second cell of 界, which counts whole, to a cell")

	text, lines = copied(pos(answer, 3, 0), pos(answer, 3, 99))
	assert.Equal(t, "func main() {}", text, "a code line without the indent and the band's padding")
	assert.Equal(t, 1, lines)

	text, _ = copied(pos(answer, 5, 0), pos(answer, 5, 99))
	assert.Equal(t, "+ added", text, "a diff line on its tint, without its label")

	text, lines = copied(pos(msg, 2, 0), pos(answer, 99, 99))
	assert.Equal(t, "show me 界面 main\n\nLIST           .\n\nHere it is:\n\nfunc main() {}\n\n+ added\n\nDone.", text)
	assert.Equal(t, 11, lines)

	text, _ = render.SelectedText(s, c, f)
	assert.Empty(t, text, "nothing selected")
}

// TestAt: the last frame maps a screen cell to the transcript line it
// shows, with the line's text; the rows outside the transcript map to
// nothing, and Edge tells above from below and how far.
func TestAt(t *testing.T) {
	s, msg, _ := selectRun(t)
	c := render.NewCache(render.Amber)
	out := strings.Split(ansi.Strip(rawScreen(s, c)), "\n")
	row := -1
	for i, l := range out {
		if strings.Contains(l, "show me") {
			row = i
		}
	}
	require.GreaterOrEqual(t, row, 0)

	got, text, ok := c.At(5, row, false)
	require.True(t, ok)
	assert.Equal(t, pos(msg, 2, 5), got)
	assert.Contains(t, text, "λ show me 界面 main")
	assert.Equal(t, 0, c.Edge(row))

	_, _, ok = c.At(0, len(out)-1, false)
	assert.False(t, ok, "the footer")
	assert.Greater(t, c.Edge(len(out)-1), 1, "a row more for each row past the transcript")
	assert.Equal(t, c.Edge(len(out)-1)-1, c.Edge(len(out)-2))
	got, _, ok = c.At(3, len(out)-1, true)
	require.True(t, ok, "clamped to the transcript's last line")
	assert.Equal(t, 3, got.Col)
	assert.Equal(t, -1, c.Edge(0), "the first row")
	got, _, ok = c.At(0, -1, true)
	require.True(t, ok)
	assert.Equal(t, state.BannerKey, got.Key, "clamped to the window's first line, the banner's")
}

// TestSelectedTextMarkdown: a copied answer keeps its words and its
// structure: a quote without its “ ” marks, an alert's title and its
// text, a table's columns without the zebra's padding, list markers and
// nesting, and code dedented without its language.
func TestSelectedTextMarkdown(t *testing.T) {
	s := apply(base(), core.AssistantMessage{At: t0, Final: true, Text: "> a quote long enough to wrap onto a second line of the quote\n\n" +
		"> [!WARNING]\n> be careful\n\n| Name | Val |\n|---|---|\n| one | 1 |\n| two | 2 |\n\n" +
		"1. first\n   - nested\n\n```go\nif ok {\n\treturn\n}\n```"})
	answer := s.Items[len(s.Items)-1].Key
	text, _ := render.SelectedText(selecting(s, pos(answer, 0, 0), pos(answer, 99, 99)), render.NewCache(render.Amber), render.Frame{Width: 44, Height: 24})
	assert.Equal(t, "a quote long enough to wrap onto a\nsecond line of the quote\n\n! Warning\n  be careful\n\n"+
		"Name    Val\none     1\ntwo     2\n\n1. first\n   ◦ nested\n\nif ok {\n    return\n}", text)
}

// TestSelectedTextWrappedCode: a code line longer than the width wraps,
// each piece after the first marked ↪ in the padding column, and a copy
// gives back the lines as written: the pieces joined without a break, the
// spaces at a break kept, the mark left out.
func TestSelectedTextWrappedCode(t *testing.T) {
	long := `query := "SELECT id,  name FROM users WHERE created_at > now() - interval '30 days' ORDER BY id"`
	code := long + "\nshort()\n" + strings.Repeat("x", 30) + "  " + strings.Repeat("y", 70) + "\na" + strings.Repeat(" ", 60) + "b"
	s := apply(base(), core.AssistantMessage{At: t0, Final: true, Text: "```go\n" + code + "\n```"})
	answer := s.Items[len(s.Items)-1].Key
	c, f := render.NewCache(render.Amber), render.Frame{Width: 40, Height: 40}

	text, lines := render.SelectedText(selecting(s, pos(answer, 0, 0), pos(answer, 99, 99)), c, f)
	assert.Equal(t, code, text)
	assert.Equal(t, 4, lines, "one line per line of code, not per row drawn")

	screen, _ := render.Screen(s, c, f)
	assert.Contains(t, ansi.Strip(screen), "↪", "a continuation shows the mark")
	assert.NotContains(t, text, "↪")

	text, _ = render.SelectedText(selecting(s, pos(answer, 1, 0), pos(answer, 1, 99)), c, f)
	assert.NotContains(t, text, "\n", "a selection that starts on a continuation copies that piece")
	assert.True(t, strings.HasPrefix(long, "query") && strings.Contains(long, text), "%q is a piece of the line", text)
}

// TestSelectedTextWrappedCodeEdges: a wide character that does not fit at
// a row's end leaves a padded cell that is not copied; a continuation of
// only spaces is part of its line, not a blank line between blocks; and a
// wrapped line loses the indent the block's lines share, as they do.
func TestSelectedTextWrappedCodeEdges(t *testing.T) {
	f := render.Frame{Width: 40, Height: 40}
	for name, code := range map[string]string{
		"a wide character at the edge": strings.Repeat("a", 36) + "界b",
		"a row of spaces":              "a" + strings.Repeat(" ", 90) + "b",
		"an indented block":            "    short()\n    " + strings.Repeat("long ", 20) + "end\n    done()",
	} {
		s := apply(base(), core.AssistantMessage{At: t0, Final: true, Text: "```\n" + code + "\n```"})
		answer := s.Items[len(s.Items)-1].Key
		text, _ := render.SelectedText(selecting(s, pos(answer, 0, 0), pos(answer, 99, 99)), render.NewCache(render.Amber), f)
		want := code
		if name == "an indented block" {
			want = strings.ReplaceAll(strings.TrimPrefix(code, "    "), "\n    ", "\n") // the block's shared indent goes
		}
		assert.Equal(t, want, text, name)
	}
}

// A long wrapped code line copies in memory linear in its length: twice
// the line allocates about twice the bytes, where joining each piece onto
// the line so far would allocate four times as many.
func TestSelectedTextLongWrappedCode(t *testing.T) {
	copyBytes := func(reps int) uint64 {
		code := strings.Repeat(`{"key":"value","n":12345},`, reps)
		s := apply(base(), core.AssistantMessage{At: t0, Final: true, Text: "```json\n" + code + "\n```"})
		answer := s.Items[len(s.Items)-1].Key
		sel := selecting(s, pos(answer, 0, 0), pos(answer, 99_999, 99))
		c, f := render.NewCache(render.Amber), render.Frame{Width: 40, Height: 40}
		render.SelectedText(sel, c, f) // draws the lines into the cache
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		text, lines := render.SelectedText(sel, c, f)
		runtime.ReadMemStats(&after)
		require.Equal(t, code, text)
		require.Equal(t, 1, lines)

		return after.TotalAlloc - before.TotalAlloc
	}
	small, large := copyBytes(2_000), copyBytes(4_000) // 52,000 and 104,000 characters
	assert.Less(t, float64(large)/float64(small), 3.0, "%d bytes, then %d: the bytes grow with the line, not its square", small, large)
}
