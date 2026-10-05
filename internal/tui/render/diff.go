package render

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/tui/state"
)

// Diffs are drawn as Codex draws an applied patch, "Edited path (+3 -1)"
// and the changed lines with their numbers in a gutter, with Claude Code's
// tint across each whole added or removed line and its marks on the words
// that changed.

// compactDiffLines is how many diff lines the compact view shows per call
// before folding the rest behind ctrl+t.
const compactDiffLines = 12

// diffIndent is where a diff's lines start, under the tool line.
const diffIndent = "    "

// diffStyle is how one kind of diff line is drawn: its tint, the tint of
// its changed words, its sign, and its gutter.
type diffStyle struct {
	line, word, sign, gutter lipgloss.Style
}

func newDiffStyles(t Theme) map[string]diffStyle {
	tinted := func(bg, word, sign lipgloss.Style) diffStyle {
		return diffStyle{line: bg, word: word, sign: sign.Inherit(bg), gutter: lipgloss.NewStyle().Foreground(t.Dim).Inherit(bg)}
	}
	add, del := lipgloss.NewStyle().Background(t.DiffAdd), lipgloss.NewStyle().Background(t.DiffDel)

	return map[string]diffStyle{
		"+": tinted(add, lipgloss.NewStyle().Background(t.DiffAddWord), lipgloss.NewStyle().Foreground(t.Good)),
		"-": tinted(del, lipgloss.NewStyle().Background(t.DiffDelWord), lipgloss.NewStyle().Foreground(t.Bad)),
		" ": {line: lipgloss.NewStyle().Foreground(t.Dim), word: lipgloss.NewStyle().Foreground(t.Dim), sign: lipgloss.NewStyle(), gutter: lipgloss.NewStyle().Foreground(t.Dim)},
	}
}

// patchLines draws an apply_patch call with its diff: the tool line with
// Codex's summary, then the diff, folded after limit lines (0: unfolded).
func (st *Styles) patchLines(it state.Item, w int, now time.Time, limit int) []string {
	head := it
	head.Label = ""
	out := []string{ansi.Truncate(st.compactTool(head, w, now)+st.diffSummary(it.Diff), w, "…")}

	return append(out, st.diffBlock(it.Diff, w, limit)...)
}

// diffSummary is Codex's header: "Edited a.go (+3 -1)", "Added b.go
// (+2 -0)", or "Edited 2 files (+5 -1)".
func (st *Styles) diffSummary(files []patch.FileDiff) string {
	added, removed := 0, 0
	for _, f := range files {
		added, removed = added+f.Added, removed+f.Removed
	}
	if len(files) != 1 {
		return st.dim.Render(fmt.Sprintf("Edited %d files ", len(files))) + st.counts(added, removed)
	}
	verb := cmp.Or(map[string]string{"add": "Added", "delete": "Deleted"}[files[0].Op], "Edited")

	return st.dim.Render(verb+" ") + st.diffPath(files[0], "") + " " + st.counts(added, removed)
}

// opDelete is a deleted file's FileDiff.Op.
const opDelete = "delete"

// diffPath is a changed file's path, "old → new" for a move, as a link to
// its first changed line; root is the directory a relative path is in (""
// for the workspace).
func (st *Styles) diffPath(f patch.FileDiff, root string) string {
	target := f.Path
	if f.MovePath != "" {
		target = f.MovePath
	}
	if root != "" && !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	link := st.fileLink(target, firstChange(f), 0)
	if f.Op == opDelete {
		link = state.FileLink{} // nothing to open
	}
	if f.MovePath != "" {
		return f.Path + " → " + st.linked(f.MovePath, lipgloss.NewStyle(), link)
	}

	return st.linked(f.Path, lipgloss.NewStyle(), link)
}

// firstChange is the first line a file's diff adds or removes, in the new
// file (0: none).
func firstChange(f patch.FileDiff) int {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind != " " {
				return max(l.New, l.Old)
			}
		}
	}

	return 0
}

// counts is "(+3 -1)" with the numbers in the diff's colors.
func (st *Styles) counts(added, removed int) string {
	return st.dim.Render("(") + st.ok.Render(fmt.Sprintf("+%d", added)) + " " + st.bad.Render(fmt.Sprintf("-%d", removed)) + st.dim.Render(")")
}

// diffBlock draws the files' changed lines, each file under its own header
// when there are several. With limit > 0 it stops after that many lines
// and says how many more ctrl+t shows.
func (st *Styles) diffBlock(files []patch.FileDiff, w, limit int) []string {
	return st.diffBlockIn("", files, w, limit)
}

// diffBlockIn is diffBlock for files whose relative paths are in root (""
// for the workspace).
func (st *Styles) diffBlockIn(root string, files []patch.FileDiff, w, limit int) []string {
	gw := gutterWidth(files)
	var out []string
	shown, total := 0, 0
	for _, f := range files {
		total += f.Omitted
		if len(files) > 1 && (limit == 0 || shown < limit) {
			out = append(out, ansi.Truncate(st.dim.Render("  └ ")+st.diffPath(f, root)+" "+st.counts(f.Added, f.Removed), w, "…"))
		}
		for i, h := range f.Hunks {
			total += len(h.Lines)
			if limit > 0 && shown >= limit {
				continue
			}
			if i > 0 {
				out = append(out, diffIndent+st.dim.Render(strings.Repeat(" ", gw)+" ⋮"))
			}
			lines := st.hunkLines(h, gw, w)
			if limit > 0 {
				lines = lines[:min(len(lines), limit-shown)]
			}
			shown += len(lines)
			out = append(out, lines...)
		}
	}
	switch {
	case total > shown && limit > 0:
		out = append(out, diffIndent+st.dim.Render(fmt.Sprintf("… +%d lines (ctrl+t to view)", total-shown)))
	case total > shown:
		out = append(out, diffIndent+st.dim.Render(fmt.Sprintf("… %d more lines not kept", total-shown)))
	}

	return out
}

// gutterWidth fits the largest line number the files show.
func gutterWidth(files []patch.FileDiff) int {
	n := 1
	for _, f := range files {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				n = max(n, len(strconv.Itoa(l.Line())))
			}
		}
	}

	return n
}

// hunkLines draws a hunk, marking the changed words of each removed line
// that an added line replaced.
func (st *Styles) hunkLines(h patch.DiffHunk, gw, w int) []string {
	segs := make([][]seg, len(h.Lines))
	for i := 0; i < len(h.Lines); {
		dels := run(h.Lines, i, "-")
		adds := run(h.Lines, i+dels, "+")
		for k := range min(dels, adds) {
			segs[i+k], segs[i+dels+k] = wordDiff(untab(h.Lines[i+k].Text), untab(h.Lines[i+dels+k].Text))
		}
		i += max(dels+adds, 1)
	}
	out := make([]string, 0, len(h.Lines))
	for i, l := range h.Lines {
		out = append(out, st.diffLine(l, segs[i], gw, w))
	}

	return out
}

// run counts the lines of kind from i.
func run(lines []patch.DiffLine, i int, kind string) int {
	n := 0
	for i+n < len(lines) && lines[i+n].Kind == kind {
		n++
	}

	return n
}

// diffLine draws "  12 +text", tinted to the full width for an added or
// removed line; segs, when set, mark its changed words.
func (st *Styles) diffLine(l patch.DiffLine, segs []seg, gw, w int) string {
	ds, known := st.diffStyles[l.Kind]
	if !known {
		ds = st.diffStyles[" "]
	}
	if segs == nil {
		segs = []seg{{text: untab(l.Text)}}
	}
	head := diffIndent + ds.gutter.Render(fmt.Sprintf("%*d ", gw, l.Line())) + ds.sign.Render(l.Kind)
	room := max(w-ansi.StringWidth(head), 1)
	var b strings.Builder
	used := 0
	for _, s := range segs {
		text := s.text
		if width := ansi.StringWidth(text); used+width > room {
			text = ansi.Truncate(text, room-used, "…")
		}
		style := ds.line
		if s.changed {
			style = ds.word
		}
		b.WriteString(style.Render(text))
		if used += ansi.StringWidth(text); used >= room {
			break
		}
	}
	if l.Kind != " " {
		b.WriteString(ds.line.Render(strings.Repeat(" ", max(room-used, 0))))
	}

	return head + b.String()
}
