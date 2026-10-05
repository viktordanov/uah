package render_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/codereview"
	"github.com/viktordanov/uah/internal/gitdiff"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// linksOn turns file links on, as the shell does from [tui] file_links.
func linksOn(s state.State) state.State {
	s.FileLinks, s.Host = state.LinksPeek, "host"

	return s
}

// shownLink is a link on screen: the text its cells show, its row, and
// where it leads.
type shownLink struct {
	text string
	row  int
	link state.FileLink
}

// linksShown draws s at w by h and reads every link back with LinkAt, the
// shell's hit test, cell by cell.
func linksShown(t *testing.T, s state.State, w, h int) []shownLink {
	t.Helper()
	c := render.NewCache(render.Amber)
	out, _ := render.Screen(s, c, render.Frame{Width: w, Height: h, Composer: "λ ", ComposerHeight: 1})
	lines := strings.Split(out, "\n")
	var found []shownLink
	for y, line := range lines {
		require.LessOrEqual(t, ansi.StringWidth(line), w, "row %d fits: %q", y, ansi.Strip(line))
		from := -1
		var cur state.FileLink
		for x := 0; x <= w; x++ {
			l, ok := c.LinkAt(x, y)
			if from >= 0 && (!ok || l != cur) {
				found = append(found, shownLink{text: ansi.Strip(ansi.Cut(line, from, x)), row: y, link: cur})
				from = -1
			}
			if ok && from < 0 {
				from, cur = x, l
			}
		}
	}

	return found
}

// linkTo finds the link to path on screen.
func linkTo(found []shownLink, path string) (shownLink, bool) {
	for _, f := range found {
		if f.link.Path == path {
			return f, true
		}
	}

	return shownLink{}, false
}

// shownAs checks that a link shows text, or its start cut with "…".
func shownAs(t *testing.T, got shownLink, text string, w int) {
	t.Helper()
	if got.text == text {
		return
	}
	cut, ok := strings.CutSuffix(got.text, "…")
	assert.True(t, ok && strings.HasPrefix(text, cut) && cut != "", "at %d columns the link reads %q, want %q or its start cut", w, got.text, text)
}

// TestLinks_ToolLines: a READ line's files link to their files and lines,
// also the one shown by its name only, and a line cut at a narrow width
// keeps the link on what shows.
func TestLinks_ToolLines(t *testing.T) {
	s := linksOn(apply(base(), core.RunStarted{At: t0, RunID: "r1"}))
	s = apply(s, bash("c5", "rtk proxy sh -c 'sed -n \"1,360p\" "+ws+"/api/ontology/ontology.go; sed -n \"356,365p\" "+ws+"/api/ontology/loader.go'", true, "exit 0", 0, "").events()...)
	for _, w := range []int{120, 60, 40} {
		found := linksShown(t, s, w, 20)
		got, ok := linkTo(found, ws+"/api/ontology/ontology.go")
		require.True(t, ok, "at %d columns: %v", w, found)
		assert.Equal(t, state.FileLink{Path: ws + "/api/ontology/ontology.go", Line: 1, End: 360}, got.link)
		shownAs(t, got, "api/ontology/ontology.go", w)
		if w >= 60 {
			got, ok = linkTo(found, ws+"/api/ontology/loader.go")
			require.True(t, ok, "at %d columns", w)
			assert.Equal(t, "loader.go", got.text, "shown by its name, linked by its path")
			assert.Equal(t, 356, got.link.Line)
		}
	}
	off := s
	off.FileLinks = state.LinksOff
	assert.Empty(t, linksShown(t, off, 120, 20), "off: plain text")
	out, _ := render.Screen(off, render.NewCache(render.Amber), render.Frame{Width: 120, Height: 20, Composer: "λ ", ComposerHeight: 1})
	assert.NotContains(t, out, "\x1b]8;", "no hyperlinks")
}

// TestLinks_PatchAndDiffHeaders: a patch's file headers and /diff's link
// to each file's first changed line; a deleted file is no link.
func TestLinks_PatchAndDiffHeaders(t *testing.T) {
	s := linksOn(patchedRun(t))
	for _, w := range []int{120, 60, 40} {
		found := linksShown(t, s, w, 30)
		got, ok := linkTo(found, ws+"/pkg/foo/foo.go")
		require.True(t, ok, "at %d columns: %v", w, found)
		assert.Equal(t, "pkg/foo/foo.go", got.text)
		assert.Equal(t, 12, got.link.Line, "the first changed line")
		got, ok = linkTo(found, ws+"/pkg/foo/doc.md")
		require.True(t, ok)
		assert.Equal(t, 1, got.link.Line)
	}

	d := gitdiff.Diff{Root: "/repo", Files: []gitdiff.File{
		{FileDiff: patch.FileDiff{Op: "update", Path: "cmd/main.go", Added: 1, Removed: 1, Hunks: []patch.DiffHunk{{Lines: []patch.DiffLine{
			{Kind: " ", Old: 7, New: 7, Text: "func main() {"},
			{Kind: "-", Old: 8, Text: "x"},
			{Kind: "+", New: 8, Text: "y"},
		}}}}},
		{FileDiff: patch.FileDiff{Op: "delete", Path: "old.go", Removed: 1, Hunks: []patch.DiffHunk{{Lines: []patch.DiffLine{{Kind: "-", Old: 1, Text: "x"}}}}}},
	}}
	found := linksShown(t, linksOn(apply(base(), state.DiffShown{Diff: d})), 60, 20)
	got, ok := linkTo(found, "/repo/cmd/main.go")
	require.True(t, ok, "under the work tree's root: %v", found)
	assert.Equal(t, state.FileLink{Path: "/repo/cmd/main.go", Line: 8}, got.link)
	_, ok = linkTo(found, "/repo/old.go")
	assert.False(t, ok, "a deleted file is no link")
}

// TestLinks_ReviewPlaces: each finding's place links to its lines, cut
// with the line at 40 columns.
func TestLinks_ReviewPlaces(t *testing.T) {
	s := linksOn(reviewed(codereview.Parse(findings)))
	for _, w := range []int{120, 60, 40} {
		found := linksShown(t, s, w, 90)
		got, ok := linkTo(found, ws+"/internal/config/list.go")
		require.True(t, ok, "at %d columns: %v", w, found)
		assert.Equal(t, state.FileLink{Path: ws + "/internal/config/list.go", Line: 12, End: 15}, got.link)
		shownAs(t, got, "internal/config/list.go:12-15", w)
		got, ok = linkTo(found, "/elsewhere/x.go")
		require.True(t, ok)
		assert.Equal(t, state.FileLink{Path: "/elsewhere/x.go", Line: 7}, got.link, "one line, outside the workspace")
	}
}

// TestLinks_MarkdownWords: the words of a message the shell found to name
// files are links, in a code span or plain, with their line; a path the
// wrap splits links on both lines; other words are not links.
func TestLinks_MarkdownWords(t *testing.T) {
	long := "internal/tui/render/markdown/testdata/a-rather-long-file-name.golden"
	text := "See `internal/tui/state/peek.go:12` and README.md, not docs/missing.md.\n\nThe golden " + long + " too.\n\n```\nREADME.md in a fence\n```"
	s := linksOn(apply(base(), core.RunStarted{At: t0, RunID: "r1"}))
	s, effects := state.Reduce(s, core.AssistantMessage{At: t0, Text: text, Final: true})
	var ask state.EffResolveLinks
	for _, e := range effects {
		if r, ok := e.(state.EffResolveLinks); ok {
			ask = r
		}
	}
	require.Len(t, ask.Messages, 1, "the finished message is looked up")
	assert.Equal(t, []string{"internal/tui/state/peek.go:12", "README.md", "docs/missing.md", long}, ask.Messages[0].Words, "outside the fence, once each")
	m := ask.Messages[0]
	s = apply(s, state.LinksResolved{Messages: []state.ResolvedLinks{{Key: m.Key, Text: m.Text, Links: map[string]state.FileLink{
		"internal/tui/state/peek.go:12": {Path: ws + "/internal/tui/state/peek.go", Line: 12},
		"README.md":                     {Path: ws + "/README.md"},
		long:                            {Path: ws + "/" + long},
	}}}})
	for _, w := range []int{120, 60, 40} {
		found := linksShown(t, s, w, 30)
		got, ok := linkTo(found, ws+"/internal/tui/state/peek.go")
		require.True(t, ok, "at %d columns: %v", w, found)
		assert.Equal(t, "internal/tui/state/peek.go:12", got.text)
		assert.Equal(t, 12, got.link.Line)
		got, ok = linkTo(found, ws+"/README.md")
		require.True(t, ok)
		assert.Equal(t, "README.md", got.text, "without the comma after it")
		var parts []string
		for _, f := range found {
			if f.link.Path == ws+"/"+long {
				parts = append(parts, f.text)
			}
		}
		assert.Equal(t, long, strings.Join(parts, ""), "at %d columns the long path links whole, over %d lines", w, len(parts))
		if w == 40 {
			assert.Len(t, parts, 2, "the wrap splits it")
		}
		for _, f := range found {
			assert.NotContains(t, f.text, "missing", "a word that names no file")
		}
	}
}

// TestLinks_URL: a link's URL is file://host/path, escaped, with its lines
// in the parameters, and a click reads it back.
func TestLinks_URL(t *testing.T) {
	s := linksOn(reviewed(codereview.Parse(strings.ReplaceAll(findings, "/workspace/proj/internal/config/doc.go", "/workspace/proj/a dir;x/doc.go"))))
	out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: 120, Height: 90, Composer: "λ ", ComposerHeight: 1})
	assert.Contains(t, out, "\x1b]8;line=41-44;file://host/workspace/proj/internal/config/load.go\x1b\\")
	assert.Contains(t, out, "\x1b]8;line=1;file://host/workspace/proj/a%20dir%3Bx/doc.go\x1b\\")
	found := linksShown(t, s, 120, 90)
	_, ok := linkTo(found, "/workspace/proj/a dir;x/doc.go")
	assert.True(t, ok, "read back unescaped")
}
