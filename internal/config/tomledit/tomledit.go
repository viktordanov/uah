// Package tomledit edits TOML configuration files in place, keeping their
// comments and formatting: it sets or removes one key, or cuts whole
// tables, and writes the file atomically. `uah mcp add` and the TUI's
// /config use it. It works on the bytes with go-toml's parser, so what it
// does not touch stays exactly as written.
package tomledit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	gotoml "github.com/pelletier/go-toml/v2"
)

// ErrNotEditable means the key is written in a form the editor does not
// change, such as inside an inline table or an array of tables.
var ErrNotEditable = errors.New("the key is not written as its own line; edit the file by hand")

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Key quotes a key part that is not a bare TOML key.
func Key(part string) string {
	if bareKey.MatchString(part) {
		return part
	}

	return basicString(part)
}

// Read returns the file and its permissions; a missing file is empty, with
// 0600 for when it is written.
func Read(path string) ([]byte, fs.FileMode, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read %s: %w", path, err)
	}

	return data, info.Mode().Perm(), nil
}

// Write replaces the file atomically with the given permissions, creating
// its directory.
func Write(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	return nil
}

// Set writes key = value, where key is the full path ("tui", "mouse"). An
// existing line is replaced in place; a new key goes after the last key of
// its table, and a missing table is appended. value is a TOML scalar or
// list.
func Set(data []byte, key []string, value any) ([]byte, error) {
	text, err := encode(value)
	if err != nil {
		return nil, err
	}
	doc, err := scan(data)
	if err != nil {
		return nil, err
	}
	if kv, ok := doc.find(key); ok {
		return slices.Concat(data[:kv.eq+1], []byte(" "+text), data[kv.end:]), nil
	}
	if doc.blocked(key) {
		return nil, fmt.Errorf("%s: %w", strings.Join(key, "."), ErrNotEditable)
	}
	table := key[:len(key)-1]
	at, written, ok := doc.insertAt(data, table, key[len(key)-1])
	line := []byte(joinKey(written) + " = " + text + "\n")
	if !ok {
		line = []byte(Key(key[len(key)-1]) + " = " + text + "\n")
		out := bytes.TrimRight(data, "\n")
		if len(out) > 0 {
			out = append(out, "\n\n"...)
		}
		out = append(out, "["+joinKey(table)+"]\n"...)

		return append(out, line...), nil
	}
	if at > 0 && data[at-1] != '\n' {
		line = append([]byte("\n"), line...)
	}

	return slices.Concat(data[:at], line, data[at:]), nil
}

// Unset removes the key's line. ok is false when the file does not set it.
func Unset(data []byte, key []string) (out []byte, ok bool, err error) {
	doc, err := scan(data)
	if err != nil {
		return nil, false, err
	}
	kv, found := doc.find(key)
	if !found {
		if doc.blocked(key) {
			return nil, false, fmt.Errorf("%s: %w", strings.Join(key, "."), ErrNotEditable)
		}

		return data, false, nil
	}
	start := bytes.LastIndexByte(data[:kv.start], '\n') + 1

	return slices.Concat(data[:start], data[lineEnd(data, kv.end):]), true, nil
}

// CutTables removes every table whose header starts with prefix, such as
// [mcp_servers.docs] and [mcp_servers.docs.env] for ("mcp_servers",
// "docs"). Each runs from its header line to the next header, less the
// blank and comment lines right before that header, which belong to what
// follows. cut reports whether there was one.
func CutTables(data []byte, prefix ...string) (out []byte, cut bool, err error) {
	doc, err := scan(data)
	if err != nil {
		return nil, false, err
	}
	var spans [][2]int
	for i, t := range doc.tables {
		if len(t.keys) < len(prefix) || !slices.Equal(t.keys[:len(prefix)], prefix) {
			continue
		}
		end := len(data)
		if i+1 < len(doc.tables) {
			end = trimTail(data, doc.tables[i+1].start)
		}
		// A block uah config update added after the table stays; a key of the
		// table below it goes too, as comments do not end a table.
		block := doc.updateBlock(data[:end], t.start)
		spans = append(spans, [2]int{t.start, block})
		if block == end {
			continue // no block in the table: nothing below it to cut
		}
		for _, kv := range doc.values {
			if kv.table == i && kv.start >= block && kv.start < end {
				spans = append(spans, [2]int{bytes.LastIndexByte(data[:kv.start], '\n') + 1, lineEnd(data, kv.end)})
			}
		}
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })
	for _, sp := range slices.Backward(spans) {
		data = append(data[:sp[0]:sp[0]], data[sp[1]:]...)
	}

	return data, len(spans) > 0, nil
}

// encode is value as TOML: a string as a basic string, as uah's files and
// Codex's write them, anything else as go-toml writes it.
func encode(value any) (string, error) {
	if s, ok := value.(string); ok {
		return basicString(s), nil
	}
	var buf bytes.Buffer
	enc := gotoml.NewEncoder(&buf).SetTablesInline(true).SetArraysMultiline(false)
	if err := enc.Encode(map[string]any{"v": value}); err != nil {
		return "", fmt.Errorf("failed to encode the value: %w", err)
	}
	text, ok := strings.CutPrefix(strings.TrimRight(buf.String(), "\n"), "v = ")
	if !ok {
		return "", fmt.Errorf("failed to encode %v as a TOML value", value)
	}

	return text, nil
}

// basicString quotes s as a TOML basic string.
func basicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')

	return b.String()
}

func joinKey(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		quoted = append(quoted, Key(p))
	}

	return strings.Join(quoted, ".")
}

// lineEnd is the offset just after the newline that ends the line at off,
// or the end of data.
func lineEnd(data []byte, off int) int {
	if i := bytes.IndexByte(data[off:], '\n'); i >= 0 {
		return off + i + 1
	}

	return len(data)
}

// trimTail moves end back over the blank and comment lines before it,
// then forward again over the blank ones among them: the comments stay
// with the header they precede, and the section's trailing blank lines go
// with it.
func trimTail(data []byte, end int) int {
	blank := func(line string) bool { return strings.TrimSpace(line) == "" }
	for end > 0 {
		prev := bytes.LastIndexByte(data[:end-1], '\n') + 1
		if line := strings.TrimSpace(string(data[prev:end])); line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		end = prev
	}
	for end < len(data) {
		next := bytes.IndexByte(data[end:], '\n')
		if next < 0 || !blank(string(data[end:end+next])) {
			break
		}
		end += next + 1
	}

	return end
}
