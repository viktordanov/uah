//go:build darwin

package sandbox_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
)

// run runs /bin/sh -c script under the policy with extra environment and
// returns its exit code and combined output. A non-nil stdout replaces the
// output capture for stdout, as the runner passes its own files.
func run(t *testing.T, p sandbox.Policy, script string, env []string, stdout *os.File) (int, string) {
	t.Helper()
	argv, err := p.Wrap([]string{"/bin/sh", "-c", script})
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	cmd.Dir = p.Workspace
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if stdout != nil {
		cmd.Stdout = stdout
	}
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), out.String()
	}
	require.NoError(t, err)

	return 0, out.String()
}

// workspace returns a resolved temp workspace with src/ and .git/config.
func workspace(t *testing.T, withGit bool) string {
	t.Helper()
	ws, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(ws, "src"), 0o755))
	if withGit {
		require.NoError(t, os.Mkdir(filepath.Join(ws, ".git"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(ws, ".git", "config"), []byte("[core]\n"), 0o600))
	}

	return ws
}

// outsideDir returns a directory under the user cache directory, which no
// policy makes writable (t.TempDir is under $TMPDIR, which is).
func outsideDir(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(cache, "uah-sandbox-test-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	return dir
}

func TestWrapSeatbelt(t *testing.T) {
	outside := outsideDir(t)
	cases := []struct {
		name    string
		mode    sandbox.Mode
		noGit   bool
		script  string
		allowed bool
	}{
		{"workspace write", sandbox.WorkspaceWrite, false, `echo x > "$WS/src/a"`, true},
		{"workspace mkdir", sandbox.WorkspaceWrite, false, `mkdir "$WS/new" && echo x > "$WS/new/b"`, true},
		{"tmpdir write", sandbox.WorkspaceWrite, false, `echo x > "$TMPDIR/uah-sandbox-$$" && rm "$TMPDIR/uah-sandbox-$$"`, true},
		{"/tmp write", sandbox.WorkspaceWrite, false, `echo x > "/tmp/uah-sandbox-$$" && rm "/tmp/uah-sandbox-$$"`, true},
		{"write outside", sandbox.WorkspaceWrite, false, `echo x > "$OUT/a"`, false},
		{".git/config write", sandbox.WorkspaceWrite, false, `echo x >> "$WS/.git/config"`, false},
		{".git/hooks create", sandbox.WorkspaceWrite, false, `mkdir "$WS/.git/hooks"`, false},
		{".git rename", sandbox.WorkspaceWrite, false, `mv "$WS/.git" "$WS/g"`, false},
		{".git mkdir when absent", sandbox.WorkspaceWrite, true, `mkdir "$WS/.git"`, false},
		{".git file when absent", sandbox.WorkspaceWrite, true, `echo "gitdir: /x" > "$WS/.git"`, false},
		{".codex mkdir", sandbox.WorkspaceWrite, true, `mkdir "$WS/.codex"`, false},
		{".uah mkdir", sandbox.WorkspaceWrite, true, `mkdir "$WS/.uah"`, false},
		{"workspace unlink", sandbox.WorkspaceWrite, false, `rmdir "$WS/src" && mv "$WS" "$WS.moved"`, false},
		{"read-only workspace write", sandbox.ReadOnly, false, `echo x > "$WS/src/a"`, false},
		{"read-only tmpdir write", sandbox.ReadOnly, false, `echo x > "$TMPDIR/uah-sandbox-$$"`, false},
		{"read-only read", sandbox.ReadOnly, false, `cat "$WS/.git/config" /etc/hosts > /dev/null`, true},
		{"read-only /dev/null", sandbox.ReadOnly, false, `echo x > /dev/null`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := workspace(t, !tc.noGit)
			p := sandbox.Policy{Mode: tc.mode, Workspace: ws}
			code, out := run(t, p, tc.script, []string{"WS=" + ws, "OUT=" + outside}, nil)
			if tc.allowed {
				assert.Zero(t, code, out)
			} else {
				assert.NotZero(t, code, out)
				assert.Contains(t, out, "not permitted")
			}
		})
	}
}

// TestWrapSeatbeltGitdir checks that a worktree's gitdir stays read-only
// when it lies in another writable root ($TMPDIR here).
func TestWrapSeatbeltGitdir(t *testing.T) {
	ws := workspace(t, false)
	gitdir := filepath.Join(filepath.Dir(ws), "main", ".git", "worktrees", "x")
	require.NoError(t, os.MkdirAll(gitdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600))
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}

	code, out := run(t, p, `echo x > "$G/HEAD"`, []string{"G=" + gitdir}, nil)
	assert.NotZero(t, code, out)
	code, out = run(t, p, `echo x > "$G/../sibling"`, []string{"G=" + gitdir}, nil)
	assert.Zero(t, code, "only the gitdir itself is protected: %s", out)
}

func TestWrapSeatbeltExitCode(t *testing.T) {
	ws := workspace(t, false)
	for _, mode := range []sandbox.Mode{sandbox.ReadOnly, sandbox.WorkspaceWrite} {
		code, out := run(t, sandbox.Policy{Mode: mode, Workspace: ws}, "exit 7", nil, nil)
		assert.Equal(t, 7, code, out)
	}
}

// TestWrapSeatbeltInheritedFd checks that a file the parent opened stays
// writable: Seatbelt checks paths on open, and the runner opens its output
// files before it starts the command.
func TestWrapSeatbeltInheritedFd(t *testing.T) {
	ws := workspace(t, false)
	path := filepath.Join(outsideDir(t), "out")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	for _, mode := range []sandbox.Mode{sandbox.ReadOnly, sandbox.WorkspaceWrite} {
		code, out := run(t, sandbox.Policy{Mode: mode, Workspace: ws}, "echo "+string(mode), nil, f)
		assert.Zero(t, code, out)
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "read-only\nworkspace-write\n", string(data))

	code, out := run(t, sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}, `echo x >> "$F"`, []string{"F=" + path}, nil)
	assert.NotZero(t, code, "reopening the file by path must be denied: %s", out)
}

func TestWrapSeatbeltNetwork(t *testing.T) {
	ws := workspace(t, false)
	probe := "/usr/bin/nc -z -w 3 1.1.1.1 443"
	code, out := run(t, sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}, probe, nil, nil)
	assert.NotZero(t, code, "network must be denied: %s", out)

	if err := exec.CommandContext(t.Context(), "/bin/sh", "-c", probe).Run(); err != nil {
		t.Skipf("no network outside the sandbox: %v", err)
	}
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, Network: true}
	code, out = run(t, p, probe, nil, nil)
	assert.Zero(t, code, out)
	code, out = run(t, p, "/usr/bin/curl -sS -m 5 -o /dev/null https://example.com", nil, nil)
	assert.Zero(t, code, "TLS and DNS with network: %s", out)
}

// TestWrapSeatbeltGoBuild records how go build behaves in workspace-write.
// The default GOCACHE (~/Library/Caches/go-build) is not writable, yet go
// build still succeeds: Go reads the cache and ignores failed cache writes,
// so new build results are not kept. A GOCACHE under the workspace or /tmp
// is writable (TestWrapSeatbelt writes both) and works as usual.
func TestWrapSeatbeltGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	t.Parallel()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	ws := workspace(t, false)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example.com/hello\n\ngo 1.22\n"), 0o600))
	main := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi " + t.Name() + time.Now().String() + "\") }\n"
	require.NoError(t, os.WriteFile(filepath.Join(ws, "main.go"), []byte(main), 0o600))
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}
	build := goBin + " env GOCACHE && " + goBin + " build -o hello . && ./hello"

	code, out := run(t, p, `touch "$(`+goBin+` env GOCACHE)/uah-sandbox-probe"`, nil, nil)
	assert.NotZero(t, code, "the default GOCACHE should not be writable: %s", out)
	code, out = run(t, p, build, nil, nil)
	t.Logf("default GOCACHE: exit %d\n%s", code, out)
	assert.Zero(t, code, "go build with a read-only GOCACHE: %s", out)
}

// shellRun runs command with `<shell> -c` through the sandboxing shell of
// the policy, as the engine's Bash does, and returns its exit code and
// combined output.
func shellRun(t *testing.T, p sandbox.Policy, realShell, command string, env []string) (int, string) {
	t.Helper()
	shell, err := sandbox.Shell(t.TempDir(), p, sandbox.EnvPolicy{}, realShell)
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), shell, "-c", command)
	cmd.Dir = p.Workspace
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out)
	}
	require.NoError(t, err)

	return 0, string(out)
}

// TestShellSeatbeltTempDir checks that a read-only command can write its
// session's $TMPDIR, and nothing else, so heredocs work.
func TestShellSeatbeltTempDir(t *testing.T) {
	ws := workspace(t, true)
	outside := outsideDir(t)
	temp := filepath.Join(outsideDir(t), "tmp")
	p := sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: ws, TempDir: temp}
	env := []string{"WS=" + ws, "OUT=" + outside}

	cases := []struct {
		name, shell, command string
		allowed              bool
	}{
		{"tmpdir write", "/bin/sh", `echo x > "$TMPDIR/a" && cat "$TMPDIR/a"`, true},
		{"TMP and TEMP", "/bin/sh", `test "$TMP" = "$TMPDIR" && test "$TEMP" = "$TMPDIR" && echo x > "$TEMP/b"`, true},
		{"mktemp", "/bin/sh", `f=$(mktemp) && echo x > "$f"`, true},
		{"zsh heredoc", "/bin/zsh", "cat <<EOF\nhi\nEOF", true},
		{"zsh here-string", "/bin/zsh", "cat <<< hi", true},
		{"workspace write", "/bin/sh", `echo x > "$WS/src/a"`, false},
		{"outside write", "/bin/sh", `echo x > "$OUT/a"`, false},
		{"/tmp write", "/bin/sh", `echo x > "/tmp/uah-sandbox-$$"`, false},
		{".git in tmpdir", "/bin/sh", `mkdir "$TMPDIR/.git"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := shellRun(t, p, tc.shell, tc.command, env)
			if tc.allowed {
				assert.Zero(t, code, out)
			} else {
				assert.NotZero(t, code, out)
				assert.Contains(t, out, "not permitted")
			}
		})
	}
	info, err := os.Stat(temp)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "Shell creates the directory private")

	t.Run("fish psub", func(t *testing.T) {
		fish, err := exec.LookPath("fish")
		if err != nil {
			t.Skip("fish is not on PATH")
		}
		code, out := shellRun(t, p, fish, "cat (echo hi | psub)", nil)
		assert.Zero(t, code, out)
	})
	t.Run("bash 3.2 heredoc", func(t *testing.T) {
		// macOS's bash 3.2 makes a heredoc's file in /tmp whatever TMPDIR
		// says (bash 5.1 uses $TMPDIR, or a pipe), so it still fails in
		// read-only mode.
		code, out := shellRun(t, p, "/bin/bash", "cat <<EOF\nhi\nEOF", nil)
		t.Logf("exit %d: %s", code, out)
	})
	t.Run("zsh heredoc without a temp dir", func(t *testing.T) {
		code, out := shellRun(t, sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: ws}, "/bin/zsh", "cat <<EOF\nhi\nEOF", nil)
		assert.NotZero(t, code, "the failure the temp dir fixes: %s", out)
	})
}

// TestShellSeatbeltTempDirGoBuild checks that go build runs in read-only
// mode: Go makes its work directory under $TMPDIR, and a cache pointed at
// it is kept. A build with the default, read-only GOCACHE succeeds too
// (TestWrapSeatbeltGoBuild).
func TestShellSeatbeltTempDirGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	t.Parallel()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	ws := workspace(t, false)
	require.NoError(t, os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module example.com/hello\n\ngo 1.22\n"), 0o600))
	main := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi " + t.Name() + time.Now().String() + "\") }\n"
	require.NoError(t, os.WriteFile(filepath.Join(ws, "main.go"), []byte(main), 0o600))
	p := sandbox.Policy{Mode: sandbox.ReadOnly, Workspace: ws, TempDir: filepath.Join(outsideDir(t), "tmp")}

	code, out := shellRun(t, p, "/bin/sh", `GOCACHE="$TMPDIR/go-build" `+goBin+` build -o "$TMPDIR/hello" . && "$TMPDIR/hello" && ls "$TMPDIR/go-build"`, nil)
	assert.Zero(t, code, out)
	assert.NotEmpty(t, strings.TrimSpace(out), "the build fills the cache under $TMPDIR")
}

// TestSeatbeltShellScriptsStayReadOnly checks that a sandboxed command
// cannot replace the sandboxing scripts, or move their directory or one of
// its parents aside to put its own in place, also when the directory is
// under $TMPDIR, a writable root: the next command would run that script
// outside the sandbox.
func TestSeatbeltShellScriptsStayReadOnly(t *testing.T) {
	ws := workspace(t, false)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home := filepath.Join(tmp, "home")
	dir := filepath.Join(home, "state", "sandbox")
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws}
	shell, err := sandbox.Shell(dir, p, sandbox.EnvPolicy{}, "/bin/sh")
	require.NoError(t, err)
	want, err := os.ReadFile(shell)
	require.NoError(t, err)
	shRun := func(command string) (int, string) {
		cmd := exec.CommandContext(t.Context(), shell, "-c", command)
		cmd.Dir = ws
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), string(out)
		}
		require.NoError(t, err)

		return 0, string(out)
	}

	code, out := shRun("echo hi > a && echo hi > " + filepath.Join(tmp, "b"))
	require.Equal(t, 0, code, "the workspace and $TMPDIR stay writable: %s", out)
	for _, command := range []string{
		"printf '#!/bin/sh\\nexec /bin/sh \"$@\"\\n' > " + shell,
		"rm " + shell,
		"touch " + filepath.Join(dir, "sh-new"),
		"mv " + dir + " " + dir + ".moved",
		"mv " + home + " " + home + ".moved",
		"mv " + filepath.Join(home, "state") + " " + filepath.Join(home, "state.moved"),
	} {
		code, out := shRun(command)
		assert.NotEqual(t, 0, code, "%s: %s", command, out)
	}
	got, err := os.ReadFile(shell)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
	assert.NoDirExists(t, home+".moved")
}

// TestSeatbeltShellScriptsCaseAliases checks that a writable root that
// names the scripts' directory, or a directory above it, in another case
// does not open the scripts: Seatbelt compares names without case.
func TestSeatbeltShellScriptsCaseAliases(t *testing.T) {
	out := outsideDir(t)
	dir := filepath.Join(out, "state", "sandbox")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	if _, err := os.Stat(filepath.Join(out, "STATE")); err != nil {
		t.Skip("this file system tells cases apart")
	}
	for _, root := range []string{filepath.Join(out, "STATE"), filepath.Join(out, "state", "SANDBOX")} {
		p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: root}
		shell, err := sandbox.Shell(dir, p, sandbox.EnvPolicy{}, "/bin/sh")
		require.NoError(t, err)
		want, err := os.ReadFile(shell)
		require.NoError(t, err)
		cmd := exec.CommandContext(t.Context(), shell, "-c", "echo evil > "+shell)
		cmd.Dir = out
		output, err := cmd.CombinedOutput()
		assert.Error(t, err, "%s: %s", root, output)
		got, err := os.ReadFile(shell)
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), root)
	}
}
