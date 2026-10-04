package contextprep_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/contextprep"
)

func module(front, body string) []byte { return []byte("---\n" + front + "---\n" + body) }

// TestParseModule pins the front matter's strict schema, the size caps, and
// the placeholders.
func TestParseModule(t *testing.T) {
	t.Parallel()
	fm, body, err := contextprep.ParseModule("go", module(
		"id: go\ndescription: Go notes\nwhen: {shell: [fish], os: [darwin], sandbox: [read-only], agent: [main, subagent], network: false}\n"+
			"check: [go, version]\nfiles: [go.mod, cmd/*/main.go]\nenabled: false\n",
		"\nCache in {{tmpdir}}.\n"))
	require.NoError(t, err)
	assert.Equal(t, "Cache in {{tmpdir}}.", body)
	assert.Equal(t, []string{"go", "version"}, fm.Check)
	assert.Equal(t, []string{"fish"}, fm.When.Shell)
	require.NotNil(t, fm.Enabled)
	assert.False(t, *fm.Enabled)

	for _, tc := range []struct{ name, front, body, err string }{
		{"no front matter", "", "", "does not start with front matter"},
		{"unknown key", "id: go\ndescription: x\nrun: rm -rf /\n", "", "field run not found"},
		{"unknown when key", "id: go\ndescription: x\nwhen: {host: [x]}\n", "", "field host not found"},
		{"a string for argv", "id: go\ndescription: x\ncheck: go version\n", "", "cannot unmarshal"},
		{"id not the file's name", "id: other\ndescription: x\n", "", `id "other" is not the file's name`},
		{"bad id", "id: Go!\ndescription: x\n", "", "is not lower-case"},
		{"no description", "id: go\n", "", "description must be"},
		{"unknown shell", "id: go\ndescription: x\nwhen: {shell: [bash5]}\n", "", `when.shell: "bash5"`},
		{"unknown sandbox", "id: go\ndescription: x\nwhen: {sandbox: [yolo]}\n", "", `when.sandbox: "yolo"`},
		{"unknown shell source", "id: go\ndescription: x\nwhen: {shell_source: [path]}\n", "", `when.shell_source: "path"`},
		{"relative check path", "id: go\ndescription: x\ncheck: [./run.sh]\n", "", "a name on PATH or an absolute path"},
		{"empty check", "id: go\ndescription: x\ncheck: []\n", "", "check must have"},
		{"files outside", "id: go\ndescription: x\nfiles: [../secret]\n", "", "inside the workspace"},
		{"absolute files", "id: go\ndescription: x\nfiles: [/etc/passwd]\n", "", "relative path"},
		{"files pattern", "id: go\ndescription: x\nfiles: ['**/go.mod']\n", "", "only * as a pattern"},
		{"files class", "id: go\ndescription: x\nfiles: ['go.[m]od']\n", "", "only * as a pattern"},
		{"unknown placeholder", "id: go\ndescription: x\n", "Run {{command}} now.", "unknown placeholder"},
		{"placeholder with spaces", "id: go\ndescription: x\n", "In {{ tmpdir }}.", "unknown placeholder"},
		{"stray braces", "id: go\ndescription: x\n", "A Go template {{.Name}}.", "unknown placeholder"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(tc.front)
			if tc.front != "" {
				data = module(tc.front, tc.body)
			}
			_, _, err := contextprep.ParseModule("go", data)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.err)
		})
	}

	t.Run("oversized", func(t *testing.T) {
		_, _, err := contextprep.ParseModule("go", module("id: go\ndescription: x\n", strings.Repeat("x", contextprep.MaxModuleBytes)))
		require.ErrorContains(t, err, "over 16384")
		_, _, err = contextprep.ParseModule("go", module("id: go\ndescription: "+strings.Repeat("x", contextprep.MaxFrontMatterBytes)+"\n", ""))
		require.ErrorContains(t, err, "the front matter is")
	})
}

// TestBuiltins: every built-in module parses, each block's module exists,
// and each module but the library's belongs to one block.
func TestBuiltins(t *testing.T) {
	t.Parallel()
	ms := contextprep.Load(contextprep.Sources{})
	var paths []string
	for _, m := range contextprep.Builtins() {
		require.NoError(t, m.Err, m.Path)
		paths = append(paths, m.Path)
		if m.Source == contextprep.SourceLibrary {
			assert.False(t, ms.Enabled(m), "%s ships turned off", m.Path)
			assert.NotEmpty(t, m.Meta.Files, "%s names its project files", m.Path)
		}
	}
	for _, s := range ms.Explain(t.Context(), contextprep.Facts{}) {
		assert.NotEmpty(t, s.Block, s.Path)
	}
	assert.Contains(t, paths, "environment/fish")
	assert.Contains(t, paths, "library/go")
}

// TestRender inserts values as text: a value that looks like a placeholder
// stays as it is.
func TestRender(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	user := filepath.Join(dir, "prompts")
	write(t, filepath.Join(user, "context.d", "note.md"), string(module("id: note\ndescription: x\n", "Workspace {{workspace}}, temp {{tmpdir}}.")))
	ms := contextprep.Load(contextprep.Sources{UserDir: user})
	got := contextprep.Prepare(t.Context(), contextprep.Facts{Workspace: "/w/{{tmpdir}}", Sandbox: contextprep.Sandbox{Mode: "read-only", TempDir: "/t"}}, ms.Adapters()...)
	assert.Contains(t, got, "\n## note\nWorkspace /w/{{tmpdir}}, temp /t.\n")

	status := find(t, ms.Explain(t.Context(), contextprep.Facts{}), "context.d/note")
	assert.False(t, status.Applies)
	assert.Equal(t, "{{workspace}} has no value in this session", status.Reason)
}

// TestOverrides: a user file with a built-in's path replaces it; one that
// does not parse, or names no built-in, is listed with its error and the
// built-in stays.
func TestOverrides(t *testing.T) {
	t.Parallel()
	user := filepath.Join(t.TempDir(), "prompts")
	write(t, filepath.Join(user, "context", "environment", "fish.md"), string(module("id: fish\ndescription: mine\nwhen: {shell: [fish]}\n", "My fish notes.")))
	write(t, filepath.Join(user, "context", "os", "darwin.md"), string(module("id: darwin\ndescription: x\nbogus: 1\n", "Broken.")))
	write(t, filepath.Join(user, "context", "os", "plan9.md"), string(module("id: plan9\ndescription: x\n", "Plan 9.")))
	write(t, filepath.Join(user, "context", "library", "go.md"), string(module("id: go\ndescription: x\nenabled: true\n", "My Go notes.")))
	ms := contextprep.Load(contextprep.Sources{UserDir: user})

	got := contextprep.Environment{Modules: ms}.Prepare(t.Context(), contextprep.Facts{Shell: "/usr/bin/fish", GOOS: "darwin"})
	assert.Equal(t, "Commands run in fish (/usr/bin/fish -c) on macOS.\nMy fish notes.\nmacOS has BSD tools, not GNU: sed -i '' (not sed -i), stat -f (not -c), "+
		"date -v-1d (not -d), no grep -P (use -E or perl), no nproc (sysctl -n hw.ncpu).", got, "the broken override leaves the built-in")
	statuses := ms.Explain(t.Context(), contextprep.Facts{Shell: "/usr/bin/fish", GOOS: "darwin"})
	fish := find(t, statuses, "environment/fish")
	assert.True(t, fish.Overrides)
	assert.Equal(t, contextprep.SourceUser, fish.Source)
	assert.Contains(t, find(t, statuses, "os/darwin").Reason, "applies")
	var errs []string
	for _, s := range statuses {
		if strings.HasPrefix(s.Reason, "error: ") {
			errs = append(errs, s.Path+": "+s.Reason)
		}
	}
	assert.Len(t, errs, 2, errs)
	assert.Contains(t, strings.Join(errs, "\n"), "os/darwin: error: the front matter")
	assert.Contains(t, strings.Join(errs, "\n"), "os/plan9: error: no built-in module os/plan9 to replace")
	assert.True(t, find(t, statuses, "library/go").Applies, "a replaced library module is the user's, turned on")
}

// TestExtras: the library's modules apply once [context] modules names
// them and their files and check match; user and project modules each get
// a block; a project module is listed but unused, and its check never
// runs, until it is trusted; an id is used once.
func TestExtras(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ws, user := filepath.Join(root, "ws"), filepath.Join(root, "prompts")
	write(t, filepath.Join(ws, "go.mod"), "module x\n")
	write(t, filepath.Join(user, "context.d", "style.md"), string(module("id: style\ndescription: x\nwhen: {agent: [main]}\n", "Be terse.")))
	write(t, filepath.Join(user, "context.d", "go.md"), string(module("id: go\ndescription: x\n", "Taken.")))
	write(t, filepath.Join(ws, ".uah", "context.d", "repo.md"), string(module("id: repo\ndescription: x\ncheck: [true]\n", "Repo rules.")))
	var checks atomic.Int32
	check := func(_ context.Context, argv []string, dir string) error {
		checks.Add(1)
		assert.Equal(t, ws, dir)

		return nil
	}
	trusted := false
	src := contextprep.Sources{
		UserDir: user, Workspace: ws, Enable: []string{"go"}, Check: check,
		Trusted: func(key string) bool {
			return trusted && strings.HasPrefix(key, "#context-module context.d/repo sha256:")
		},
	}

	ms := contextprep.Load(src)
	f := contextprep.Facts{Workspace: ws, Sandbox: contextprep.Sandbox{Mode: "read-only", TempDir: "/t"}}
	got := contextprep.Prepare(t.Context(), f, ms.Adapters()...)
	assert.Contains(t, got, "\n## go\nGo: when the build cache")
	assert.Contains(t, got, "\n## style\nBe terse.")
	assert.NotContains(t, got, "Repo rules.")
	assert.NotContains(t, got, "Taken.")
	assert.Equal(t, int32(1), checks.Load(), "go's check, not the untrusted module's")
	statuses := ms.Explain(t.Context(), f)
	assert.Equal(t, "untrusted project module: run `uah context trust` to use it", find(t, statuses, "context.d/repo").Reason)
	assert.Contains(t, find(t, statuses, "context.d/go").Reason, "id go is taken by the built-in library/go")
	assert.Equal(t, int32(1), checks.Load(), "a check runs once for the modules")
	require.Len(t, ms.Untrusted(), 1)

	sub := contextprep.Prepare(t.Context(), contextprep.Facts{Workspace: ws, Subagent: true}, contextprep.Load(src).Adapters()...)
	assert.NotContains(t, sub, "Be terse.", "when.agent")

	trusted = true
	got = contextprep.Prepare(t.Context(), f, contextprep.Load(src).Adapters()...)
	assert.Contains(t, got, "\n## repo\nRepo rules.")

	write(t, filepath.Join(ws, ".uah", "context.d", "repo.md"), string(module("id: repo\ndescription: x\n", "Changed rules.")))
	got = contextprep.Prepare(t.Context(), f, contextprep.Load(src).Adapters()...)
	assert.Contains(t, got, "Changed rules.", "the test's trust ignores the hash; uah's does not")
	require.NoError(t, os.Remove(filepath.Join(ws, "go.mod")))
	assert.Contains(t, find(t, contextprep.Load(src).Explain(t.Context(), f), "library/go").Reason, "files: none of go.mod, go.work in the workspace")
}

// TestTrustKey changes with the module's content.
func TestTrustKey(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	path := filepath.Join(ws, ".uah", "context.d", "repo.md")
	write(t, path, string(module("id: repo\ndescription: x\n", "One.")))
	key := contextprep.TrustKey(contextprep.Load(contextprep.Sources{Workspace: ws}).Untrusted()[0])
	write(t, path, string(module("id: repo\ndescription: x\n", "Two.")))
	assert.NotEqual(t, key, contextprep.TrustKey(contextprep.Load(contextprep.Sources{Workspace: ws}).Untrusted()[0]))
}

// TestExecChecker runs argv without a shell: ; and $(…) reach the command
// as they are. It needs a sandbox wrapper, and stops a check at its
// timeout.
func TestExecChecker(t *testing.T) {
	t.Parallel()
	for _, bin := range []string{"test", "ls", "sleep"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("no " + bin)
		}
	}
	dir := t.TempDir()
	var wrapped [][]string
	wrap := func(argv []string) ([]string, error) {
		wrapped = append(wrapped, argv)

		return argv, nil
	}
	check := contextprep.ExecChecker(wrap, []string{"PATH=" + os.Getenv("PATH")})
	ctx := t.Context()

	require.NoError(t, check(ctx, []string{"test", "$(echo x)", "=", "$(echo x)"}, dir), "the words compare as they are")
	require.Error(t, check(ctx, []string{"test", "$(echo x)", "=", "x"}, dir), "no substitution")
	require.Error(t, check(ctx, []string{"ls", "; touch pwned", "$(touch pwned2)"}, dir))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing ran the ; or the $(…)")
	assert.True(t, filepath.IsAbs(wrapped[0][0]), "argv[0] is found on PATH before the sandbox wraps it: %v", wrapped[0])
	assert.Equal(t, "$(echo x)", wrapped[0][1])

	// A caller's deadline stops the check as CheckTimeout does, on the same
	// path, without waiting 2 s.
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = check(short, []string{"sleep", "10"}, dir)
	require.ErrorContains(t, err, "timed out")
	assert.Less(t, time.Since(start), 2*time.Second)

	require.ErrorIs(t, contextprep.ExecChecker(nil, nil)(ctx, []string{"true"}, dir), contextprep.ErrNoSandbox, "never unsandboxed")
	require.Error(t, check(ctx, []string{"no-such-command-uah"}, dir))
}

// TestExplain lists the built-ins in block order with why each applies.
func TestExplain(t *testing.T) {
	t.Parallel()
	statuses := contextprep.Load(contextprep.Sources{}).Explain(t.Context(), contextprep.Facts{Shell: "/bin/zsh", GOOS: "linux"})
	zsh, fish := find(t, statuses, "environment/zsh"), find(t, statuses, "environment/fish")
	assert.True(t, zsh.Applies)
	assert.Equal(t, "environment", zsh.Block)
	assert.Equal(t, "when.shell: zsh is not fish", fish.Reason)
	assert.Equal(t, "when.os: linux is not darwin", find(t, statuses, "os/darwin").Reason)
	assert.Equal(t, "when.sandbox: none is not read-only", find(t, statuses, "sandbox/read-only").Reason)
	goStatus := find(t, statuses, "library/go")
	assert.False(t, goStatus.Enabled)
	assert.Equal(t, "go", goStatus.Block)
	assert.Equal(t, "disabled (enabled: false; [context] modules can turn it on)", goStatus.Reason)
	paths := make([]string, 0, len(statuses))
	for _, s := range statuses {
		paths = append(paths, s.Path)
	}
	assert.Less(t, slices.Index(paths, "environment/intro"), slices.Index(paths, "sandbox/read-only"))
	assert.Less(t, slices.Index(paths, "harness/output"), slices.Index(paths, "library/go"))
}

func find(t *testing.T, statuses []contextprep.Status, path string) contextprep.Status {
	t.Helper()
	for _, s := range statuses {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("no module %s", path)

	return contextprep.Status{}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(text), 0o644))
}

// TestUserLibrary: a user's module with enabled: false is a library module
// of the user's own: off until [context] modules names its id, then a block
// of its own, while the built-ins stay as uah ships them.
func TestUserLibrary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ws, user := filepath.Join(root, "ws"), filepath.Join(root, "prompts")
	write(t, filepath.Join(ws, "go.mod"), "module x\n")
	write(t, filepath.Join(user, "context.d", "my-go.md"), string(module("id: my-go\ndescription: x\nfiles: [go.mod]\nenabled: false\n", "Run mage test.")))
	f := contextprep.Facts{Workspace: ws}

	off := contextprep.Load(contextprep.Sources{UserDir: user})
	assert.NotContains(t, contextprep.Prepare(t.Context(), f, off.Adapters()...), "mage")
	assert.Contains(t, find(t, off.Explain(t.Context(), f), "context.d/my-go").Reason, "disabled")

	on := contextprep.Load(contextprep.Sources{UserDir: user, Enable: []string{"my-go"}})
	got := contextprep.Prepare(t.Context(), f, on.Adapters()...)
	assert.Contains(t, got, "\n## my-go\nRun mage test.")
	assert.NotContains(t, got, "## go\n", "the library's go stays off")
	for _, s := range on.Explain(t.Context(), f) {
		assert.False(t, s.Overrides, "%s: nothing replaces a built-in", s.Path)
	}
}

// TestPinnedOverrides: Overrides lists the user's files under context/,
// flags a copy identical to the built-in as pinned, and `uah context`'s
// listing says so too.
func TestPinnedOverrides(t *testing.T) {
	t.Parallel()
	user := filepath.Join(t.TempDir(), "prompts")
	darwin, err := contextprep.BuiltinFile("os/darwin")
	require.NoError(t, err)
	goLib, err := contextprep.BuiltinFile("library/go")
	require.NoError(t, err)
	write(t, filepath.Join(user, "context", "os", "darwin.md"), darwin)
	write(t, filepath.Join(user, "context", "library", "go.md"), goLib)
	write(t, filepath.Join(user, "context", "environment", "fish.md"), string(module("id: fish\ndescription: mine\nwhen: {shell: [fish]}\n", "My fish notes.")))
	write(t, filepath.Join(user, "context", "os", "plan9.md"), string(module("id: plan9\ndescription: x\n", "Plan 9.")))

	got := contextprep.Overrides(user)
	require.Len(t, got, 4)
	byPath := map[string]contextprep.Override{}
	for _, o := range got {
		byPath[o.Path] = o
	}
	assert.True(t, byPath["os/darwin"].Pinned)
	assert.True(t, byPath["library/go"].Pinned, "the library's too")
	assert.False(t, byPath["environment/fish"].Pinned)
	require.NoError(t, byPath["environment/fish"].Err)
	assert.False(t, byPath["os/plan9"].Pinned)
	require.Error(t, byPath["os/plan9"].Err)
	assert.Equal(t, filepath.Join(user, "context", "os", "darwin.md"), byPath["os/darwin"].File)

	statuses := contextprep.Load(contextprep.Sources{UserDir: user}).Explain(t.Context(), contextprep.Facts{GOOS: "darwin"})
	assert.True(t, find(t, statuses, "os/darwin").Pinned)
	assert.False(t, find(t, statuses, "environment/fish").Pinned)
	assert.Empty(t, contextprep.Overrides(filepath.Join(t.TempDir(), "none")))
}

// TestUserGuide keeps docs/context-preparation.md complete: its library
// table has a row for each library module, and it names every front
// matter key, when key, and placeholder.
func TestUserGuide(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "context-preparation.md"))
	require.NoError(t, err)
	guide := string(data)
	for _, m := range contextprep.Builtins() {
		if m.Source == contextprep.SourceLibrary {
			assert.Contains(t, guide, "| `"+m.Meta.ID+"` |", "the library table has %s", m.Meta.ID)
			assert.Contains(t, guide, "`[context] modules = [\""+m.Meta.ID+"\"]`")
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[contextprep.FrontMatter](), reflect.TypeFor[contextprep.When]()} {
		for f := range typ.Fields() {
			key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			assert.Contains(t, guide, "| `"+key+"` |", "the guide's tables have the key %s", key)
		}
	}
	for _, p := range contextprep.Placeholders {
		assert.Contains(t, guide, "| `{{"+p+"}}` |")
	}
}
