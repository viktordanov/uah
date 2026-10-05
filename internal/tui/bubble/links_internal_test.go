package bubble

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// TestEditorAt: each known editor gets the line its way, a windowed one
// drops --wait and starts on its own, and an unknown one gets the path.
func TestEditorAt(t *testing.T) {
	const p = "/w/a.go"
	cases := []struct {
		editor []string
		line   int
		want   []string
		window bool
	}{
		{[]string{"vim"}, 12, []string{"vim", "+12", p}, false},
		{[]string{"/usr/bin/nvim", "-f"}, 12, []string{"/usr/bin/nvim", "-f", "+12", p}, false},
		{[]string{"vi"}, 3, []string{"vi", "+3", p}, false},
		{[]string{"nano"}, 12, []string{"nano", "+12", p}, false},
		{[]string{"emacs", "-nw"}, 12, []string{"emacs", "-nw", "+12", p}, false},
		{[]string{"emacsclient", "-t"}, 12, []string{"emacsclient", "-t", "+12", p}, false},
		{[]string{"micro"}, 12, []string{"micro", "+12", p}, false},
		{[]string{"hx"}, 12, []string{"hx", p + ":12"}, false},
		{[]string{"helix"}, 12, []string{"helix", p + ":12"}, false},
		{[]string{"code", "--wait"}, 12, []string{"code", "--goto", p + ":12"}, true},
		{[]string{"cursor", "-w"}, 12, []string{"cursor", "--goto", p + ":12"}, true},
		{[]string{"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code"}, 7, []string{"/Applications/Visual Studio Code.app/Contents/Resources/app/bin/code", "--goto", p + ":7"}, true},
		{[]string{"zed", "--wait"}, 12, []string{"zed", p + ":12"}, true},
		{[]string{"subl", "-w"}, 12, []string{"subl", p + ":12"}, true},
		{[]string{"code"}, 0, []string{"code", p}, true},
		{[]string{"vim"}, 0, []string{"vim", p}, false},
		{[]string{"ed"}, 12, []string{"ed", p}, false},
		{[]string{"my-editor", "--flag"}, 12, []string{"my-editor", "--flag", p}, false},
	}
	for _, tc := range cases {
		got, window := editorAt(tc.editor, p, tc.line)
		assert.Equal(t, tc.want, got, "%v", tc.editor)
		assert.Equal(t, tc.window, window, "%v", tc.editor)
	}
	editor := []string{"code", "--wait"}
	editorAt(editor, p, 1)
	assert.Equal(t, []string{"code", "--wait"}, editor, "the command is not changed")
}

// TestFileOpenedAfterIdle: a toast after the clock sat still is timed
// from now, not from the last tick, so it does not end at once.
func TestFileOpenedAfterIdle(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	later := t0.Add(time.Minute)
	m := New(context.Background(), Deps{Now: func() time.Time { return later }})
	m.st.Now = t0 // the last tick, before the TUI sat idle
	next, _ := m.Update(state.FileOpened{Link: state.FileLink{Path: "/w/a.go"}, With: "code"})
	toast := next.(Model).st.Toast //nolint:forcetypeassert // Update returns a Model
	require.NotNil(t, toast)
	assert.Equal(t, later.Add(2*time.Second), toast.Until)
}

// TestRunnable: the opener gets no file it would run.
func TestRunnable(t *testing.T) {
	dir := t.TempDir()
	for name, mode := range map[string]os.FileMode{"notes.md": 0o600, "run.sh": 0o700, "x.command": 0o600, "app.desktop": 0o644, "X.COMMAND": 0o600} {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("x"), mode))
		err := runnable(p)
		if name == "notes.md" {
			assert.NoError(t, err)
		} else {
			assert.ErrorIs(t, err, errRunnable, name)
		}
	}
}

// TestResolveLinks: a message's words are links when they lead to a
// regular file in the workspace: relative, absolute, under ~ when home is
// in it, through a symlink that stays in it, with a line; not a missing
// file, a directory, a file outside, or a symlink out of it.
func TestResolveLinks(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	outside := filepath.Join(root, "outside.go")
	write := func(p, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte(text), 0o600))
	}
	write(filepath.Join(ws, "a.go"), "package a\n")
	write(filepath.Join(ws, "pkg", "b.go"), "package b\n")
	write(outside, "package out\n")
	require.NoError(t, os.Symlink(filepath.Join(ws, "a.go"), filepath.Join(ws, "in.go")))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "out.go")))
	words := []string{"a.go", "pkg/b.go:3", ws + "/a.go", "~/pkg/b.go", "in.go", "out.go", outside, "missing.go", "pkg", "../outside.go"}
	got := resolveLinks(state.EffResolveLinks{Workspace: ws, Home: ws, Messages: []state.LinkWords{{Key: "k", Text: "t", Words: words}}})
	require.Len(t, got.Messages, 1)
	assert.Equal(t, map[string]state.FileLink{
		"a.go":       {Path: filepath.Join(ws, "a.go")},
		"pkg/b.go:3": {Path: filepath.Join(ws, "pkg/b.go"), Line: 3},
		ws + "/a.go": {Path: filepath.Join(ws, "a.go")},
		"~/pkg/b.go": {Path: filepath.Join(ws, "pkg/b.go")},
		"in.go":      {Path: filepath.Join(ws, "in.go")},
	}, got.Messages[0].Links)
	assert.Equal(t, "k", got.Messages[0].Key)
	assert.Equal(t, "t", got.Messages[0].Text)
}

// TestReadPeek: a file is read for the overlay with its control characters
// made harmless; a binary file, a directory, a missing file, a FIFO, a
// device, and a file too large say so; long lines are cut.
func TestReadPeek(t *testing.T) {
	dir := t.TempDir()
	styles := render.NewStyles(render.Amber)
	read := func(name, text string) state.PeekLoaded {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(text), 0o600))

		return readPeek(state.FileLink{Path: p}, styles, 20)
	}

	got := read("a.txt", "one\r\ntwo\x1b]52;c;aGk=\x07\tend\n")
	assert.Equal(t, []string{"one", "two�]52;c;aGk=�\tend"}, got.Lines, "no escape reaches the terminal")
	assert.Empty(t, got.Note)
	assert.Equal(t, 20, got.Rows)

	got = read("bin", "abc\x00def")
	assert.Equal(t, "binary file, not shown", got.Note)
	assert.Nil(t, got.Lines)

	got = read("bad.txt", "ok \xff\xfe\n")
	assert.Equal(t, []string{"ok �"}, got.Lines, "invalid UTF-8")

	got = read("long.txt", strings.Repeat("é", maxPeekLineBytes)+"\n")
	assert.True(t, strings.HasSuffix(got.Lines[0], "…"))
	assert.LessOrEqual(t, len(got.Lines[0]), maxPeekLineBytes+len("…"))

	got = read("many.txt", strings.Repeat("x\n", maxPeekLines+5))
	assert.Len(t, got.Lines, maxPeekLines)
	assert.Contains(t, got.Note, "showing the first 20000 lines")

	got = read("big.txt", strings.Repeat(strings.Repeat("y", 199)+"\n", maxPeekBytes/200+10))
	assert.Contains(t, got.Note, "the file is larger")
	assert.Len(t, got.Lines, maxPeekBytes/200, "cut at the last whole line")

	got = read("code.go", "package a\n\nfunc A() {}\n")
	assert.Contains(t, got.Lines[0], "\x1b[", "highlighted by its name")

	peek := func(p string) state.PeekLoaded { return readPeek(state.FileLink{Path: p}, styles, 20) }
	assert.Contains(t, peek(dir).Note, "not a regular file", "a directory")
	assert.Contains(t, peek(filepath.Join(dir, "missing")).Note, "no such file")
	fifo := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	assert.Contains(t, peek(fifo).Note, "not a regular file", "a FIFO does not block the read")
	assert.Contains(t, peek("/dev/null").Note, "a device or system file")
	link := filepath.Join(dir, "null")
	require.NoError(t, os.Symlink("/dev/zero", link))
	assert.Contains(t, peek(link).Note, "a device or system file", "a symlink to a device")
	inner := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(filepath.Join(dir, "a.txt"), inner))
	assert.Equal(t, "one", peek(inner).Lines[0], "a symlink to a file")
}
