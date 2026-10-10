package systemskills_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/systemskills"
)

// TestInstall writes each skill, leaves a file that is current alone,
// rewrites one that changed, and removes a skill uah no longer ships.
func TestInstall(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills", systemskills.DirName)
	require.NoError(t, systemskills.Install(root))
	names := systemskills.Names()
	require.NotEmpty(t, names)
	for _, name := range names {
		want, err := systemskills.File(name)
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(root, name, "SKILL.md"))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got))
	}

	file := filepath.Join(root, names[0], "SKILL.md")
	past := mtime(t, file).Add(-time.Hour)
	require.NoError(t, os.Chtimes(file, past, past))
	require.NoError(t, systemskills.Install(root))
	assert.Equal(t, past, mtime(t, file), "a current file is not written again")

	require.NoError(t, os.WriteFile(file, []byte("edited\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "retired"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "retired", "SKILL.md"), []byte("old\n"), 0o600))
	require.NoError(t, systemskills.Install(root))
	want, err := systemskills.File(names[0])
	require.NoError(t, err)
	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "uah rewrites its own copy")
	assert.NoDirExists(t, filepath.Join(root, "retired"))
	var files []string
	require.NoError(t, filepath.WalkDir(filepath.Join(root, names[0]), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(filepath.Join(root, names[0]), p)
			files = append(files, filepath.ToSlash(rel))
		}

		return err
	}))
	assert.ElementsMatch(t, []string{"SKILL.md", "agents/openai.yaml"}, files, "the skill's files, and no temporary file left")
}

// TestSkillFrontMatter: each skill names itself as its folder does and
// has a description, as the runner's parser requires.
func TestSkillFrontMatter(t *testing.T) {
	for _, name := range systemskills.Names() {
		data, err := systemskills.File(name)
		require.NoError(t, err)
		head, _, ok := strings.Cut(strings.TrimPrefix(string(data), "---\n"), "\n---\n")
		require.True(t, ok, "%s has front matter", name)
		assert.Contains(t, head, "name: "+name+"\n")
		desc := regexp.MustCompile(`(?m)^description: (.+)$`).FindStringSubmatch(head)
		require.Len(t, desc, 2, "%s has a description", name)
		assert.LessOrEqual(t, len(desc[1]), 1024)
	}
}

// TestCustomizationSkillMatchesTheSchema keeps the uah-customization skill
// from drifting: it names every front matter key, when key, shell family,
// placeholder, and hook event the code knows, its key table names no
// other key, and its example module parses.
func TestCustomizationSkillMatchesTheSchema(t *testing.T) {
	data, err := systemskills.File("uah-customization")
	require.NoError(t, err)
	text := string(data)

	keys := yamlKeys(reflect.TypeFor[contextprep.FrontMatter]())
	whenKeys := yamlKeys(reflect.TypeFor[contextprep.When]())
	for _, k := range append(append(keys, whenKeys...), contextprep.Shells...) {
		assert.Contains(t, text, "`"+k+"`", "the skill names %s", k)
	}
	for _, v := range []string{"read-only", "workspace-write", "none", "main", "subagent"} {
		assert.Contains(t, text, "`"+v+"`", "the skill names the when value %s", v)
	}
	for _, p := range contextprep.Placeholders {
		assert.Contains(t, text, "`{{"+p+"}}`", "the skill names the placeholder %s", p)
	}
	for _, e := range hooks.Events {
		assert.Contains(t, text, "`"+string(e)+"`", "the skill names the hook event %s", e)
	}

	rows := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|").FindAllStringSubmatch(text, -1)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		assert.Contains(t, keys, r[1], "the key table names only module keys")
	}

	example := regexp.MustCompile("(?s)```markdown\n(.*?)```").FindStringSubmatch(text)
	require.Len(t, example, 2, "the skill has an example module")
	id := regexp.MustCompile(`(?m)^id: (\S+)$`).FindStringSubmatch(example[1])
	require.Len(t, id, 2)
	fm, _, err := contextprep.ParseModule(id[1], []byte(example[1]))
	require.NoError(t, err, "the example module parses")
	require.NotNil(t, fm.Enabled)
}

// yamlKeys are a struct's yaml keys.
func yamlKeys(typ reflect.Type) []string {
	var out []string
	for f := range typ.Fields() {
		if k, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); k != "" {
			out = append(out, k)
		}
	}

	return out
}

func mtime(t *testing.T, file string) time.Time {
	t.Helper()
	info, err := os.Stat(file)
	require.NoError(t, err)

	return info.ModTime()
}
