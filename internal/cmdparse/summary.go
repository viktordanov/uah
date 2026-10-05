package cmdparse

import (
	"path"
	"path/filepath"
	"strings"
)

// Style is how a part of a summary is drawn.
type Style int

const (
	// Plain is the terminal's own text: what the command reads, lists,
	// searches, or runs.
	Plain Style = iota
	// Dim is the words between them: separators, line ranges, filters.
	Dim
	// Faint is secondary detail, such as a heredoc's body.
	Faint
)

// Part is a run of a summary's text in one style. A file a command reads
// also has its Path, absolute when the Env has a workspace, and the line
// range it reads ("1-360"), so the TUI can link it.
type Part struct {
	Text  string
	Style Style
	Path  string
	Lines string
}

// Summary is how the TUI shows a command: a label for the tool column
// (READ, LIST, SEARCH, or "" for a command it only ran) and the text after
// it.
type Summary struct {
	Label string
	Parts []Part
}

// Text is the summary's text without styles.
func (s Summary) Text() string {
	var b strings.Builder
	for _, p := range s.Parts {
		b.WriteString(p.Text)
	}

	return b.String()
}

// The labels of the kinds a summary can have.
const (
	LabelRead   = "READ"
	LabelList   = "LIST"
	LabelSearch = "SEARCH"
)

// Summarize says what a command does, for its tool line: its wrappers
// stripped (Strip), a heredoc folded, and then, when every command in it
// reads, every one lists, or every one searches (Codex's parse), what it
// reads, lists, or searches for; otherwise the command, on one line, with
// its paths relative (Relative).
func Summarize(command string, env Env) Summary {
	script := Strip(command)
	if head, body, ok := heredoc(script); ok {
		return Summary{Parts: []Part{{Text: oneLine(Relative(head, env)) + " ", Style: Plain}, {Text: Relative(body, env), Style: Faint}}}
	}
	parsed := ParseScript(script)
	if kind, ok := sameKind(parsed); ok {
		switch kind {
		case Read:
			return Summary{Label: LabelRead, Parts: readParts(parsed, env)}
		case ListFiles:
			return Summary{Label: LabelList, Parts: listParts(parsed, env)}
		case Search:
			return Summary{Label: LabelSearch, Parts: searchParts(parsed, env)}
		case Unknown:
		}
	}

	return Summary{Parts: []Part{{Text: oneLine(Relative(script, env)), Style: Plain}}}
}

// sameKind is the kind all the parsed commands share, when they do.
func sameKind(parsed []Parsed) (Kind, bool) {
	if len(parsed) == 0 {
		return Unknown, false
	}
	for _, p := range parsed[1:] {
		if p.Kind != parsed[0].Kind {
			return Unknown, false
		}
	}

	return parsed[0].Kind, true
}

// readParts are the files read, with their line ranges; a file in the
// same directory as the one before shows only its name.
func readParts(parsed []Parsed, env Env) []Part {
	var parts []Part
	prevDir := ""
	for i, p := range parsed {
		if i > 0 {
			parts = append(parts, Part{Text: ", ", Style: Dim})
		}
		file := Relative(p.Path, env)
		shown := file
		if dir := path.Dir(file); i > 0 && dir == prevDir && dir != "." {
			shown = path.Base(file)
		}
		prevDir = path.Dir(file)
		parts = append(parts, Part{Text: shown, Style: Plain, Path: Absolute(p.Path, env), Lines: p.Lines})
		if p.Lines != "" {
			parts = append(parts, Part{Text: ":" + p.Lines, Style: Dim})
		}
	}

	return parts
}

// listParts are the directories listed and their filters.
func listParts(parsed []Parsed, env Env) []Part {
	var parts []Part
	for i, p := range parsed {
		if i > 0 {
			parts = append(parts, Part{Text: ", ", Style: Dim})
		}
		parts = append(parts, Part{Text: paths(p.Paths, env), Style: Plain})
		if len(p.Globs) > 0 {
			parts = append(parts, Part{Text: "  " + strings.Join(p.Globs, " "), Style: Dim})
		}
	}

	return parts
}

// searchParts are each pattern and where it is searched for.
func searchParts(parsed []Parsed, env Env) []Part {
	var parts []Part
	for i, p := range parsed {
		if i > 0 {
			parts = append(parts, Part{Text: ", ", Style: Dim})
		}
		parts = append(parts, Part{Text: p.Query, Style: Plain})
		if len(p.Paths) > 0 {
			parts = append(parts, Part{Text: " in ", Style: Dim}, Part{Text: paths(p.Paths, env), Style: Plain})
		}
	}

	return parts
}

// paths are paths relative to the workspace, "." for none.
func paths(ps []string, env Env) string {
	if len(ps) == 0 {
		return "."
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = strings.TrimSuffix(Relative(p, env), "/")
		if out[i] == "" {
			out[i] = "."
		}
	}

	return strings.Join(out, ", ")
}

// Absolute is p made absolute: under the home directory for ~ and ~/, and
// under the workspace when relative. Without the directory it needs, p
// comes back as it is.
func Absolute(p string, env Env) string {
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		if env.Home == "" {
			return p
		}

		return filepath.Join(env.Home, p[1:])
	case filepath.IsAbs(p) || env.Workspace == "":
		return filepath.Clean(p)
	}

	return filepath.Join(env.Workspace, p)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
