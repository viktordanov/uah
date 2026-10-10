// Package systemskills holds the skills built into uah, such as the one that
// explains context preparation and uah's other customization points. The
// runner's SkillUse tool reads a skill from a file, so Install writes them
// under ~/.uah/skills/.system, as Codex writes its system skills under
// $CODEX_HOME/skills/.system; the engine lists them after every other skill
// folder, so a user's or a project's skill of the same name wins.
package systemskills

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// skillsFS holds the skills, each skills/<name>/SKILL.md.
//
//go:embed skills
var skillsFS embed.FS

// DirName is the folder under a skills folder that holds the system skills.
const DirName = ".system"

// Names are the system skills' names, in order.
func Names() []string {
	entries, _ := fs.ReadDir(skillsFS, "skills")
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}

	return out
}

// File is the SKILL.md of the system skill name, as uah ships it.
func File(name string) ([]byte, error) {
	data, err := skillsFS.ReadFile(path.Join("skills", name, "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("no system skill %s: %w", name, err)
	}

	return data, nil
}

// Install writes each system skill's files to <root>/<name>/, its SKILL.md
// and its agents/openai.yaml (the name and description menus show), only
// where a file differs, through a temporary file and a rename, so sessions
// that start at once never read half a file. It removes the folders under
// root of skills uah no longer ships. root is uah's own: uah rewrites it.
func Install(root string) error {
	names := Names()
	var errs []error
	for _, name := range names {
		err := fs.WalkDir(skillsFS, path.Join("skills", name), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := skillsFS.ReadFile(p)
			if err != nil {
				return err
			}

			return writeIfChanged(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, "skills/"))), data)
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() && !slices.Contains(names, e.Name()) {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				errs = append(errs, fmt.Errorf("failed to remove the old system skill %s: %w", e.Name(), err))
			}
		}
	}

	return errors.Join(errs...)
}

// writeIfChanged writes data to file unless the file already has it.
func writeIfChanged(file string, data []byte) error {
	if old, err := os.ReadFile(file); err == nil && bytes.Equal(old, data) {
		return nil
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", file, err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("failed to write %s: %w", file, err)
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		_ = os.Remove(tmp.Name())

		return fmt.Errorf("failed to write %s: %w", file, err)
	}

	return nil
}
