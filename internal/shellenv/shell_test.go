package shellenv_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/shellenv"
)

// script writes an executable file and returns its path.
func script(t *testing.T, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"), mode))

	return p
}

func TestResolve(t *testing.T) {
	envShell := script(t, "envsh", 0o755)
	loginShell := script(t, "loginsh", 0o755)
	notExec := script(t, "plain", 0o644)
	missing := filepath.Join(t.TempDir(), "missing")
	dir := t.TempDir()
	env := func(shell string) func(string) string {
		return func(k string) string {
			switch k {
			case "SHELL":
				return shell
			case "PATH":
				return "relative:" + filepath.Dir(envShell)
			}

			return ""
		}
	}
	login := func(s string) func() string { return func() string { return s } }

	for _, c := range []struct {
		name  string
		shell string
		login string
		want  shellenv.Shell
	}{
		{"SHELL set", envShell, loginShell, shellenv.Shell{Path: envShell, Source: shellenv.FromEnv}},
		{"SHELL with spaces", " " + envShell + "\n", loginShell, shellenv.Shell{Path: envShell, Source: shellenv.FromEnv}},
		{"SHELL unset", "", loginShell, shellenv.Shell{Path: loginShell, Source: shellenv.FromLogin}},
		{"SHELL blank", "  ", loginShell, shellenv.Shell{Path: loginShell, Source: shellenv.FromLogin}},
		{"SHELL missing", missing, loginShell, shellenv.Shell{Path: loginShell, Source: shellenv.FromLogin, Env: missing}},
		{"SHELL not executable", notExec, loginShell, shellenv.Shell{Path: loginShell, Source: shellenv.FromLogin, Env: notExec}},
		{"SHELL a name on PATH", "envsh", loginShell, shellenv.Shell{Path: envShell, Source: shellenv.FromEnv}},
		{"SHELL a name not on PATH", "nosuchsh", loginShell, shellenv.Shell{Path: loginShell, Source: shellenv.FromLogin, Env: "nosuchsh"}},
		{"SHELL relative", "./envsh", loginShell, shellenv.Shell{Path: loginShell, Source: shellenv.FromLogin, Env: "./envsh"}},
		{"SHELL a directory", dir, "", shellenv.Shell{Path: shellenv.Default, Source: shellenv.FromDefault, Env: dir}},
		{"no login shell", "", "", shellenv.Shell{Path: shellenv.Default, Source: shellenv.FromDefault}},
		{"login shell missing", "", missing, shellenv.Shell{Path: shellenv.Default, Source: shellenv.FromDefault}},
		{"login shell not executable", "", notExec, shellenv.Shell{Path: shellenv.Default, Source: shellenv.FromDefault}},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, shellenv.Resolve(env(c.shell), login(c.login)))
		})
	}
}

func TestResolveSymlink(t *testing.T) {
	target := script(t, "real", 0o755)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))
	got := shellenv.Resolve(func(string) string { return link }, func() string { return "" })
	assert.Equal(t, shellenv.Shell{Path: link, Source: shellenv.FromEnv}, got, "a link to an executable is kept as written")
}

func TestPasswdShell(t *testing.T) {
	p := filepath.Join(t.TempDir(), "passwd")
	require.NoError(t, os.WriteFile(p, []byte(`# a comment
root:x:0:0:root:/root:/bin/bash
+:::::::
broken:x:1000
alice:x:1000:1000:Alice,,,:/home/alice:/usr/bin/fish
bob:x:1001:1001::/home/bob:
`), 0o600))

	s, err := shellenv.PasswdShell(p, 1000)
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin/fish", s)

	s, err = shellenv.PasswdShell(p, 0)
	require.NoError(t, err)
	assert.Equal(t, "/bin/bash", s)

	s, err = shellenv.PasswdShell(p, 1001)
	require.NoError(t, err)
	assert.Empty(t, s, "an empty shell field")

	_, err = shellenv.PasswdShell(p, 4242)
	require.Error(t, err, "no entry")

	_, err = shellenv.PasswdShell(filepath.Join(t.TempDir(), "none"), 0)
	require.Error(t, err)
}

// TestLogin reads the real user database: the login shell is an absolute
// path where the system gives one for the user, and "" on Windows.
func TestLogin(t *testing.T) {
	s := shellenv.Login()
	switch runtime.GOOS {
	case "windows":
		assert.Empty(t, s)
	default:
		if s == "" {
			t.Skip("the user database is not reachable here (a sandbox) or has no entry for the user")
		}
		assert.True(t, filepath.IsAbs(s), "the login shell: %q", s)
	}
	assert.Equal(t, s, shellenv.Login(), "looked up once")
}
