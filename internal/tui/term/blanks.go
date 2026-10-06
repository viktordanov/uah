package term

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// eraseTail shortens the blanks at the end of line, a row's line cut to w
// cells, as written after the row was erased: blanks without any style are
// dropped, since the erase left the cells blank, and four or more blanks
// with only a background that reach the row's end become an erase to it
// (EL), which fills the cells with the background in force. Blanks with
// any other style, a line that holds a hyperlink, and blanks that end
// short of the row stay as they are.
func eraseTail(line string, w int) string {
	if !strings.HasSuffix(line, " ") && !strings.HasSuffix(line, "m") || strings.Contains(line, "\x1b]8;") {
		return line
	}
	// ts is where the trailing run of spaces and SGR sequences starts.
	ts := len(line)
	for ts > 0 {
		if line[ts-1] == ' ' {
			ts--

			continue
		}
		if st := strings.LastIndexByte(line[:ts], 0x1b); line[ts-1] == 'm' && st >= 0 {
			if end, ok := sgrEnd(line, st); ok && end == ts {
				ts = st

				continue
			}
		}

		break
	}
	tail := line[ts:]
	first, last := strings.IndexByte(tail, ' '), strings.LastIndexByte(tail, ' ')
	if first < 0 || strings.Contains(tail[first:last+1], "\x1b") {
		return line
	}
	lead, blanks, trail := tail[:first], tail[first:last+1], tail[last+1:]
	bg, other := blankLook(collectSGR(line[:ts]) + lead)
	switch {
	case other:
		return line
	case !bg:
		return line[:ts] + trail
	case len(blanks) < 4 || ansi.StringWidth(line) < w:
		return line
	}

	return line[:ts] + lead + "\x1b[K" + trail
}

// blankLook reads SGR sequences in order and reports the style left in
// force: whether it sets a background, and whether it sets anything else
// (a foreground, a weight, an underline and its color, reverse, and so
// on). An erase draws a cell with the background only, so only blanks
// without anything else become one.
func blankLook(seqs string) (bg, other bool) {
	set := map[string]bool{}
	for p := 0; p < len(seqs); {
		end, ok := sgrEnd(seqs, p)
		if !ok {
			return false, true
		}
		params := strings.Split(seqs[p+2:end-1], ";")
		p = end
		for i := 0; i < len(params); i++ {
			code, _, _ := strings.Cut(params[i], ":")
			switch {
			case code == "" || code == "0":
				bg = false
				clear(set)
			case code == "48":
				bg = true
				i += extendedLen(params[i+1:])
			case code == "49":
				bg = false
			case code == "38" || code == "58":
				set[code] = true
				i += extendedLen(params[i+1:])
			case len(code) == 2 && code[0] == '4' || len(code) == 3 && code[:2] == "10":
				bg = true // 40–47, 100–107
			case len(code) == 2 && (code[0] == '3' || code[0] == '9') && code != "39":
				set["38"] = true // 30–37, 90–97; 39 is below
			default:
				if off, ok := sgrOff[code]; ok {
					for _, c := range off {
						delete(set, c)
					}
				} else {
					set[code] = true
				}
			}
		}
	}

	return bg, len(set) > 0
}

// sgrOff are the SGR codes that end others: 22 ends bold and faint, 39 the
// foreground, 59 the underline color, and so on.
var sgrOff = map[string][]string{
	"22": {"1", "2"}, "23": {"3"}, "24": {"4", "21"}, "25": {"5", "6"}, "27": {"7"},
	"28": {"8"}, "29": {"9"}, "39": {"38"}, "55": {"53"}, "59": {"58"},
}

// extendedLen is how many parameters follow 38, 48, or 58: 5;n or 2;r;g;b
// (none in the colon form, which is one parameter).
func extendedLen(rest []string) int {
	switch {
	case len(rest) > 0 && rest[0] == "5":
		return min(2, len(rest))
	case len(rest) > 0 && rest[0] == "2":
		return min(4, len(rest))
	}

	return 0
}
