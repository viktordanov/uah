package contextprep_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/contextprep"
)

func TestEnvironmentName(t *testing.T) {
	t.Parallel()
	var a contextprep.Adapter = contextprep.Environment{}
	assert.Equal(t, "environment", a.Name())
}

// TestEnvironmentPrepare pins each case's first line and the traps it names.
func TestEnvironmentPrepare(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		shell, goos string
		first       string
		has         []string
		hasNot      []string
	}{
		{
			shell: "/bin/bash", goos: "linux", first: "Commands run in bash (/bin/bash -c) on Linux.",
			hasNot: []string{"\n"},
		},
		{
			shell: "/bin/bash", goos: "darwin", first: "Commands run in bash (/bin/bash -c) on macOS.",
			has: []string{"3.2", "declare -A", "sed -i ''", "stat -f", "date -v-1d", "grep -P", "sysctl -n hw.ncpu"},
		},
		{
			shell: "/opt/homebrew/bin/bash", goos: "darwin", first: "Commands run in bash (/opt/homebrew/bin/bash -c) on macOS.",
			has: []string{"sed -i ''"}, hasNot: []string{"3.2"},
		},
		{
			shell: "/bin/zsh", goos: "darwin", first: "Commands run in zsh (/bin/zsh -c) on macOS.",
			has: []string{"unmatched glob", "no matches found", "${=var}", "read -A", "sed -i ''"},
		},
		{
			shell: "/usr/bin/zsh", goos: "linux", first: "Commands run in zsh (/usr/bin/zsh -c) on Linux.",
			has: []string{"unmatched glob"}, hasNot: []string{"BSD"},
		},
		{
			shell: "/opt/homebrew/bin/fish", goos: "darwin", first: "Commands run in fish (/opt/homebrew/bin/fish -c) on macOS.",
			has: []string{
				"not a POSIX shell", "<<EOF", "do …; done", "end", "set x 1", "set -x", "VAR=1 cmd works",
				"$status", "{$x}", "math 1+2", "(cmd | psub)", "unmatched glob", "sh -c '…'", "sed -i ''",
			},
		},
		{
			shell: "/usr/bin/fish", goos: "linux", first: "Commands run in fish (/usr/bin/fish -c) on Linux.",
			has: []string{"set x 1"}, hasNot: []string{"BSD"},
		},
		{shell: "/bin/dash", goos: "linux", first: "Commands run in sh (/bin/dash -c) on Linux.", has: []string{"[[ ]]", "bash -c '…'"}},
		{shell: "", goos: "linux", first: "Commands run in sh (/bin/sh -c) on Linux.", has: []string{"POSIX sh"}},
		{
			shell: "/usr/local/bin/nu", goos: "linux", first: "Commands run in nushell (/usr/local/bin/nu -c) on Linux.",
			has: []string{"&& and ||", "$env.X = '1'", "(cmd)", "o+e>|", "^ls", "sh -c '…'"},
		},
		{
			shell: "/usr/bin/xonsh", goos: "linux", first: "Commands run in xonsh (/usr/bin/xonsh -c) on Linux.",
			has: []string{"Python", "$X = '1'", "regex globs", "$(cmd)"},
		},
		{
			shell: "/usr/bin/elvish", goos: "linux", first: "Commands run in elvish (/usr/bin/elvish -c) on Linux.",
			has: []string{"var x = 1", "set E:X = 1", "(cmd)"},
		},
		{
			shell: `C:\Program Files\PowerShell\7\pwsh.exe`, goos: "windows",
			first: `Commands run in powershell (C:\Program Files\PowerShell\7\pwsh.exe -c) on Windows.`,
			has:   []string{"$env:X = '1'", "here-string", "2>$null", "Remove-Item -Recurse -Force", "drive letters"},
		},
		{
			shell: `C:\Windows\System32\cmd.exe`, goos: "windows",
			first: `Commands run in cmd (C:\Windows\System32\cmd.exe -c) on Windows.`,
			has:   []string{"set X=1", "%X%", "2>nul"},
		},
		{
			shell: "/bin/tcsh", goos: "freebsd", first: "Commands run in csh (/bin/tcsh -c) on FreeBSD.",
			has: []string{"setenv X 1", "foreach", ">&", "BSD tools"},
		},
		{
			shell: "/usr/bin/oddsh", goos: "plan9", first: "Commands run in oddsh (/usr/bin/oddsh -c) on plan9.",
			has: []string{"may not be a POSIX shell", "sh -c '…'"},
		},
	} {
		got := contextprep.Environment{}.Prepare(context.Background(), contextprep.Facts{Shell: tc.shell, GOOS: tc.goos})
		first, _, _ := strings.Cut(got, "\n")
		assert.Equal(t, tc.first, first, tc.shell)
		for _, s := range tc.has {
			assert.Contains(t, got, s, tc.shell+" on "+tc.goos)
		}
		for _, s := range tc.hasNot {
			assert.NotContains(t, got, s, tc.shell+" on "+tc.goos)
		}
	}
}

// TestShellClaims runs what the fish and zsh guidance says fails and what
// it says to write instead, in the shells installed here.
func TestShellClaims(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		shell      string
		fail, pass []string
	}{
		{
			shell: "fish",
			fail: []string{
				"cat <<EOF\nx\nEOF", "for i in 1 2; do echo $i; done", "if true; then echo y; fi",
				"x=1", "echo $?", "echo ${HOME}", "echo $((1+2))", "echo *.no-such-glob",
			},
			pass: []string{
				"printf '%s\\n' x | cat", "for i in 1 2; echo $i; end", "if true; echo y; end",
				"set x 1; echo $x", "set -x X 1; sh -c 'test \"$X\" = 1'", "X=1 sh -c 'test \"$X\" = 1'",
				"false; test $status = 1", "echo {$HOME}", "math 1+2", "echo (echo sub)",
				"diff (echo a | psub) (echo a | psub)", "true && echo y || echo n",
				"sh -c 'for i in 1 2; do x=$i; done; echo $x'", `test 'it\'s' = "it's"`,
			},
		},
		{
			shell: "zsh",
			fail:  []string{"echo *.no-such-glob", "read -a a <<< 'x y'"},
			pass:  []string{`x="a b"; set -- $x; test $# = 1`, `x="a b"; set -- ${=x}; test $# = 2`, "a=(p q); test $a[1] = p", "read -A a <<< 'x y'"},
		},
	} {
		path, err := exec.LookPath(tc.shell)
		if err != nil {
			t.Logf("%s is not installed", tc.shell)
			continue
		}
		for _, c := range tc.fail {
			out, err := exec.Command(path, "-c", c).CombinedOutput()
			assert.Error(t, err, "%s -c %q: %s", tc.shell, c, out)
		}
		for _, c := range tc.pass {
			out, err := exec.Command(path, "-c", c).CombinedOutput()
			require.NoError(t, err, "%s -c %q: %s", tc.shell, c, out)
		}
	}
}

// TestEnvironmentStripped pins the lines about an environment a service
// started without the user's: each only in its abnormal case.
func TestEnvironmentStripped(t *testing.T) {
	t.Parallel()
	normal := contextprep.Facts{Shell: "/bin/bash", ShellSource: "env", Locale: "LANG=en_US.UTF-8", GOOS: "linux"}
	base := contextprep.Environment{}.Prepare(t.Context(), normal)
	assert.Equal(t, "Commands run in bash (/bin/bash -c) on Linux.", base, "nothing when all is normal")
	for _, tc := range []struct {
		name string
		edit func(*contextprep.Facts)
		want string
	}{
		{"login shell", func(f *contextprep.Facts) { f.ShellSource = "login" }, "$SHELL was unset or not an executable file when uah started, so uah picked your login shell from the user database."},
		{"default shell", func(f *contextprep.Facts) { f.Shell, f.ShellSource = "/bin/sh", "default" }, "$SHELL was unset or not an executable file when uah started, and the login shell could not be read, so uah fell back to /bin/sh."},
		{"C locale", func(f *contextprep.Facts) { f.Locale, f.NotUTF8 = "LANG=C", true }, "The locale is not UTF-8 (LANG=C): tools may"},
		{"no locale", func(f *contextprep.Facts) { f.Locale, f.NotUTF8 = "", true }, "The locale is not UTF-8 (LC_ALL, LC_CTYPE, and LANG unset)"},
		{"minimal PATH", func(f *contextprep.Facts) { f.MissingPathDirs = []string{"/opt/homebrew/bin", "/home/u/go/bin"} }, "PATH has none of the user's tool directories; these exist: /opt/homebrew/bin, /home/u/go/bin. A tool installed there is not found by name; call it by its full path."},
	} {
		f := normal
		tc.edit(&f)
		got := contextprep.Environment{}.Prepare(t.Context(), f)
		assert.Contains(t, got, tc.want, tc.name)
		plain := f
		plain.ShellSource, plain.Locale, plain.NotUTF8, plain.MissingPathDirs = "env", "LANG=en_US.UTF-8", false, nil
		assert.Equal(t, strings.Count(contextprep.Environment{}.Prepare(t.Context(), plain), "\n")+1, strings.Count(got, "\n"), "%s adds one line", tc.name)
	}

	f := normal
	f.ShellSource = "login"
	lines := strings.Split(contextprep.Environment{}.Prepare(t.Context(), f), "\n")
	assert.True(t, strings.HasPrefix(lines[1], "$SHELL was unset"), "the shell's source follows the first line")
}

// TestWithEnvironment fills the environment's facts from getenv.
func TestWithEnvironment(t *testing.T) {
	t.Parallel()
	env := map[string]string{"SHELL": "/bin/sh", "LANG": "C", "PATH": "/usr/bin:/bin", "HOME": t.TempDir()}
	f := contextprep.Facts{Workspace: "/w"}.WithEnvironment(func(k string) string { return env[k] })
	assert.Equal(t, "/bin/sh", f.Shell)
	assert.Equal(t, "env", f.ShellSource)
	assert.Equal(t, "LANG=C", f.Locale)
	assert.True(t, f.NotUTF8)
	assert.Equal(t, "/w", f.Workspace)

	env["LANG"] = "en_US.UTF-8"
	env["SHELL"] = "/nonexistent/shell"
	f = contextprep.Facts{}.WithEnvironment(func(k string) string { return env[k] })
	assert.False(t, f.NotUTF8)
	assert.NotEqual(t, "env", f.ShellSource, "a missing $SHELL is not used")
	assert.NotEqual(t, "/nonexistent/shell", f.Shell)
}
