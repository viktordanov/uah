package render_test

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestBacktrackShowsTheSelectedMessage: going back marks the selected
// message and scrolls the transcript to it.
func TestBacktrackShowsTheSelectedMessage(t *testing.T) {
	s := backtrackRuns(t)
	s = apply(s, state.Esc{Empty: true}, state.Esc{Empty: true})
	golden(t, "backtrack-latest", screen(s, ""))
	older := screen(apply(s, state.BacktrackMove{Delta: -6}), "")
	golden(t, "backtrack-older", older)
	assert.Contains(t, older, "▶ message 2")
	assert.NotContains(t, older, "message 8", "the transcript scrolls to the selected message")
}

// TestBacktrackFadesTheRest: everything in the transcript but the selected
// message is drawn in the theme's dim color, in both themes, and cancelling
// draws the transcript as before from the same cache.
func TestBacktrackFadesTheRest(t *testing.T) {
	s := backtrackRuns(t)
	for _, tc := range []struct {
		name  string
		theme render.Theme
	}{{"backtrack-faded", render.Amber}, {"backtrack-faded-light", render.AmberLight}} {
		t.Run(tc.name, func(t *testing.T) {
			c := render.NewCache(tc.theme)
			draw := func(s state.State) string { return rawScreen(s, c) }
			before := draw(s)
			selecting := apply(s, state.Esc{Empty: true}, state.Esc{Empty: true}, state.BacktrackMove{Delta: -6})
			faded := draw(selecting)
			golden(t, tc.name, fadeMarks(faded, tc.theme.Dim))
			assert.Equal(t, before, draw(apply(selecting, state.BacktrackCancel{})), "cancelling draws the lines as before")
		})
	}
}

func rawScreen(s state.State, c *render.Cache) string {
	out, _ := render.Screen(s, c, render.Frame{Width: 100, Height: 24, Composer: "λ ", ComposerHeight: 1})

	return out
}

// fadeMarks is a screen's text with a mark before each line that has text:
// "░" when all of it is in the dim color and not bold, "█" when not.
func fadeMarks(out string, dim color.Color) string {
	r, g, b, _ := dim.RGBA()
	dimFg := fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		text := strings.TrimRight(ansi.Strip(l), " ")
		switch {
		case strings.TrimSpace(text) == "":
			lines[i] = ""
		case allDim(l, dimFg):
			lines[i] = "░ " + text
		default:
			lines[i] = "█ " + text
		}
	}

	return strings.Join(lines, "\n") + "\n"
}

var sgrSeq = regexp.MustCompile("\x1b\\[([0-9;]*)m")

// allDim reports whether every visible character of a line is drawn in the
// dim foreground and not bold.
func allDim(line, dimFg string) bool {
	fg, bold, at := "", false, 0
	visible := func(text string) bool { return strings.TrimSpace(ansi.Strip(text)) != "" }
	for _, loc := range sgrSeq.FindAllStringSubmatchIndex(line, -1) {
		if visible(line[at:loc[0]]) && (fg != dimFg || bold) {
			return false
		}
		params := strings.Split(line[loc[2]:loc[3]], ";")
		for i := 0; i < len(params); i++ {
			switch p := params[i]; {
			case p == "" || p == "0":
				fg, bold = "", false
			case p == "1":
				bold = true
			case p == "22":
				bold = false
			case p == "39":
				fg = ""
			case p == "38" && i+4 < len(params) && params[i+1] == "2":
				fg, i = strings.Join(params[i:i+5], ";"), i+4
			case p == "38" || p == "48":
				i += map[bool]int{true: 2, false: 4}[i+1 < len(params) && params[i+1] == "5"]
			case len(p) == 2 && (p[0] == '3' || p[0] == '9'):
				fg = p
			}
		}
		at = loc[1]
	}

	return !visible(line[at:]) || (fg == dimFg && !bold)
}

func backtrackRuns(t *testing.T) state.State {
	t.Helper()
	s := base()
	for i := 1; i <= 8; i++ {
		run := fmt.Sprintf("run-%d", i)
		s = apply(s,
			core.RunStarted{At: t0, RunID: run},
			core.UserMessage{At: t0, ID: fmt.Sprintf("m%d", i), Text: fmt.Sprintf("message %d", i)},
			core.AssistantMessage{At: t0, Text: fmt.Sprintf("answer %d", i), Final: true},
			core.RunFinished{At: t0, Result: core.Result{Request: core.Request{RunID: run}, Status: core.StatusOK}},
			session.Idle{At: t0},
		)
	}

	return s
}

// TestFindMarksTheMatches: while searching, the window holds the shown
// match, every line is dim, and the query is drawn on the accent in the
// shown item and on the dimmer tint in the others.
func TestFindMarksTheMatches(t *testing.T) {
	s := apply(backtrackRuns(t), state.FindOpen{Query: "2"})
	var docs []state.SearchDoc
	for _, it := range s.Items {
		if it.Kind == state.KindUser || it.Kind == state.KindAssistant {
			docs = append(docs, state.SearchDoc{Key: it.Key, Text: it.Text})
		}
	}
	s = apply(s, state.Found{Gen: 1, Keys: state.FindIn(docs, "2", nil)})
	s = apply(s, state.FindMove{Delta: -1})
	assert.Equal(t, "message 2", itemText(s, s.Find.Key()), "the earlier match")

	c := render.NewCache(render.Amber)
	raw := rawScreen(s, c)
	golden(t, "find-marks", fadeMarks(raw, render.Amber.Dim))
	assert.Contains(t, raw, "\x1b[48;2;255;196;0;38;2;42;42;42m2\x1b[m", "the shown match on the accent")
	assert.Contains(t, raw, "\x1b[48;2;92;70;8m2\x1b[m", "the other match on the tint")
	assert.NotContains(t, ansi.Strip(raw), "message 8", "the window holds the match")
}

func itemText(s state.State, key string) string {
	it, _ := s.Item(key)

	return it.Text
}
