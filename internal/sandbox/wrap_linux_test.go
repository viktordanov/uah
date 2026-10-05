//go:build linux

package sandbox_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
)

// requireBwrap skips the test unless bwrap can create the namespaces it
// needs; on Ubuntu 24.04 AppArmor blocks unprivileged user namespaces unless
// kernel.apparmor_restrict_unprivileged_userns is 0. CI sets
// UAH_REQUIRE_BWRAP=1 to fail instead of skipping.
func requireBwrap(t *testing.T) {
	t.Helper()
	skip := t.Skipf
	if os.Getenv("UAH_REQUIRE_BWRAP") != "" {
		skip = t.Fatalf
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		skip("bwrap is not installed")
	}
	out, err := exec.Command(bwrap, "--unshare-user", "--unshare-net", "--ro-bind", "/", "/", "true").CombinedOutput()
	if err != nil {
		skip("bwrap cannot create user namespaces: %v: %s", err, out)
	}
}

type linuxRun struct {
	base, ws, outside string
}

// newLinuxRun makes a workspace with a .git directory and a directory outside
// every writable root. Both live under the package directory, not /tmp, which
// is writable in workspace-write.
func newLinuxRun(t *testing.T) linuxRun {
	t.Helper()
	requireBwrap(t)
	base := goldenBase(t)
	t.Setenv("TMPDIR", mkdir(t, base, "tmpdir"))
	r := linuxRun{base: base, ws: mkdir(t, base, "ws", ".git"), outside: mkdir(t, base, "outside")}
	require.NoError(t, os.WriteFile(filepath.Join(r.ws, ".git", "config"), []byte("[core]\n"), 0o600))
	for _, mode := range []sandbox.Mode{sandbox.ReadOnly, sandbox.WorkspaceWrite} {
		code, out := sh(t, r.policy(mode, false), "true")
		require.Equal(t, 0, code, "the sandbox itself fails in %s: %s", mode, out)
	}

	return r
}

func (r linuxRun) policy(mode sandbox.Mode, network bool) sandbox.Policy {
	return sandbox.Policy{Mode: mode, Workspace: r.ws, Network: network}
}

// run runs name with args inside the sandbox and returns its exit code and
// output.
func run(t *testing.T, p sandbox.Policy, stdout *os.File, name string, args ...string) (code int, output string) {
	t.Helper()
	argv, err := p.Wrap(append([]string{name}, args...))
	require.NoError(t, err)
	cmd := exec.Command(argv[0], argv[1:]...)
	var buf strings.Builder
	cmd.Stderr = &buf
	cmd.Stdout = &buf
	if stdout != nil {
		cmd.Stdout = stdout
	}
	cmd.Dir = p.Workspace
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), buf.String()
	}
	require.NoError(t, err)

	return 0, buf.String()
}

func sh(t *testing.T, p sandbox.Policy, script string) (code int, output string) {
	t.Helper()

	return run(t, p, nil, "/bin/sh", "-c", script)
}

func TestLinuxWorkspaceWrite(t *testing.T) {
	r := newLinuxRun(t)
	p := r.policy(sandbox.WorkspaceWrite, false)

	code, out := sh(t, p, "echo hi > a && mkdir -p sub && echo hi > sub/b && echo hi > \"$TMPDIR/c\" && echo hi > /dev/null")
	require.Equal(t, 0, code, out)
	assert.FileExists(t, filepath.Join(r.ws, "a"))
	assert.FileExists(t, filepath.Join(r.ws, "sub", "b"))

	code, out = sh(t, p, "echo hi > "+filepath.Join(r.outside, "x"))
	assert.NotEqual(t, 0, code)
	assert.Contains(t, out, "Read-only file system")
	assert.True(t, sandbox.Denied(code, out), out)
	assert.NoFileExists(t, filepath.Join(r.outside, "x"))

	code, out = sh(t, p, "echo '[x]' >> .git/config")
	assert.NotEqual(t, 0, code)
	assert.True(t, sandbox.Denied(code, out), out)
	data, err := os.ReadFile(filepath.Join(r.ws, ".git", "config"))
	require.NoError(t, err)
	assert.Equal(t, "[core]\n", string(data))

	require.NoError(t, os.Mkdir(filepath.Join(r.ws, ".uah"), 0o700))
	code, out = sh(t, p, "mkdir .uah/rules")
	assert.NotEqual(t, 0, code)
	assert.True(t, sandbox.Denied(code, out), out)

	// A protected name that does not exist yet is not protected on Linux.
	code, out = sh(t, p, "mkdir .codex")
	assert.Equal(t, 0, code, out)
}

func TestLinuxReadOnly(t *testing.T) {
	r := newLinuxRun(t)
	p := r.policy(sandbox.ReadOnly, false)

	code, out := sh(t, p, "cat .git/config")
	require.Equal(t, 0, code, out)
	assert.Equal(t, "[core]\n", out)

	code, out = sh(t, p, "echo hi > a")
	assert.NotEqual(t, 0, code)
	assert.True(t, sandbox.Denied(code, out), out)
	assert.NoFileExists(t, filepath.Join(r.ws, "a"))
}

func TestLinuxNetwork(t *testing.T) {
	r := newLinuxRun(t)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	// Connect by address, so the test does not depend on DNS.
	code, out := run(t, r.policy(sandbox.WorkspaceWrite, false), nil, bash, "-c", "exec 3<>/dev/tcp/1.1.1.1/443")
	assert.NotEqual(t, 0, code)
	assert.Contains(t, strings.ToLower(out), "network is unreachable")
	assert.True(t, sandbox.Denied(code, out), out)
}

func TestLinuxInheritedOutput(t *testing.T) {
	r := newLinuxRun(t)
	// A file outside every writable root, opened by the parent, as the
	// runner opens its output files.
	f, err := os.Create(filepath.Join(r.outside, "out.log"))
	require.NoError(t, err)
	defer f.Close()

	code, out := run(t, r.policy(sandbox.WorkspaceWrite, false), f, "/bin/sh", "-c", "echo through the fd")
	require.Equal(t, 0, code, out)
	data, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	assert.Equal(t, "through the fd\n", string(data))
}

func TestLinuxExitCode(t *testing.T) {
	r := newLinuxRun(t)
	code, out := sh(t, r.policy(sandbox.WorkspaceWrite, false), "echo failing >&2; exit 7")
	assert.Equal(t, 7, code)
	assert.Equal(t, "failing\n", out)
	assert.False(t, sandbox.Denied(code, out))
}

// TestLinuxReadOnlyTempDir checks that a read-only command, run through the
// sandboxing shell, can write the session's $TMPDIR and nothing else, so a
// heredoc works.
func TestLinuxReadOnlyTempDir(t *testing.T) {
	r := newLinuxRun(t)
	p := r.policy(sandbox.ReadOnly, false)
	p.TempDir = filepath.Join(r.base, "session", "tmp")
	shell, err := sandbox.Shell(t.TempDir(), p, sandbox.EnvPolicy{}, "/bin/sh")
	require.NoError(t, err)
	shRun := func(command string) (int, string) {
		cmd := exec.Command(shell, "-c", command)
		cmd.Dir = r.ws
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), string(out)
		}
		require.NoError(t, err)

		return 0, string(out)
	}

	code, out := shRun(`test "$TMPDIR" = "` + p.TempDir + `" && echo hi > "$TMPDIR/a" && cat <<EOF
heredoc
EOF`)
	require.Equal(t, 0, code, out)
	assert.Equal(t, "heredoc\n", out)
	assert.FileExists(t, filepath.Join(p.TempDir, "a"))

	for _, target := range []string{filepath.Join(r.ws, "a"), filepath.Join(r.outside, "a"), filepath.Join(r.base, "tmpdir", "a")} {
		code, out = shRun("echo hi > " + target)
		assert.NotEqual(t, 0, code, target)
		assert.True(t, sandbox.Denied(code, out), out)
	}
}

// TestLinuxShellScriptsStayReadOnly checks that a sandboxed command cannot
// replace the sandboxing scripts, or move their directory or one of its
// parents aside to put its own in place, also when the directory is under
// $TMPDIR, a writable root: the next command would run that script outside
// the sandbox.
func TestLinuxShellScriptsStayReadOnly(t *testing.T) {
	r := newLinuxRun(t)
	tmp := filepath.Join(r.base, "tmpdir")
	home := filepath.Join(tmp, "home")
	dir := filepath.Join(home, "state", "sandbox")
	shell, err := sandbox.Shell(dir, r.policy(sandbox.WorkspaceWrite, false), sandbox.EnvPolicy{}, "/bin/sh")
	require.NoError(t, err)
	want, err := os.ReadFile(shell)
	require.NoError(t, err)
	shRun := func(command string) (int, string) {
		cmd := exec.Command(shell, "-c", command)
		cmd.Dir = r.ws
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), string(out)
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

// TestLinuxSignals: a sandboxed command can stop its own children, but it
// cannot see a process an earlier sandboxed command started: each runs in
// a new PID namespace. The engine runs a kill of the session's own
// commands outside the sandbox for that (internal/engine/embedded/commands.go).
func TestLinuxSignals(t *testing.T) {
	r := newLinuxRun(t)
	p := r.policy(sandbox.ReadOnly, false)

	code, out := sh(t, p, "sleep 30 & kill $!; wait $!; echo status=$?")
	require.Equal(t, 0, code, out)
	assert.Contains(t, out, "status=143", "its own child stopped")

	argv, err := p.Wrap([]string{"/bin/sh", "-c", "exec sleep 30"})
	require.NoError(t, err)
	earlier := exec.Command(argv[0], argv[1:]...)
	require.NoError(t, earlier.Start())
	t.Cleanup(func() { _ = earlier.Process.Kill(); _ = earlier.Wait() })
	code, out = sh(t, p, "kill "+strconv.Itoa(earlier.Process.Pid))
	assert.NotEqual(t, 0, code, "another command's process is out of reach: %s", out)
}
