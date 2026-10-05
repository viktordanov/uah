package composer

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// fuzzKeys are the keys the fuzzer presses, by a byte's value.
var fuzzKeys = []string{
	"left", "right", "up", "down", "home", "end", "alt+f", "alt+b", "ctrl+home", "ctrl+end",
	"backspace", "delete", "ctrl+w", "alt+d", "ctrl+k", "ctrl+u", "ctrl+y", "ctrl+_", "shift+enter", "pgup", "pgdown",
	"alt+u", "alt+l", "alt+c", "ctrl+t", "shift+left", "shift+right", "alt+shift+f", "alt+shift+b",
	"shift+up", "shift+down", "ctrl+g", "ctrl+shift+c", "ctrl+v",
}

// fuzzTexts are typed or pasted by a byte's value: wide characters,
// clusters of several runes, and control characters among them.
var fuzzTexts = []string{
	"a", " ", "word ", "漢字", "👨‍👩‍👧", "é", "é", "🇺🇦", "\t", "x\r\ny", "\x1b[31m", "longwordwithoutspaces", "​", "  ",
}

// check asserts the composer's invariants.
func check(t *testing.T, c *Composer) {
	t.Helper()
	if c.row < 0 || c.row >= len(c.lines) || c.col < 0 || c.col > len(c.cur()) {
		t.Fatalf("cursor %d,%d outside the draft of %d lines", c.row, c.col, len(c.lines))
	}
	lay := c.layout(c.row)
	if g := lay.cluster(c.col); lay.starts[g] != c.col {
		t.Fatalf("cursor %d,%d inside a cluster", c.row, c.col)
	}
	value := c.Value()
	if got := strings.Count(value, "\n") + 1; got != len(c.lines) {
		t.Fatalf("Value has %d lines, the draft %d", got, len(c.lines))
	}
	total := 0
	for i, l := range c.lines {
		lay := c.layout(i)
		var joined strings.Builder
		for r := range lay.rows {
			from, to := lay.starts[lay.rows[r]], lay.starts[lay.rowEnd(r)]
			text := string(l.text[from:to])
			joined.WriteString(text)
			if w := ansi.StringWidth(text); w > c.width && lay.rowEnd(r)-lay.rows[r] > 1 {
				t.Fatalf("line %d row %d is %d cells wide, past %d: %q", i, r, w, c.width, text)
			}
		}
		if joined.String() != string(l.text) {
			t.Fatalf("line %d's rows %q lose text of %q", i, joined.String(), string(l.text))
		}
		total += len(lay.rows)
	}
	want := max(total, 1)
	if c.maxHeight > 0 {
		want = min(want, c.maxHeight)
	}
	if c.height != want {
		t.Fatalf("height %d, want %d of %d rows", c.height, want, total)
	}
	x, y, ok := c.Cursor()
	if !ok || y < 0 || y >= c.height {
		t.Fatalf("cursor row %d outside the %d shown", y, c.height)
	}
	if li := c.LineInfo(); x >= c.promptWidth+c.width && li.CharWidth <= c.width {
		t.Fatalf("cursor column %d past the width %d+%d: %q at %d,%d %+v rows %v", x, c.promptWidth, c.width, c.Value(), c.row, c.col, li, c.layout(c.row).rows)
	}
	if got := len(strings.Split(c.View(), "\n")); got != c.height {
		t.Fatalf("View has %d rows, the height is %d", got, c.height)
	}
	if !c.sel.on {
		return
	}
	for _, p := range []Position{c.sel.anchor, c.sel.head} {
		if p.Row >= len(c.lines) || p.Col > len(c.lines[p.Row].text) {
			t.Fatalf("selection end %+v outside the draft", p)
		}
	}
}

func FuzzComposer(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3}, uint8(10))
	f.Add([]byte{0x80, 0x84, 0x85, 0x0d, 0x10, 0x8b, 0x02, 0x16}, uint8(3))
	f.Add([]byte("typing some words and moving around"), uint8(1))
	f.Fuzz(func(t *testing.T, ops []byte, width uint8) {
		c := New()
		c.Placeholder = "Ask uah"
		c.SetPrompt(2, func(row int) string {
			if row == 0 {
				return "λ "
			}

			return ""
		})
		c.SetMaxHeight(5)
		c.SetWidth(int(width%40) + 1)
		for i, op := range ops {
			switch {
			case op&0x80 != 0:
				text := fuzzTexts[int(op&0x7f)%len(fuzzTexts)]
				if op&0x40 != 0 {
					c.Paste(text + "\n" + text)
				} else {
					c.Press(Key{Name: text, Text: text})
				}
			case op == 0x7f:
				c.SetWidth(int(ops[(i+1)%len(ops)]%40) + 1)
			default:
				c.Press(Key{Name: fuzzKeys[int(op)%len(fuzzKeys)]})
			}
			check(t, &c)
		}
		value := c.Value()
		c.SetValue(value)
		if c.Value() != value {
			t.Fatalf("SetValue(%q) gives %q", value, c.Value())
		}
		check(t, &c)
	})
}
