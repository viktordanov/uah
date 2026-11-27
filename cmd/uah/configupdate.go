package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/home"
)

const flagDryRun = "dry-run"

// configUpdateCommand is `uah config update`: the settings uah describes,
// added to the user file as comments, for the user to uncomment.
func configUpdateCommand() *cli.Command {
	return &cli.Command{
		Name:  "update",
		Usage: "add the settings this version describes to the user file, commented out",
		Description: "Appends each new setting the user file does not set yet, such as [advisor], as TOML\n" +
			"comments: what it does, where its keys are documented, and examples to uncomment. Only\n" +
			"comments go in, so the file means the same to any uah, an older one too. It adds a block\n" +
			"once, keeps a symlinked file a symlink, and creates a missing file. The first TUI session\n" +
			"after an update names the settings that are new since this home last ran one.",
		Flags: []cli.Flag{
			userFileFlag(),
			&cli.BoolFlag{Name: flagDryRun, Usage: "print what would be added, and leave the file as it is"},
		},
		OnUsageError: onUsageError,
		Action: func(_ context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return cli.Exit("config update takes no arguments (see --help)", exitUsage)
			}

			file, err := updateTarget(cmd)
			if err != nil {
				return err
			}

			return configUpdate(os.Stdout, file, docsRef(buildVersion()), cmd.Bool(flagDryRun))
		},
	}
}

// configUpdate adds the new settings to the user file at path, or with dry
// prints them, and says what it did.
func configUpdate(w io.Writer, path, ref string, dry bool) error {
	added, err := config.AddNewSettings(path, ref, dry)
	if err != nil {
		return exitError(err)
	}
	if len(added) == 0 {
		_, err = fmt.Fprintf(w, "%s has every setting this uah describes, set or as comments\n", path)

		return err
	}
	for _, n := range added {
		if dry {
			_, err = fmt.Fprintf(w, "would add [%s] to %s, commented out:\n\n%s\n", n.Key, path, n.Comment(ref))
		} else {
			_, err = fmt.Fprintf(w, "added [%s] to %s, commented out: uncomment one example to use it\n", n.Key, path)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

// updateTarget is the user file `uah config update` adds to: its own
// --config, else UAH_CONFIG or the default, as every subcommand reads its
// own. A different --config on `uah` or `uah config`, which would name
// another file, is refused rather than ignored; one equal to UAH_CONFIG's
// value is that value, which urfave/cli sets on each of them.
func updateTarget(cmd *cli.Command) (string, error) {
	if parent := cmd.Lineage(); len(parent) > 1 && parent[1].Bool(flagJSON) {
		return "", cli.Exit("config update prints text: --json is uah config's own", exitUsage)
	}
	file := cmd.String(flagConfig)
	if file == "" {
		return "", cli.Exit("--config names no file", exitUsage)
	}
	env := os.Getenv(home.EnvConfig)
	for _, v := range parentConfigs(cmd) {
		switch {
		case v == "":
			return "", cli.Exit("--config names no file", exitUsage) // typed empty: UAH_CONFIG's empty value sets nothing
		case v == env:
		case folderShaped(v) || folderShaped(linkEnd(v)):
			return "", cli.Exit(fmt.Sprintf("--config %s names a folder, not a file", v), exitUsage)
		case v != file && !sameFile(v, file):
			return "", cli.Exit(fmt.Sprintf("--config %s before update names another file than %s: put --config after update", v, file), exitUsage)
		}
	}

	return file, nil
}

// parentConfigs are the values of the set --config flags of the commands
// above cmd: typed, or UAH_CONFIG's.
func parentConfigs(cmd *cli.Command) []string {
	var out []string
	for _, c := range cmd.Lineage()[1:] {
		for _, f := range c.Flags {
			if v, ok := f.Get().(string); ok && slices.Contains(f.Names(), flagConfig) && f.IsSet() {
				out = append(out, v)
			}
		}
	}

	return out
}

// updateCommand is how to run `uah config update` on userFile from any
// shell: with --config and the file's absolute path, as UAH_CONFIG,
// UAH_HOME, or HOME may have been set for the session's command only; ""
// when that path cannot be made or needs quoting, which bash, fish, and
// PowerShell each do their own way.
func updateCommand(userFile string) string {
	if !filepath.IsAbs(userFile) {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		userFile = cwd + string(filepath.Separator) + userFile // not cleaned: a .. after a link is the link's
	}
	if quoted := shellQuote(userFile); quoted != userFile {
		return "" // a space, a quote, a tab, a backslash: no one form copies safely
	}
	if info, err := os.Stat(userFile); err == nil && !info.Mode().IsRegular() {
		return "" // such as /dev/null: uah config update leaves it alone
	}
	if config.CheckUserFilePath(userFile) != nil {
		return "" // a folder, or a path only the write could resolve: uah config update refuses it
	}

	return "uah config update --config " + userFile
}

// shellQuote quotes s for a POSIX shell when it needs it; a leading ~ is
// quoted too, so the shell does not expand a path that names a folder ~.
func shellQuote(s string) string {
	plain := func(r rune) bool {
		return r == '/' || r == '.' || r == '-' || r == '_' ||
			(r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
	}
	if s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !plain(r) }) {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// docsRef is the git ref of the docs for version: a source build's commit
// (a pseudo-version's revision), a release's tag, a prerelease's too, else
// main. A +dirty build links its commit, without the changes on top.
func docsRef(version string) string {
	version = strings.TrimSuffix(version, semver.Build(version))
	if module.IsPseudoVersion(version) {
		if rev, err := module.PseudoVersionRev(version); err == nil {
			return rev
		}
	}
	if semver.IsValid(version) {
		return version
	}

	return "main"
}

// sameFile reports whether a and b, spelled differently, name one file:
// the same existing file, or, for one not made yet, the same name in the
// same existing folder.
func sameFile(a, b string) bool { return sameFileDepth(a, b, 64) }

// sameFileDepth is sameFile, going up at most depth folders: a symlink loop
// ends there, as any error but a missing file ends it at once.
func sameFileDepth(a, b string, depth int) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	switch {
	case errA == nil && errB == nil:
		return os.SameFile(ia, ib)
	case !errors.Is(errA, fs.ErrNotExist) || !errors.Is(errB, fs.ErrNotExist) || depth == 0:
		return false
	}
	a, b = dropDots(linkEnd(a)), dropDots(linkEnd(b))
	da, db := cmp.Or(dirOf(a), "."), cmp.Or(dirOf(b), ".")

	return filepath.Base(a) == filepath.Base(b) && da != a && db != b && sameFileDepth(da, db, depth-1)
}

// dropDots drops path's "." and empty elements, which name nothing, as a
// doubled separator does not, and keeps its "..", which may follow a link.
func dropDots(path string) string {
	sep := string(filepath.Separator)
	parts := strings.Split(path, sep)
	kept := parts[:0]
	for i, p := range parts {
		if (p != "." && p != "") || i == 0 {
			kept = append(kept, p)
		}
	}

	return strings.Join(kept, sep)
}

// linkEnd is where a chain of symlinks at path ends, as written, for a link
// to a file not made yet; path itself when it is no link.
func linkEnd(path string) string {
	for range 40 {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			return path
		}
		target, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if dir := dirOf(path); dir != "" && !filepath.IsAbs(target) {
			target = dir + string(filepath.Separator) + target
		}
		path = target
	}

	return path
}

// dirOf is path without its last element, as written, so a .. after a
// link stays the link's.
func dirOf(path string) string {
	if dir, _, ok := strings.CutLast(path, string(filepath.Separator)); ok {
		return cmp.Or(dir, string(filepath.Separator))
	}

	return ""
}

// folderShaped reports whether path's last element names a folder: empty,
// as after a trailing separator, ".", or "..".
func folderShaped(path string) bool {
	last := path[strings.LastIndex(path, string(filepath.Separator))+1:]

	return last == "" || last == "." || last == ".."
}
