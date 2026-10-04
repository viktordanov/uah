package composer_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/composer"
)

// at builds a composer width cells wide (prompt included) holding draft,
// with the cursor at the |.
func at(t *testing.T, width int, draft string) composer.Composer {
	t.Helper()
	c := newComposer(width, 0)
	before, after, ok := strings.Cut(draft, "|")
	require.True(t, ok, "the draft marks the cursor with |")
	c.SetValue(before)
	c.InsertString(after)
	for range len([]rune(after)) {
		c.Press(composer.Key{Name: "left"})
	}

	return c
}

// marked is the draft with the cursor marked by |.
func marked(c composer.Composer) string {
	lines := strings.Split(c.Value(), "\n")
	l := []rune(lines[c.Line()])
	lines[c.Line()] = string(l[:c.Column()]) + "|" + string(l[c.Column():])

	return strings.Join(lines, "\n")
}

func press(c *composer.Composer, keys ...string) {
	for _, k := range keys {
		c.Press(composer.Key{Name: k})
	}
}

func typeText(c *composer.Composer, s string) {
	for _, r := range s {
		c.Press(composer.Key{Name: string(r), Text: string(r)})
	}
}

// shown is the composer's view without colors and trailing spaces.
func shown(c composer.Composer) []string {
	lines := strings.Split(ansi.Strip(c.View()), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}

	return lines
}

func TestKeys(t *testing.T) {
	tests := []struct {
		name, draft string
		keys        []string
		want        string
	}{
		{"right", "a|bc", []string{"right"}, "ab|c"},
		{"ctrl+f at the line's end goes to the next line", "ab|\ncd", []string{"ctrl+f"}, "ab\n|cd"},
		{"left at the line's start goes to the previous line's end", "ab\n|cd", []string{"left"}, "ab|\ncd"},
		{"ctrl+b at the draft's start stays", "|ab", []string{"ctrl+b"}, "|ab"},
		{"alt+f to the word's end", "|foo bar", []string{"alt+f"}, "foo| bar"},
		{"alt+f over spaces to the next word's end", "foo|  bar baz", []string{"alt+f"}, "foo  bar| baz"},
		{"ctrl+right across lines", "foo|\n  bar", []string{"ctrl+right"}, "foo\n  bar|"},
		{"alt+b to the word's start", "foo bar|", []string{"alt+b"}, "foo |bar"},
		{"alt+left across lines", "foo\n|  bar", []string{"alt+left"}, "|foo\n  bar"},
		{"home", "foo b|ar", []string{"home"}, "|foo bar"},
		{"ctrl+e", "fo|o bar\nx", []string{"ctrl+e"}, "foo bar|\nx"},
		{"ctrl+home", "ab\ncd|", []string{"ctrl+home"}, "|ab\ncd"},
		{"alt+>", "a|b\ncd", []string{"alt+>"}, "ab\ncd|"},
		{"up keeps the column", "abcd\nab|cd", []string{"up"}, "ab|cd\nabcd"},
		{"down to a shorter line stops at its end", "abc|d\nab", []string{"down"}, "abcd\nab|"},
		{"down then down keeps the goal column", "abc|d\nab\nabcd", []string{"down", "down"}, "abcd\nab\nabc|d"},
		{"up on the first row stays", "ab|c\nd", []string{"up"}, "ab|c\nd"},
		{"down on the last row stays", "a\nb|c", []string{"down"}, "a\nb|c"},
		{"backspace", "ab|c", []string{"backspace"}, "a|c"},
		{"backspace at the line's start joins", "ab\n|cd", []string{"backspace"}, "ab|cd"},
		{"ctrl+h at the draft's start does nothing", "|ab", []string{"ctrl+h"}, "|ab"},
		{"delete", "a|bc", []string{"delete"}, "a|c"},
		{"ctrl+d at the line's end joins", "ab|\ncd", []string{"ctrl+d"}, "ab|cd"},
		{"ctrl+w deletes the word before", "foo bar|", []string{"ctrl+w"}, "foo |"},
		{"ctrl+w deletes the spaces and the word", "foo bar  |x", []string{"ctrl+w"}, "foo |x"},
		{"ctrl+w keeps a leading space", " foo|", []string{"ctrl+w"}, " |"},
		{"alt+backspace at the line's start joins", "ab\n|cd", []string{"alt+backspace"}, "ab|cd"},
		{"alt+d deletes the word after", "foo| bar baz", []string{"alt+d"}, "foo| baz"},
		{"ctrl+delete at the line's end joins", "ab|\ncd", []string{"ctrl+delete"}, "ab|cd"},
		{"ctrl+k deletes to the line's end", "ab|cd\nef", []string{"ctrl+k"}, "ab|\nef"},
		{"ctrl+k at the line's end joins", "ab|\ncd", []string{"ctrl+k"}, "ab|cd"},
		{"ctrl+u deletes to the line's start", "ab\ncd|ef", []string{"ctrl+u"}, "ab\n|ef"},
		{"ctrl+u at the line's start joins", "ab\n|cd", []string{"ctrl+u"}, "ab|cd"},
		{"shift+enter splits the line", "ab|cd", []string{"shift+enter"}, "ab\n|cd"},
		{"ctrl+j splits the line", "ab|", []string{"ctrl+j"}, "ab\n|"},
		{"enter does not edit", "ab|", []string{"enter"}, "ab|"},
		{"alt+u uppercases the next word", "|foo bar", []string{"alt+u"}, "FOO| bar"},
		{"alt+l lowercases the next word", "FOO| BAR", []string{"alt+l"}, "FOO bar|"},
		{"alt+c capitalizes the next word", "|foo bar", []string{"alt+c", "alt+c"}, "Foo Bar|"},
		{"ctrl+t swaps and moves on", "a|bc", []string{"ctrl+t"}, "ba|c"},
		{"ctrl+t at the end swaps the last two", "abc|", []string{"ctrl+t"}, "acb|"},
		{"ctrl+t at the start does nothing", "|abc", []string{"ctrl+t"}, "|abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := at(t, 40, tt.draft)
			press(&c, tt.keys...)
			assert.Equal(t, tt.want, marked(c))
		})
	}
}

// TestClusters: the cursor steps over a grapheme cluster as one, and
// deletions delete it whole; Column counts runes.
func TestClusters(t *testing.T) {
	family := "👨‍👩‍👧"
	c := at(t, 40, "a"+family+"|b")
	assert.Equal(t, 1+len([]rune(family)), c.Column())
	press(&c, "left")
	assert.Equal(t, "a|"+family+"b", marked(c))
	press(&c, "delete")
	assert.Equal(t, "a|b", marked(c))

	c = at(t, 40, "é🇺🇦漢|")
	press(&c, "backspace", "backspace")
	assert.Equal(t, "é|", marked(c))
	press(&c, "left")
	assert.Equal(t, 0, c.Column())

	c = at(t, 40, "x漢"+family+"|")
	press(&c, "ctrl+t")
	assert.Equal(t, "x"+family+"漢|", marked(c), "ctrl+t swaps clusters")

	c = at(t, 40, "a|")
	typeText(&c, "́") // a combining mark joins the cluster before it
	assert.Equal(t, "a\u0301|", marked(c))
}

// TestWrap: rows break at spaces, as textarea's do; a word wider than the
// row is cut; the last row keeps a cell for the cursor.
func TestWrap(t *testing.T) {
	tests := []struct {
		name  string
		width int // the text's, the prompt adds 2
		draft string
		want  []string
	}{
		{"fits", 10, "hello", []string{"λ hello"}},
		{"breaks at a space", 10, "hello world foo", []string{"λ hello", "  world foo"}},
		{"a word and its space move together", 10, "hello abcd x", []string{"λ hello", "  abcd x"}},
		{"the cursor's cell after a full last row", 10, "hello abcd", []string{"λ hello", "  abcd"}},
		{"a long word is cut", 5, "abcdefghij", []string{"λ abcde", "  fghij", ""}},
		{"a long word after a short one", 10, "hello abcdefghijkl", []string{"λ hello", "  abcdefghij", "  kl"}},
		{"wide characters", 5, "漢字漢字漢", []string{"λ 漢字", "  漢字", "  漢"}},
		{"a wide character never straddles the edge", 5, "aaaa漢", []string{"λ aaaa", "  漢"}},
		{"a cluster is never cut", 4, "ab👨‍👩‍👧c", []string{"λ ab👨‍👩‍👧", "  c"}},
		{"lines wrap on their own", 6, "abc def\nghi", []string{"λ abc", "  def", "  ghi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newComposer(tt.width+2, 0)
			c.SetValue(tt.draft)
			assert.Equal(t, tt.want, shown(c))
			assert.Equal(t, len(tt.want), c.Height())
		})
	}
}

// TestCursor: the terminal's cursor follows the text's cells, after the
// prompt, and moves to the next row at a full row's end.
func TestCursor(t *testing.T) {
	tests := []struct {
		name  string
		width int // the text's, the prompt adds 2
		draft string
		x, y  int
	}{
		{"empty", 10, "|", 2, 0},
		{"after text", 10, "ab|", 4, 0},
		{"after a wide character", 10, "漢|", 4, 0},
		{"after a cluster", 10, "👨‍👩‍👧|", 4, 0},
		{"on a wrapped row", 10, "hello wor|ld", 5, 1},
		{"at a row's end shows at the next row's start", 10, "hello |world", 2, 1},
		{"after a full last row", 5, "abcde|", 2, 1},
		{"on a later line", 10, "ab\nc|", 3, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := at(t, tt.width+2, tt.draft)
			x, y, ok := c.Cursor()
			require.True(t, ok)
			assert.Equal(t, [2]int{tt.x, tt.y}, [2]int{x, y})
		})
	}
	c := newComposer(12, 0)
	c.Blur()
	_, _, ok := c.Cursor()
	assert.False(t, ok, "a blurred composer hides the cursor")
	press(&c, "a")
	c.Press(composer.Key{Name: "a", Text: "a"})
	assert.Empty(t, c.Value(), "and takes no keys")
}

// TestLineInfo: uah's ↑ and ↓ scroll the transcript only on the draft's
// first and last rows, which LineInfo tells.
func TestLineInfo(t *testing.T) {
	c := at(t, 12, "hello wor|ld foo")
	info := c.LineInfo()
	assert.Equal(t, composer.LineInfo{Height: 2, RowOffset: 1, StartColumn: 6, Width: 9, CharWidth: 9, ColumnOffset: 3, CharOffset: 3}, info)
	press(&c, "up")
	assert.Equal(t, 0, c.LineInfo().RowOffset)
	assert.Equal(t, "hel|lo world foo", marked(c))
}

// TestHeight: the composer grows with its rows to the limit, then scrolls
// to keep the cursor in view; the prompt's λ scrolls away with row 0.
func TestHeight(t *testing.T) {
	c := newComposer(20, 3)
	assert.Equal(t, 1, c.Height())
	c.SetValue("one\ntwo")
	assert.Equal(t, 2, c.Height())
	c.SetValue("one\ntwo\nthree\nfour\nfive")
	assert.Equal(t, 3, c.Height())
	assert.Equal(t, []string{"  three", "  four", "  five"}, shown(c))
	_, y, _ := c.Cursor()
	assert.Equal(t, 2, y)
	press(&c, "ctrl+home")
	assert.Equal(t, []string{"λ one", "  two", "  three"}, shown(c))
	press(&c, "down", "down", "down")
	assert.Equal(t, []string{"  two", "  three", "  four"}, shown(c))
	_, y, _ = c.Cursor()
	assert.Equal(t, 2, y)
	c.SetMaxHeight(10)
	assert.Equal(t, 5, c.Height(), "a higher limit shows every row")
	c.SetValue("")
	assert.Equal(t, 1, c.Height())
}

// TestPage: pgup and pgdn go to the visible edge, then a page on.
func TestPage(t *testing.T) {
	c := newComposer(20, 3)
	c.SetValue("1\n2\n3\n4\n5\n6\n7\n8")
	press(&c, "pgup")
	assert.Equal(t, 5, c.Line(), "the first visible row")
	press(&c, "pgup")
	assert.Equal(t, 2, c.Line(), "a page up")
	press(&c, "pgdown")
	assert.Equal(t, 4, c.Line(), "the last visible row")
}

// TestPlaceholder: the empty composer shows its placeholder, wrapped to the
// width, on its one row.
func TestPlaceholder(t *testing.T) {
	c := newComposer(40, 8)
	c.SetStyles(composer.Styles{Placeholder: func(s string) string { return "<" + s + ">" }, Prompt: func(s string) string { return "[" + s + "]" }})
	assert.Equal(t, "[λ ]<Ask uah to do anything>", c.View())
	c.SetWidth(14)
	assert.Equal(t, "[λ ]<Ask uah to>", c.View())
	typeText(&c, "x")
	assert.Equal(t, "[λ ]x", c.View())
}

// TestPrompt: a narrower prompt is padded on the left; a new prompt
// keeps the composer's width.
func TestPrompt(t *testing.T) {
	c := newComposer(12, 0)
	c.SetValue("hello world")
	assert.Equal(t, 10, c.Width())
	c.SetPrompt(3, func(row int) string { return "!" })
	assert.Equal(t, 9, c.Width())
	assert.Equal(t, []string{"  !hello", "  !world"}, shown(c))
}

// TestPaste: a paste keeps its lines, turns tabs into spaces and \r\n into
// one line break, and drops control characters.
func TestPaste(t *testing.T) {
	c := at(t, 40, "a|b")
	c.Paste("one\r\ntwo\tx\x1b[31m\nthree")
	assert.Equal(t, "aone\ntwo    x[31m\nthree|b", marked(c))
	assert.Equal(t, 3, c.LineCount())
	assert.Equal(t, composer.ActionPaste, c.Press(composer.Key{Name: "ctrl+v"}))
}

// TestMaxLines: a paste past MaxLines is cut, and a new line is refused.
func TestMaxLines(t *testing.T) {
	c := newComposer(40, 0)
	c.Paste(strings.Repeat("x\n", composer.MaxLines+5))
	assert.Equal(t, composer.MaxLines, c.LineCount())
	press(&c, "shift+enter")
	assert.Equal(t, composer.MaxLines, c.LineCount())
}

// TestLongLine: a line far wider than the window wraps and scrolls.
func TestLongLine(t *testing.T) {
	c := newComposer(22, 5)
	c.SetValue(strings.Repeat("word ", 2000))
	assert.Equal(t, 5, c.Height())
	assert.Equal(t, 2000*5, c.Column())
	x, y, _ := c.Cursor()
	assert.Equal(t, 4, y)
	assert.Less(t, x, 22)
	press(&c, "home")
	_, y, _ = c.Cursor()
	assert.Equal(t, 0, y)
	assert.Equal(t, 0, c.ScrollOffset())
}

// TestSelection: shift+arrows select, typing replaces the selection, a
// deletion deletes it, and ctrl+shift+c asks to copy it.
func TestSelection(t *testing.T) {
	c := at(t, 40, "foo bar|")
	press(&c, "shift+left", "shift+left", "shift+left")
	assert.True(t, c.HasSelection())
	assert.Equal(t, "bar", c.SelectedText())
	assert.Equal(t, composer.ActionCopy, c.Press(composer.Key{Name: "ctrl+shift+c"}))
	typeText(&c, "baz")
	assert.Equal(t, "foo baz|", marked(c))
	assert.False(t, c.HasSelection())

	press(&c, "alt+shift+b", "backspace")
	assert.Equal(t, "foo |", marked(c), "the deletion deletes the selection only")

	c = at(t, 40, "ab|\ncd")
	press(&c, "shift+right", "shift+right")
	assert.Equal(t, "\nc", c.SelectedText())
	c.SetStyles(composer.Styles{Selection: func(s string) string { return "[" + s + "]" }})
	assert.Equal(t, "λ ab\n  [c]d", c.View())
	press(&c, "left")
	assert.False(t, c.HasSelection(), "a move drops the selection")
	assert.Equal(t, composer.ActionNone, c.Press(composer.Key{Name: "ctrl+shift+c"}))

	c = at(t, 40, "ab\ncd|")
	press(&c, "ctrl+g")
	assert.Equal(t, "ab\ncd", c.SelectedText())
	c.Paste("x")
	assert.Equal(t, "x|", marked(c))
}
