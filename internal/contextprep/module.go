package contextprep

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Module limits: a module file, its front matter, and its lists.
const (
	MaxModuleBytes      = 16 << 10
	MaxFrontMatterBytes = 2 << 10
	maxListItems        = 16
	maxCheckArgBytes    = 256
)

// Module is one Markdown module of prepared context: front matter that
// says when it applies, and the text it adds. The text is data: it is
// rendered with the session's placeholders and never run.
type Module struct {
	// Path names the module: "environment/fish" for a built-in,
	// "library/go" for a library module, "context.d/<id>" for a user's or a
	// project's.
	Path string
	// Source is where it comes from.
	Source Source
	// File is the file it was read from, or "" for an embedded one.
	File string
	// Overrides is true for a user file that replaces a built-in.
	Overrides bool
	Meta      FrontMatter
	// Body is the text after the front matter, trimmed.
	Body string
	// Raw is the whole file, for trust and for `uah prompts show`.
	Raw string
	// Err is why the module cannot be used: the file does not parse, or
	// its id is taken. Such a module is listed but never applies.
	Err error
}

// Source is where a module comes from.
type Source string

// The sources.
const (
	SourceBuiltin Source = "builtin"
	SourceLibrary Source = "library"
	SourceUser    Source = "user"
	SourceProject Source = "project"
)

// FrontMatter is a module's preamble, a strict schema: an unknown key is
// an error.
type FrontMatter struct {
	// ID names the module; it is the file's base name without .md.
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	// When lists the facts the session must have; an empty list matches
	// any value.
	When When `yaml:"when"`
	// Check is a command, as argv, that must exit 0 for the module to
	// apply. It runs without a shell, in the read-only sandbox with no
	// network.
	Check []string `yaml:"check"`
	// Files are workspace paths, one of which must exist; * matches
	// within one path element.
	Files []string `yaml:"files"`
	// Enabled is false for a module that applies only when the
	// configuration enables it (the library's); true by default.
	Enabled *bool `yaml:"enabled"`
}

// When is the facts a module needs.
type When struct {
	// Shell is the shell's family: bash, zsh, sh, fish, nu, xonsh,
	// elvish, pwsh, cmd, csh, or other.
	Shell []string `yaml:"shell"`
	// ShellPath is the shell's exact path, such as /bin/bash.
	ShellPath []string `yaml:"shell_path"`
	// OS is runtime.GOOS: darwin, linux, freebsd, windows, ….
	OS []string `yaml:"os"`
	// Sandbox is read-only, workspace-write, or none.
	Sandbox []string `yaml:"sandbox"`
	// Network is whether sandboxed commands have the network.
	Network *bool `yaml:"network"`
	// Agent is main or subagent.
	Agent []string `yaml:"agent"`
	// Instructions is whether instruction files were loaded.
	Instructions *bool `yaml:"instructions"`
	// InstructionsOmitted is whether the system prompt leaves the
	// instruction files out on purpose.
	InstructionsOmitted *bool `yaml:"instructions_omitted"`
	// InstructionsOff is whether loading instruction files is turned off
	// (--no-instructions, or instructions.enabled = false).
	InstructionsOff *bool `yaml:"instructions_off"`
}

// Keys of When and placeholders that share a name.
const (
	keyShell     = "shell"
	keySandbox   = "sandbox"
	keyAgent     = "agent"
	keyWorkspace = "workspace"
)

// Shells are the shell families When.Shell names.
var Shells = []string{famBash, "zsh", "sh", "fish", "nu", "xonsh", "elvish", keyPwsh, famCmd, famCsh, "other"}

// Placeholders are the names a module's text may use as {{name}}, each
// replaced with the session's value as plain text.
var Placeholders = []string{
	keyShell, "shell_name", "os", "goos", "mode", "tmpdir", keyWorkspace, keyAgent,
	"instruction_files", "omitted_instruction_files", "max_output_length",
}

var (
	idPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	osPattern   = regexp.MustCompile(`^[a-z0-9]{1,20}$`)
	placeholder = regexp.MustCompile(`\{\{([a-z_]*)\}\}`)
)

// ParseModule reads a module file: front matter between --- lines, then
// the text. name is the file's base name without .md, which the id must
// be.
func ParseModule(name string, data []byte) (FrontMatter, string, error) {
	if len(data) > MaxModuleBytes {
		return FrontMatter{}, "", fmt.Errorf("the module is %d bytes, over %d", len(data), MaxModuleBytes)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return FrontMatter{}, "", errors.New("the module does not start with front matter (a --- line)")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		if head, ok = strings.CutSuffix(rest, "\n---"); !ok {
			return FrontMatter{}, "", errors.New("the front matter has no closing --- line")
		}
		body = ""
	}
	if len(head) > MaxFrontMatterBytes {
		return FrontMatter{}, "", fmt.Errorf("the front matter is %d bytes, over %d", len(head), MaxFrontMatterBytes)
	}
	var fm FrontMatter
	dec := yaml.NewDecoder(bytes.NewReader([]byte(head)))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil && !errors.Is(err, io.EOF) {
		return FrontMatter{}, "", fmt.Errorf("the front matter: %w", err)
	}
	body = strings.TrimSpace(body)
	if err := fm.validate(name); err != nil {
		return FrontMatter{}, "", err
	}
	if err := checkPlaceholders(body); err != nil {
		return FrontMatter{}, "", err
	}

	return fm, body, nil
}

// validate checks the front matter's values.
func (fm FrontMatter) validate(name string) error {
	switch {
	case !idPattern.MatchString(fm.ID):
		return fmt.Errorf("id %q is not lower-case letters, digits, - and _ (at most 40)", fm.ID)
	case fm.ID != name:
		return fmt.Errorf("id %q is not the file's name, %s", fm.ID, name)
	case strings.TrimSpace(fm.Description) == "" || len(fm.Description) > 200:
		return errors.New("description must be 1 to 200 characters")
	}
	if err := fm.When.validate(); err != nil {
		return err
	}
	if err := validateCheck(fm.Check); err != nil {
		return err
	}

	return validateFiles(fm.Files)
}

func (w When) validate() error {
	for _, l := range []struct {
		key    string
		values []string
		ok     func(string) bool
	}{
		{keyShell, w.Shell, func(v string) bool { return slices.Contains(Shells, v) }},
		{"shell_path", w.ShellPath, func(v string) bool { return path.IsAbs(v) && path.Clean(v) == v }},
		{"os", w.OS, osPattern.MatchString},
		{keySandbox, w.Sandbox, func(v string) bool { return slices.Contains([]string{"read-only", "workspace-write", "none"}, v) }},
		{keyAgent, w.Agent, func(v string) bool { return v == "main" || v == "subagent" }},
	} {
		if len(l.values) > maxListItems {
			return fmt.Errorf("when.%s has more than %d values", l.key, maxListItems)
		}
		for _, v := range l.values {
			if !l.ok(v) {
				return fmt.Errorf("when.%s: %q is not a known value", l.key, v)
			}
		}
	}

	return nil
}

// validateCheck allows an argv of plain words: a command name found on
// PATH, or an absolute path, and its arguments, which are passed as they
// are, never to a shell.
func validateCheck(argv []string) error {
	if argv == nil {
		return nil
	}
	if len(argv) == 0 || len(argv) > maxListItems {
		return fmt.Errorf("check must have 1 to %d words", maxListItems)
	}
	for _, a := range argv {
		if len(a) > maxCheckArgBytes || strings.ContainsRune(a, 0) {
			return fmt.Errorf("check word %q is too long or has a NUL", a)
		}
	}
	if cmd := argv[0]; cmd == "" || (strings.Contains(cmd, "/") && !path.IsAbs(cmd)) {
		return fmt.Errorf("check command %q must be a name on PATH or an absolute path", cmd)
	}

	return nil
}

// validateFiles allows relative paths inside the workspace, with * as the
// only pattern.
func validateFiles(files []string) error {
	if len(files) > maxListItems {
		return fmt.Errorf("files has more than %d paths", maxListItems)
	}
	for _, f := range files {
		switch {
		case f == "" || path.IsAbs(f) || strings.Contains(f, `\`):
			return fmt.Errorf("files: %q must be a relative path", f)
		case path.Clean(f) != f || f == ".." || strings.HasPrefix(f, "../") || slices.Contains(strings.Split(f, "/"), ".."):
			return fmt.Errorf("files: %q must stay inside the workspace (no ..)", f)
		case strings.ContainsAny(f, "?[]{}") || strings.Contains(f, "**"):
			return fmt.Errorf("files: %q may use only * as a pattern", f)
		}
	}

	return nil
}

// checkPlaceholders requires every {{ in the text to start a known
// placeholder.
func checkPlaceholders(body string) error {
	for i := 0; ; {
		j := strings.Index(body[i:], "{{")
		if j < 0 {
			return nil
		}
		i += j
		m := placeholder.FindStringSubmatch(body[i:])
		if m == nil || !strings.HasPrefix(body[i:], m[0]) || !slices.Contains(Placeholders, m[1]) {
			end := min(len(body), i+24)

			return fmt.Errorf("unknown placeholder at %q: the known ones are {{%s}}", body[i:end], strings.Join(Placeholders, "}}, {{"))
		}
		i += len(m[0])
	}
}

// render replaces each placeholder with its value, in one pass, so a value
// is never read as a placeholder. It reports the first placeholder with no
// value.
func render(body string, vars map[string]string) (string, error) {
	var b strings.Builder
	for i := 0; ; {
		j := strings.Index(body[i:], "{{")
		if j < 0 {
			b.WriteString(body[i:])

			return b.String(), nil
		}
		b.WriteString(body[i : i+j])
		i += j
		m := placeholder.FindStringSubmatch(body[i:])
		if m == nil || !strings.HasPrefix(body[i:], m[0]) {
			return "", fmt.Errorf("unknown placeholder at %q", body[i:min(len(body), i+24)])
		}
		v := vars[m[1]]
		if v == "" {
			return "", fmt.Errorf("{{%s}} has no value in this session", m[1])
		}
		b.WriteString(v)
		i += len(m[0])
	}
}
