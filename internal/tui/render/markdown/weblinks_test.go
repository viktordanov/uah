package markdown

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The long example of issue #9.
const (
	longURL   = "https://example.com/documentation/a/very/long/path/that/wraps/across/multiple/terminal/rows?first=one&second=two"
	longLinks = "[Example documentation](" + longURL + ")\n\n" + longURL + "\n"
)

func TestWebURLs(t *testing.T) {
	t.Parallel()
	for text, want := range map[string][]string{
		"see https://example.com/docs.":                       {"https://example.com/docs"},
		"see https://example.com/docs, then":                  {"https://example.com/docs"},
		"is it https://example.com/a?":                        {"https://example.com/a"},
		"wow https://example.com/a!":                          {"https://example.com/a"},
		"https://example.com/a; https://example.com/b:":       {"https://example.com/a", "https://example.com/b"},
		`"https://example.com/quoted"`:                        {"https://example.com/quoted"},
		"'https://example.com/single'.":                       {"https://example.com/single"},
		"(see https://x.com/a)":                               {"https://x.com/a"},
		"(see https://x.com/a).":                              {"https://x.com/a"},
		"https://en.wikipedia.org/wiki/Foo_(bar)":             {"https://en.wikipedia.org/wiki/Foo_(bar)"},
		"(https://en.wikipedia.org/wiki/Foo_(bar))":           {"https://en.wikipedia.org/wiki/Foo_(bar)"},
		"[https://x.com/a] and <https://x.com/b>":             {"https://x.com/a", "https://x.com/b"},
		longURL + ".":                                         {longURL},
		"https://x.com/search?q=a+b&lang=en#results":          {"https://x.com/search?q=a+b&lang=en#results"},
		"https://x.com/page#section-2.":                       {"https://x.com/page#section-2"},
		"http://[::1]:8080/health and http://127.0.0.1:3000/": {"http://[::1]:8080/health", "http://127.0.0.1:3000/"},
		"http://localhost:8080/a/b":                           {"http://localhost:8080/a/b"},
		"HTTPS://EXAMPLE.COM/A":                               {"HTTPS://EXAMPLE.COM/A"},
		"url=https://x.com/a":                                 {"https://x.com/a"},
		"ftp://x.com/a mailto:a@x.com file:///etc/passwd":     nil,
		"javascript:alert(1) data:text/html,x":                nil,
		"xhttps://x.com/a and ahttp://x.com":                  nil,
		"https:// and https:/x.com and http://":               nil,
		"https://x.com/%zz":                                   nil,
		"no links here, just example.com":                     nil,
	} {
		var got []string
		for _, u := range webURLs(text) {
			assert.Equal(t, u.target, text[u.from:u.to], "%q: the target is the text", text)
			got = append(got, u.target)
		}
		assert.Equal(t, want, got, "%q", text)
	}
}

func TestWebTarget(t *testing.T) {
	t.Parallel()
	for dest, want := range map[string]string{
		"https://example.com/a?b=c#d":                     "https://example.com/a?b=c#d",
		"http://x.com":                                    "http://x.com",
		"https://de.wikipedia.org/wiki/Ä":                 "https://de.wikipedia.org/wiki/%C3%84",
		"https://x.com/‮gnp.exe":                          "https://x.com/%E2%80%AEgnp.exe",
		"https://x.com/\x1b]8;;evil\x1b\\":                "",
		"https://x.com/\a":                                "",
		"https://x.com/a\nb":                              "",
		"https://x.com/a\tb":                              "",
		"https://x.com/a b":                               "",
		"https://x.com/ ":                                 "",
		"https://x.com/\u0085":                            "",
		"https://x.com/\x7f":                              "",
		"https://x.com/\xff":                              "",
		"https://x.com/" + strings.Repeat("a", maxTarget): "",
		"/relative/path":                                  "",
		"docs/guide.md":                                   "",
		"file:///etc/passwd":                              "",
		"javascript:alert(1)":                             "",
		"mailto:a@example.com":                            "",
		"https:///no-host":                                "",
		"":                                                "",
	} {
		got, ok := webTarget(dest)
		assert.Equal(t, want, got, "%q", dest)
		assert.Equal(t, want != "", ok, "%q", dest)
	}
}

// links reads a drawn line's hyperlinks: the text each target covers.
type linkedText struct{ target, text string }

func linksIn(line string) []linkedText {
	var out []linkedText
	cur := ""
	var pstate byte
	for line != "" {
		seq, width, n, next := ansi.DecodeSequence(line, pstate, nil)
		pstate, line = next, line[n:]
		if m := hyperlink.FindStringSubmatch(seq); m != nil && m[0] == seq {
			cur = m[1]

			continue
		}
		if width == 0 || cur == "" {
			continue
		}
		if k := len(out) - 1; k >= 0 && out[k].target == cur {
			out[k].text += seq
		} else {
			out = append(out, linkedText{target: cur, text: seq})
		}
	}

	return out
}

func linkedRenderer() *Renderer {
	r := New(testStyles())
	r.SetLinks(true)

	return r
}

// TestWebLinksSurviveTheWrap: the issue's example at a narrow width, every
// fragment of the label, the URL after it, and the bare URL leads to the
// whole URL, and each line ends its link.
func TestWebLinksSurviveTheWrap(t *testing.T) {
	t.Parallel()
	for _, w := range []int{80, 30, 12} {
		lines := linkedRenderer().Render(longLinks, w, "● ", "  ")
		var linked strings.Builder
		fragments := 0
		for _, l := range lines {
			assert.LessOrEqual(t, ansi.StringWidth(l), w, "%q", shown(l))
			for _, lt := range linksIn(l) {
				require.Equal(t, longURL, lt.target, "at %d: every fragment leads to the whole URL", w)
				linked.WriteString(ansi.Strip(lt.text))
				fragments++
			}
			if seqs := hyperlink.FindAllString(l, -1); len(seqs) > 0 {
				assert.Equal(t, LinkClose, seqs[len(seqs)-1], "at %d a line ends its link: %q", w, l)
			}
		}
		// The wrap drops the space it breaks the label at.
		want := "Example documentation" + longURL + longURL
		assert.Equal(t, strings.ReplaceAll(want, " ", ""), strings.ReplaceAll(linked.String(), " ", ""), "at %d: the label, the URL after it, and the bare URL", w)
		if w == 30 {
			assert.Greater(t, fragments, 8, "the URLs wrap")
		}
	}
}

// TestWebLinksOffDrawTheTextAlone: without links the text is the same, and
// nothing but styles is drawn.
func TestWebLinksOffDrawTheTextAlone(t *testing.T) {
	t.Parallel()
	off := New(testStyles()).Render(longLinks, 30, "", "")
	on := linkedRenderer().Render(longLinks, 30, "", "")
	require.Len(t, on, len(off))
	for i := range off {
		assert.NotContains(t, off[i], "\x1b]8;")
		assert.Equal(t, ansi.Strip(off[i]), ansi.Strip(on[i]))
	}
	r := linkedRenderer()
	assert.Contains(t, strings.Join(r.Render(longLinks, 30, "", ""), ""), "\x1b]8;")
	r.SetLinks(false)
	assert.NotContains(t, strings.Join(r.Render(longLinks, 30, "", ""), ""), "\x1b]8;", "turning links off drops the kept lines")
}

// TestWebLinksWhere: links in prose, emphasis, headings, lists, quotes, and
// tables; none in code or for other schemes.
func TestWebLinksWhere(t *testing.T) {
	t.Parallel()
	doc := strings.Join([]string{
		"# See https://a.example/h1",
		"Text **https://a.example/bold** and `https://a.example/code`.",
		"- item https://a.example/list",
		"> quoted https://a.example/quote",
		"| a | b |\n|---|---|\n| https://a.example/cell | [cell](https://a.example/linked) |",
		"```\nhttps://a.example/fence\n```",
		"    https://a.example/indented",
		"[relative](docs/guide.md), [mail](mailto:a@example.com), [script](javascript:alert(1)), <ftp://a.example/f>.",
		"[**bold `code` label**](https://a.example/label) and ![image](https://a.example/i.png).",
		"[https://a.example/same](https://a.example/same)",
		"[spaced](<https://a.example/a b>) and www.example.com",
		"[https://a.example/not-the-destination](docs/a.md)",
		"<https://a.example/bang!> and https://a.example/found!",
		"Beyond ASCII: https://de.wikipedia.org/wiki/Äpfel_(Obst) and see https://x.example/日本.",
		"Local: http://localhost/foo_bar_baz, http://[::1]:8080/foo and \"http://127.0.0.1:3000/a_b\".",
	}, "\n\n")
	got := map[string]string{}
	for _, l := range linkedRenderer().Render(doc, 100, "", "") {
		for _, lt := range linksIn(l) {
			got[lt.target] += ansi.Strip(lt.text)
		}
	}
	assert.Equal(t, map[string]string{
		"https://a.example/h1":                            "https://a.example/h1",
		"https://a.example/bold":                          "https://a.example/bold",
		"https://a.example/list":                          "https://a.example/list",
		"https://a.example/quote":                         "https://a.example/quote",
		"https://a.example/cell":                          "https://a.example/cell",
		"https://a.example/linked":                        "cellhttps://a.example/linked",
		"https://a.example/label":                         "bold code labelhttps://a.example/label",
		"https://a.example/i.png":                         "imagehttps://a.example/i.png",
		"https://a.example/same":                          "https://a.example/same",
		"https://a.example/bang!":                         "https://a.example/bang!",
		"https://a.example/found":                         "https://a.example/found",
		"https://de.wikipedia.org/wiki/%C3%84pfel_(Obst)": "https://de.wikipedia.org/wiki/Äpfel_(Obst)",
		"https://x.example/%E6%97%A5%E6%9C%AC":            "https://x.example/日本",
		"http://localhost/foo_bar_baz":                    "http://localhost/foo_bar_baz",
		"http://[::1]:8080/foo":                           "http://[::1]:8080/foo",
		"http://127.0.0.1:3000/a_b":                       "http://127.0.0.1:3000/a_b",
	}, got, "an autolink in <> keeps its last character, a bare URL does not")
	h1 := linkedRenderer().Render("# See https://a.example/H1", 80, "", "")
	assert.Equal(t, "SEE https://a.example/H1", ansi.Strip(h1[0]), "a heading's URL keeps its case")
}

// TestWebLinksEscapeNothing: a destination that would break out of the
// hyperlink is drawn as text, and a ";" goes as written (it is reserved).
func TestWebLinksEscapeNothing(t *testing.T) {
	t.Parallel()
	out := strings.Join(linkedRenderer().Render("[x](https://a.example/&#27;]8;;evil&#7;) and https://a.example/page;jsessionid=abc", 80, "", ""), "\n")
	osc := regexp.MustCompile("\x1b\\][^\x1b\a]*(\x1b\\\\|\a)")
	for _, seq := range osc.FindAllString(out, -1) {
		assert.Regexp(t, "^\x1b\\]8;;(https://a\\.example/page;jsessionid=abc)?\x1b\\\\$", seq)
	}
	assert.Equal(t, 1, strings.Count(out, "\x1b]8;;https"), "only the bare URL links")
}
