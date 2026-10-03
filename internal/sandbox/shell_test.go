package sandbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
)

func TestShellFullAccessIsTheRealShell(t *testing.T) {
	got, err := sandbox.Shell(t.TempDir(), sandbox.Policy{Mode: sandbox.FullAccess}, sandbox.EnvPolicy{}, "/bin/zsh")
	require.NoError(t, err)
	assert.Equal(t, "/bin/zsh", got)
}

func TestShellScript(t *testing.T) {
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: t.TempDir()}
	if _, err := p.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	dir := t.TempDir()
	first, err := sandbox.Shell(dir, p, sandbox.EnvPolicy{}, "/bin/sh")
	require.NoError(t, err)
	again, err := sandbox.Shell(dir, p, sandbox.EnvPolicy{}, "/bin/sh")
	require.NoError(t, err)
	assert.Equal(t, first, again, "the same policy reuses its script")
	info, err := os.Stat(first)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary files are left")
	assert.Equal(t, filepath.Dir(first), dir)
}

func TestShellEnvPolicy(t *testing.T) {
	t.Setenv("UAH_TEST_TOKEN", "secret-value")
	t.Setenv("UAH_TEST_PLAIN", "plain value")
	no := false
	env := sandbox.EnvPolicy{IgnoreDefaultExcludes: &no, Set: map[string]string{"UAH_TEST_SET": "it's set"}}
	path, err := sandbox.Shell(t.TempDir(), sandbox.Policy{Mode: sandbox.FullAccess}, env, "/bin/sh")
	require.NoError(t, err)
	script, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(script), "secret-value", "inherited values are not written to disk")
	assert.NotContains(t, string(script), "UAH_TEST_TOKEN")

	out, err := exec.Command(path, "-c", `printf '%s|%s|%s' "$UAH_TEST_TOKEN" "$UAH_TEST_PLAIN" "$UAH_TEST_SET"`).Output()
	require.NoError(t, err)
	assert.Equal(t, "|plain value|it's set", string(out))
}

// TestShellTempDir checks that every mode's script sets the temporary
// directory, over the environment policy, and creates it.
func TestShellTempDir(t *testing.T) {
	temp := filepath.Join(t.TempDir(), "session", "tmp")
	scripts, ws := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", "/somewhere/else")
	p := sandbox.Policy{Mode: sandbox.FullAccess, Workspace: ws, TempDir: temp}
	print := `printf '%s|%s|%s|%s' "$TMPDIR" "$TMP" "$TEMP" "$TMPPREFIX"`
	want := temp + "|" + temp + "|" + temp + "|" + filepath.Join(temp, "zsh")
	for _, env := range []sandbox.EnvPolicy{
		{},
		{Inherit: sandbox.InheritNone, Set: map[string]string{"TMPDIR": "/set/by/policy"}},
		{IncludeOnly: []string{"PATH"}},
	} {
		path, err := sandbox.Shell(scripts, p, env, "/bin/sh")
		require.NoError(t, err)
		assert.NotEqual(t, "/bin/sh", path, "full access with a temp dir still needs a script")
		out, err := exec.Command(path, "-c", print).Output()
		require.NoError(t, err)
		assert.Equal(t, want, string(out), "%+v", env)
	}
	info, err := os.Stat(temp)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// TestShellReplacesAChangedScript checks that a script already in the
// directory is used only when it is still the one Shell writes: a changed
// script, a symlink, or a script others can write is replaced, and the
// directory is made private again.
func TestShellReplacesAChangedScript(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sandbox")
	p := sandbox.Policy{Mode: sandbox.FullAccess, TempDir: filepath.Join(t.TempDir(), "tmp")}
	path, err := sandbox.Shell(dir, p, sandbox.EnvPolicy{}, "/bin/sh")
	require.NoError(t, err)
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	evil := []byte("#!/bin/sh\nexec /bin/sh \"$@\"\n")
	other := filepath.Join(t.TempDir(), "evil")
	require.NoError(t, os.WriteFile(other, evil, 0o700))

	for name, tamper := range map[string]func(){
		"changed content": func() { require.NoError(t, os.WriteFile(path, evil, 0o700)) },
		"symlink": func() {
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Symlink(other, path))
		},
		"writable by others": func() { require.NoError(t, os.Chmod(path, 0o777)) },
		"open directory":     func() { require.NoError(t, os.Chmod(dir, 0o777)) },
	} {
		tamper()
		again, err := sandbox.Shell(dir, p, sandbox.EnvPolicy{}, "/bin/sh")
		require.NoError(t, err, name)
		assert.Equal(t, path, again, name)
		info, err := os.Lstat(again)
		require.NoError(t, err, name)
		assert.True(t, info.Mode().IsRegular(), name)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), name)
		got, err := os.ReadFile(again)
		require.NoError(t, err, name)
		assert.Equal(t, string(want), string(got), name)
		info, err = os.Stat(dir)
		require.NoError(t, err, name)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), name)
	}
	data, err := os.ReadFile(other)
	require.NoError(t, err)
	assert.Equal(t, evil, data, "the symlink's target is left alone")
}
