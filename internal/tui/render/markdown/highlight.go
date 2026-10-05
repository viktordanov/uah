package markdown

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Codex's limits for highlighting (codex-rs/tui/src/render/highlight.rs):
// larger code is drawn plain.
const (
	maxCodeBytes = 512 << 10
	maxCodeLines = 10_000
	maxLineBytes = 4 << 10
)

// maxCached is how many highlighted blocks a Renderer keeps, and maxLexers
// how many languages it remembers the lexer of.
const (
	maxCached = 256
	maxLexers = 64
)

type codeKey struct{ lang, code string }

// codeCache keeps highlighted code by language and text, the oldest out
// first, and the lexer of each language name (nil for none).
type codeCache struct {
	lines  map[codeKey][]string
	order  []codeKey
	lexers map[string]chroma.Lexer
}

func newCodeCache() codeCache {
	return codeCache{lines: map[codeKey][]string{}, lexers: map[string]chroma.Lexer{}}
}

// highlight colors code with chroma, one string per line.
func (r *Renderer) highlight(lang, code string) []string {
	key := codeKey{lang, code}
	if lines, ok := r.code.lines[key]; ok {
		return lines
	}
	lines := r.colorize(lang, untab(code))
	if len(r.code.order) >= maxCached {
		delete(r.code.lines, r.code.order[0])
		r.code.order = r.code.order[1:]
	}
	r.code.lines[key] = lines
	r.code.order = append(r.code.order, key)

	return lines
}

// colorize highlights code whose language chroma knows, and draws the rest
// in the code color. A fence without a language is not guessed at, as in
// Codex: guessing runs every lexer's analyser.
func (r *Renderer) colorize(lang, code string) []string {
	plain := strings.Split(code, "\n")
	lexer := r.lexer(lang)
	if lexer == nil || r.st.CodeStyle == nil || tooLarge(code, plain) {
		return r.plainCode(plain)
	}
	out, ok := format(lexer, r.st.CodeStyle, code, len(plain))
	if !ok {
		return r.plainCode(plain)
	}

	return out
}

// format colors code with a lexer and a style, a string per line, each
// line's colors ending with it; ok is false when chroma fails.
func format(lexer chroma.Lexer, style *chroma.Style, code string, n int) (out []string, ok bool) {
	it, err := lexer.Tokenise(nil, code)
	if err != nil {
		return nil, false
	}
	out = make([]string, 0, n)
	var b strings.Builder
	for _, tokens := range chroma.SplitTokensIntoLines(it.Tokens()) {
		b.Reset()
		if err := formatters.TTY16m.Format(&b, style, chroma.Literator(tokens...)); err != nil {
			return nil, false
		}
		out = append(out, strings.ReplaceAll(b.String(), "\n", ""))
	}
	// The lexer may end the code with a newline of its own.
	for len(out) < n {
		out = append(out, "")
	}

	return out[:n], true
}

// Highlight colors a file's lines by the language its name says, with
// style; text chroma has no lexer for, or too large to highlight (as a
// code block), comes back plain. It keeps no cache, so any goroutine may
// call it.
func Highlight(style *chroma.Style, name string, lines []string) []string {
	code := strings.Join(lines, "\n")
	lexer := lexers.Match(name)
	if lexer == nil || style == nil || tooLarge(code, lines) {
		return lines
	}
	out, ok := format(chroma.Coalesce(lexer), style, code, len(lines))
	if !ok {
		return lines
	}

	return out
}

func (r *Renderer) plainCode(lines []string) []string {
	for i, l := range lines {
		lines[i] = r.st.Code.Render(l)
	}

	return lines
}

// lexer is chroma's lexer for a language name, looked up once.
func (r *Renderer) lexer(lang string) chroma.Lexer {
	if lang == "" {
		return nil
	}
	lexer, ok := r.code.lexers[lang]
	if ok {
		return lexer
	}
	if lexer = lexers.Get(lang); lexer != nil {
		lexer = chroma.Coalesce(lexer)
	}
	if len(r.code.lexers) >= maxLexers {
		clear(r.code.lexers)
	}
	r.code.lexers[lang] = lexer

	return lexer
}

func tooLarge(code string, lines []string) bool {
	if len(code) > maxCodeBytes || len(lines) > maxCodeLines {
		return true
	}
	for _, l := range lines {
		if len(l) > maxLineBytes {
			return true
		}
	}

	return false
}
