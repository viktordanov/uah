package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// Shell returns a shell for the runner that runs every command under the
// policy and with the environment policy: a small script in dir that execs
// the sandbox around realShell, so `<script> -c <command>` is
// `<sandbox> <realShell> -c <command>`. Scripts are named by their content,
// so a session reuses one and a changed policy gets a new one. Shell creates
// the policy's TempDir (mode 0700) and sets TMPDIR, TMP, TEMP, and zsh's
// TMPPREFIX into it. With FullAccess, the default environment policy, and
// no TempDir it returns realShell.
//
// The scripts run outside the sandbox, so a sandboxed command must not
// change them: Shell makes dir a private directory (0700, the user's own)
// and adds it to the policy's ReadOnly paths, so it stays read-only also
// when it lies in a writable root, such as $TMPDIR. A script already in dir
// is used only when it is the user's, private, and has the content Shell
// would write; any other file is replaced.
func Shell(dir string, p Policy, env EnvPolicy, realShell string) (string, error) {
	if err := privateDir(dir); err != nil {
		return "", err
	}
	p.ReadOnly = append(slices.Clip(p.ReadOnly), dir)
	if p.TempDir != "" {
		// The sandbox can only grant a directory that exists: Seatbelt
		// matches resolved paths and bwrap binds existing ones.
		if err := os.MkdirAll(p.TempDir, 0o700); err != nil {
			return "", fmt.Errorf("failed to create the temporary directory: %w", err)
		}
	}
	argv := []string{realShell}
	if p.Mode != FullAccess {
		var err error
		if argv, err = p.Wrap(argv); err != nil {
			return "", err
		}
	} else if env.isDefault() && p.TempDir == "" {
		return realShell, nil
	}
	words := make([]string, 0, len(argv)+8)
	switch {
	case !env.isDefault():
		words = append(words, envWords(env, p.TempDir != "")...)
	case p.TempDir != "":
		words = append(words, "/usr/bin/env")
	}
	if p.TempDir != "" {
		for _, name := range tempVars {
			value := p.TempDir
			if name == "TMPPREFIX" {
				value = filepath.Join(p.TempDir, "zsh")
			}
			words = append(words, quote(name+"="+value))
		}
	}
	for _, a := range argv {
		words = append(words, quote(a))
	}
	script := "#!/bin/sh\n# Written by uah: runs the command in the " + string(p.Mode) + " sandbox.\nexec " + strings.Join(words, " ") + " \"$@\"\n"

	return writeScript(dir, script)
}

// tempVars name the temporary directory; Shell sets each to the policy's
// TempDir. zsh ignores TMPDIR and makes its temporary files, such as a
// heredoc's, at $TMPPREFIX (default /tmp/zsh), so that is <TempDir>/zsh.
var tempVars = []string{"TMPDIR", "TMP", "TEMP", "TMPPREFIX"}

// writeScript writes an executable script into dir, named by its content
// (sh-<hash>), and returns its path. A file already there is kept only when
// it is a regular file of the user's, mode 0700, with exactly this content;
// anything else, such as a script a command replaced or a symlink, is
// replaced by a fresh copy.
func writeScript(dir, script string) (string, error) {
	sum := sha256.Sum256([]byte(script))
	path := filepath.Join(dir, "sh-"+hex.EncodeToString(sum[:8]))
	if trusted(path, script) {
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, ".sh-*")
	if err != nil {
		return "", fmt.Errorf("failed to write the sandbox shell: %w", err)
	}
	_, werr := tmp.WriteString(script)
	if err := errors.Join(werr, tmp.Chmod(0o700), tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("failed to write the sandbox shell: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())

		return "", fmt.Errorf("failed to save the sandbox shell: %w", err)
	}

	return path, nil
}

// trusted reports whether path is a script Shell wrote with this content:
// a regular file (not a symlink) owned by the user, mode 0700.
func trusted(path, script string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 || !ownedByUser(info) {
		return false
	}
	data, err := os.ReadFile(path)

	return err == nil && string(data) == script
}

// privateDir creates dir (mode 0700) when it is missing and makes sure it is
// a directory of the user's that no one else can write.
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("failed to check %s: %w", dir, err)
	}
	if !info.IsDir() || !ownedByUser(info) {
		return fmt.Errorf("the sandbox shell directory %s is not a directory of the current user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("failed to make %s private: %w", dir, err)
		}
	}

	return nil
}

// ownedByUser reports whether the file belongs to the current user.
func ownedByUser(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)

	return ok && int(st.Uid) == os.Getuid()
}

// Quote quotes s for /bin/sh.
func Quote(s string) string { return quote(s) }

// envWords starts the command with `env -i` and the variables the policy
// keeps. Inherited variables are copied from the environment when the
// command runs ("NAME=$NAME"), so no inherited value is written to disk;
// only the policy's own Set values are. With temp, tempVars are left out,
// for Shell to set.
func envWords(env EnvPolicy, temp bool) []string {
	words := []string{"/usr/bin/env", "-i"}
	for _, kv := range env.Apply(os.Environ()) {
		name, value, _ := strings.Cut(kv, "=")
		if !shellName(name) || temp && slices.Contains(tempVars, name) {
			continue
		}
		if v, ok := env.Set[name]; ok && v == value {
			words = append(words, quote(name+"="+value))
		} else {
			words = append(words, name+`="$`+name+`"`)
		}
	}

	return words
}

// shellName reports whether s can be referenced as $s.
func shellName(s string) bool {
	for i, r := range s {
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}

	return s != ""
}

// quote quotes s for /bin/sh.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
