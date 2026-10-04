// Package shellenv picks the shell uah runs commands in and notices what a
// stripped environment lacks: $SHELL, a UTF-8 locale, and the user's tool
// directories on PATH. A service that starts uah, or the terminal
// multiplexer it runs in, without the user's environment leaves all three
// out.
package shellenv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default is the shell when neither $SHELL nor the login shell is usable.
const Default = "/bin/sh"

// Source says where the shell came from.
type Source string

// The shell's sources.
const (
	// FromEnv is $SHELL.
	FromEnv Source = "env"
	// FromLogin is the user's login shell from the user database, when
	// $SHELL is unset or not an executable file.
	FromLogin Source = "login"
	// FromDefault is /bin/sh, when neither is usable.
	FromDefault Source = "default"
)

// Shell is the shell commands run in.
type Shell struct {
	// Path is the shell's path, such as /bin/zsh.
	Path string
	// Source says where Path came from.
	Source Source
	// Env is $SHELL as it was set, when it was set but is not an
	// executable file; "" otherwise.
	Env string
}

// Resolve picks the shell: $SHELL when it names an executable file (a
// bare name, such as fish, found on getenv's PATH), else the login shell
// that login returns when it is one, else /bin/sh. login is Login outside
// tests.
func Resolve(getenv func(string) string, login func() string) Shell {
	env := strings.TrimSpace(getenv("SHELL"))
	if p := onPath(env, getenv("PATH")); p != "" {
		return Shell{Path: p, Source: FromEnv}
	}
	if s := login(); s != "" && Executable(s) {
		return Shell{Path: s, Source: FromLogin, Env: env}
	}

	return Shell{Path: Default, Source: FromDefault, Env: env}
}

// Current is Resolve with the user database's login shell.
func Current(getenv func(string) string) Shell { return Resolve(getenv, Login) }

// onPath is name when it is an executable file's absolute path, the first
// executable file of that name in path's directories when it is a bare
// name, and "" otherwise: a relative path with a separator names no
// fixed file.
func onPath(name, path string) string {
	switch {
	case name == "":
		return ""
	case filepath.IsAbs(name):
		if Executable(name) {
			return name
		}

		return ""
	case strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator):
		return ""
	}
	for _, dir := range filepath.SplitList(path) {
		if p := filepath.Join(dir, name); filepath.IsAbs(dir) && Executable(p) {
			return p
		}
	}

	return ""
}

// Executable reports whether path is an absolute path to a regular file
// that someone may execute, following symbolic links.
func Executable(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	fi, err := os.Stat(path)

	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// Login is the current user's login shell from the user database, or ""
// when it cannot be read. It is looked up once per process.
//
// Adapted from openai/codex rust-v0.160.0 (Apache License 2.0, Copyright
// 2025 OpenAI), codex-rs/shell-command/src/shell_detect.rs,
// get_user_shell_path: Codex reads pw_shell with getpwuid_r for the
// current uid. Go's os/user does not expose pw_shell and uah builds
// without cgo, so uah reads the same record where the system keeps it:
// Directory Services (dscl) on macOS, /etc/passwd and then getent
// elsewhere.
var Login = sync.OnceValue(func() string { return lookupLogin(context.Background()) })

// lookupTimeout bounds a user database command.
const lookupTimeout = 2 * time.Second

// lookupLogin reads the login shell of the current user.
func lookupLogin(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	switch runtime.GOOS {
	case "windows", "plan9":
		return ""
	case "darwin":
		u, err := user.Current()
		if err != nil {
			return ""
		}

		return dsclShell(ctx, u.Username)
	}
	uid := os.Getuid()
	if s, err := PasswdShell("/etc/passwd", uid); err == nil {
		return s
	}

	return getentShell(ctx, uid)
}

// errNoEntry is a user database without the user.
var errNoEntry = errors.New("no entry for the user")

// PasswdShell is the login shell of uid in a passwd(5) file.
func PasswdShell(path string, uid int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	return passwdShell(f, uid)
}

// passwdShell reads passwd(5) lines, name:password:uid:gid:gecos:home:shell,
// and returns the shell of the first line with uid.
func passwdShell(r io.Reader, uid int) (string, error) {
	sc := bufio.NewScanner(r)
	want := strconv.Itoa(uid)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' || line[0] == '+' || line[0] == '-' {
			continue // comments and NIS compat entries
		}
		f := strings.Split(line, ":")
		if len(f) == 7 && f[2] == want {
			return strings.TrimSpace(f[6]), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("failed to read passwd: %w", err)
	}

	return "", errNoEntry
}

// getentShell asks the name service switch (LDAP, NIS, systemd's homed)
// for the user's entry; "" when getent is missing or fails.
func getentShell(ctx context.Context, uid int) string {
	bin, err := exec.LookPath("getent")
	if err != nil {
		bin = "/usr/bin/getent"
	}
	out, err := exec.CommandContext(ctx, bin, "passwd", strconv.Itoa(uid)).Output() //nolint:gosec // a fixed program and the numeric uid
	if err != nil {
		return ""
	}
	s, _ := passwdShell(bytes.NewReader(out), uid)

	return s
}

// dsclShell reads UserShell from Directory Services, macOS's user
// database: `dscl . -read /Users/<name> UserShell` prints
// "UserShell: /bin/zsh". No shell runs it.
func dsclShell(ctx context.Context, name string) string {
	if name == "" || strings.ContainsAny(name, "/\x00") {
		return ""
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/dscl", ".", "-read", "/Users/"+name, "UserShell").Output() //nolint:gosec // a fixed program; the name is one path element
	if err != nil {
		return ""
	}

	return dsclValue(string(out))
}

// dsclValue is the value in dscl's "UserShell: <value>" output.
func dsclValue(out string) string {
	v, ok := strings.CutPrefix(strings.TrimSpace(out), "UserShell:")
	if !ok {
		return ""
	}

	return strings.TrimSpace(v)
}
