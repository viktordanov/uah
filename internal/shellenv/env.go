package shellenv

import (
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
)

// Keys are the variables Locale and MissingToolDirs read.
var Keys = []string{"LC_ALL", "LC_CTYPE", "LANG", "PATH", "HOME"}

// Locale is the variable that decides the character set and its value,
// such as "LANG=C": LC_ALL, else LC_CTYPE, else LANG, the first that is
// set, as the C library reads them. utf8 reports whether it names UTF-8;
// with none set, setting is "" and utf8 false, since the C locale is
// ASCII.
func Locale(getenv func(string) string) (setting string, utf8 bool) {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			lower := strings.ToLower(v)

			return k + "=" + v, strings.Contains(lower, "utf-8") || strings.Contains(lower, "utf8")
		}
	}

	return "", false
}

// ToolDirs are where package managers and toolchains put the user's
// tools, ~ for the home directory.
var ToolDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "~/.local/bin", "~/go/bin", "~/.cargo/bin"}

// MissingToolDirs are the ToolDirs that exist when none of them is on
// PATH: a PATH that minimal finds none of the user's tools by name. It is
// nil when one of them is on PATH or none exists.
func MissingToolDirs(getenv func(string) string) []string {
	home := getenv("HOME")
	if home == "" {
		if u, err := user.Current(); err == nil {
			home = u.HomeDir
		}
	}
	var exist []string
	for _, d := range ToolDirs {
		if rest, ok := strings.CutPrefix(d, "~/"); ok {
			if home == "" {
				continue
			}
			d = filepath.Join(home, rest)
		}
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			exist = append(exist, d)
		}
	}
	var resolved []string
	for _, d := range exist {
		resolved = append(resolved, evalSymlinks(d))
	}
	for _, p := range filepath.SplitList(getenv("PATH")) {
		if filepath.IsAbs(p) && slices.Contains(resolved, evalSymlinks(p)) {
			return nil
		}
	}

	return exist
}

// evalSymlinks is p with its symbolic links resolved, so two spellings of
// one directory compare equal, or p cleaned when that fails.
func evalSymlinks(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}

	return filepath.Clean(p)
}
