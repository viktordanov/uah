package clipboard_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/images/clipboard"
)

func reader(calls *[]string, goos string, tools []string, env map[string]string) clipboard.TextReader {
	return clipboard.TextReader{
		Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
			*calls = append(*calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))

			return []byte("pasted"), nil
		},
		GOOS: goos, Getenv: func(k string) string { return env[k] },
		LookPath: func(name string) (string, error) {
			for _, t := range tools {
				if t == name {
					return "/usr/bin/" + name, nil
				}
			}

			return "", errors.New("not found")
		},
	}
}

func TestReadText_PicksTheSystemTool(t *testing.T) {
	cases := []struct {
		name, goos string
		tools      []string
		env        map[string]string
		want       string
	}{
		{"macOS", "darwin", []string{"pbpaste"}, nil, "pbpaste"},
		{"Wayland", "linux", []string{"wl-paste", "xclip"}, map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, "wl-paste --no-newline --type text/plain"},
		{"X11", "linux", []string{"wl-paste", "xclip"}, map[string]string{"DISPLAY": ":0"}, "xclip -selection clipboard -o"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			text, err := reader(&calls, c.goos, c.tools, c.env).ReadText(t.Context())
			require.NoError(t, err)
			assert.Equal(t, "pasted", text)
			assert.Equal(t, []string{c.want}, calls)
		})
	}
}

func TestReadText_WithoutATool(t *testing.T) {
	var calls []string
	_, err := reader(&calls, "linux", nil, map[string]string{"DISPLAY": ":0"}).ReadText(t.Context())
	require.ErrorIs(t, err, clipboard.ErrNoReadTool)
	assert.Empty(t, calls)
}
