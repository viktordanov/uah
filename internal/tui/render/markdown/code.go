package markdown

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// WrapMark starts a code line's continuation, dim in the padding column: a
// line longer than the width goes on below it.
const WrapMark = "↪"

// WrapTag starts each row of a wrapped code line with its piece's number
// (0 for the first) and width in cells, for a copy (render's SelectedText)
// to join the pieces back exactly: an OSC 8 with parameters and no URI,
// which a terminal reads as the end of a link and so ignores, and no
// width. A padded cell after a piece, where a wide character did not fit,
// is then not taken for a space.
func WrapTag(piece, cells int) string {
	return fmt.Sprintf("\x1b]8;%s=%d,%d;\x1b\\", WrapParam, piece, cells)
}

// WrapParam names WrapTag's parameter.
const WrapParam = "uah-wrap"

// codeBlock draws code on the band, highlighted, with the fence's language
// dim at the right end of its first line. A line longer than the width
// wraps at the cell that ends it, losing no character, and each piece
// after the first starts with WrapMark in place of the padding; Codex cuts
// such a line off instead.
// A diff's + and - lines sit on the edit tool's tints instead of the band.
// While a fence is still open its last line is usually incomplete: the
// complete lines are highlighted as one piece, which stays cached until the
// next newline, and the partial line on its own.
func (r *Renderer) codeBlock(lang, code string, w int) []string {
	var hl []string
	if i := strings.LastIndexByte(code, '\n'); i >= 0 && i < len(code)-1 {
		hl = append(slices.Clip(r.highlight(lang, code[:i])), r.highlight(lang, code[i+1:])...)
	} else {
		hl = r.highlight(lang, strings.TrimSuffix(code, "\n"))
	}
	var src []string
	if isDiff(lang) {
		src = strings.Split(strings.TrimSuffix(code, "\n"), "\n")
	}
	out := make([]string, 0, len(hl))
	for i, l := range hl {
		band := r.st.Band
		if i < len(src) {
			band = r.tint(src[i])
		}
		pieces := codePieces(l, w-1)
		for j, piece := range pieces {
			line := " " + piece
			if j > 0 {
				line = r.st.Dim.Render(WrapMark) + piece
			}
			if len(pieces) > 1 {
				line = WrapTag(j, ansi.StringWidth(piece)) + line
			}
			if i == 0 && j == 0 {
				line = r.labeled(line, lang, w)
			}
			out = append(out, band(line, w))
		}
	}

	return out
}

// codePieces cuts a highlighted line into pieces of at most w cells, in
// one pass, each starting with the styles in force where it starts and
// ending them: every character stays, spaces too, so the pieces joined are
// the line. A wide character that would cross a piece's end starts the
// next. A width too narrow to read keeps the line whole.
func codePieces(line string, w int) []string {
	if w < minCodeWidth || ansi.StringWidth(line) <= w {
		return []string{line}
	}
	var (
		out    []string
		piece  strings.Builder
		styles string // the SGR sequences in force since the last reset
		cells  int
		pstate byte
	)
	end := func() {
		if styles != "" {
			piece.WriteString(sgrReset)
		}
		out = append(out, piece.String())
		piece.Reset()
		piece.WriteString(styles)
		cells = 0
	}
	for rest := line; rest != ""; {
		seq, width, n, next := ansi.DecodeSequence(rest, pstate, nil)
		pstate, rest = next, rest[n:]
		if width == 0 {
			if strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
				if isReset(seq) {
					styles = ""
				} else {
					styles += seq
				}
			}
			piece.WriteString(seq)

			continue
		}
		if cells+width > w && cells > 0 {
			end()
		}
		piece.WriteString(seq)
		cells += width
	}
	if cells > 0 || len(out) == 0 {
		out = append(out, piece.String())
	}

	return out
}

// minCodeWidth is the narrowest code a line is wrapped to.
const minCodeWidth = 8

// labeled is a code block's first line with the language dim at the right
// end of width w, a cell from the edge. A line that leaves no space before
// the label goes without it.
func (r *Renderer) labeled(line, lang string, w int) string {
	gap := w - ansi.StringWidth(line) - ansi.StringWidth(lang) - 1
	if lang == "" || gap < 1 {
		return line
	}

	return line + strings.Repeat(" ", gap) + r.st.Dim.Render(lang) + " "
}

// tint is how a diff line is drawn: on the added or removed tint by its
// first character, else on the band.
func (r *Renderer) tint(line string) func(string, int) string {
	switch {
	case strings.HasPrefix(line, "+"):
		return r.st.Added
	case strings.HasPrefix(line, "-"):
		return r.st.Removed
	}

	return r.st.Band
}

// isDiff reports whether a fence's language is one of chroma's names for
// a diff.
func isDiff(lang string) bool {
	switch strings.ToLower(lang) {
	case "diff", "patch", "udiff":
		return true
	}

	return false
}
