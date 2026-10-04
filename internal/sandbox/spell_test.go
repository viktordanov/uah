package sandbox_test

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// spellings makes other spellings of one existing path: each letter in
// another case where the file system ignores case, and the path through a
// symlink to one of its directories.
type spellings struct {
	rng  *rand.Rand
	fold bool
	// base is where letters change case, and the first name of the path;
	// the long temporary directory between them keeps its spelling, so a
	// variant does not list it again.
	base string
	// links are symlinks by the directory they lead to.
	links map[string]string
}

func (s spellings) of(path string) string {
	for target, link := range s.links {
		if s.rng.IntN(2) == 0 && (path == target || strings.HasPrefix(path, target+string(filepath.Separator))) {
			path = link + strings.TrimPrefix(path, target)
		}
	}
	if !s.fold {
		return path
	}
	rest, ok := strings.CutPrefix(path, s.base)
	if !ok {
		return path
	}
	head := s.base
	if first, after, ok := strings.Cut(strings.TrimPrefix(head, "/"), "/"); ok && s.rng.IntN(2) == 0 {
		head = "/" + strings.ToUpper(first) + "/" + after
	}
	b := []rune(rest)
	for i, r := range b {
		if s.rng.IntN(2) == 0 {
			if unicode.IsUpper(r) {
				b[i] = unicode.ToLower(r)
			} else {
				b[i] = unicode.ToUpper(r)
			}
		}
	}

	return head + string(b)
}

// TestSpelling_OneDirectoryOneString pins the invariant every policy rests
// on: whatever spelling a root, a ReadOnly path, a grant, or a checked
// path comes in, by case or through a symlink, the policy gives the same
// roots, protected paths, Seatbelt profile, bubblewrap arguments, and
// decisions as for the paths as the file system spells them.
func TestSpelling_OneDirectoryOneString(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Outside the shared temporary directory, which holds thousands of
	// entries that each decision would list again, and with $TMPDIR, a
	// root of every policy, pointed at a small directory of its own.
	base, err := filepath.EvalSymlinks(harnesstest.OutsideDir(t, "uah-spelling-"))
	require.NoError(t, err)
	t.Setenv("TMPDIR", base)
	base = filepath.Join(base, "Base")
	ws := filepath.Join(base, "Work", "Space")
	extra := filepath.Join(base, "Extra", "Root")
	grant := filepath.Join(base, "Grant", "Tree")
	scripts := filepath.Join(base, "State", "Sandbox")
	temp := filepath.Join(base, "State", "Tmp")
	for _, d := range []string{filepath.Join(ws, ".git"), filepath.Join(extra, ".Agents"), filepath.Join(grant, "Src"), scripts, temp} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(grant, ".git"), []byte("gitdir: "+filepath.Join(ws, ".git", "worktrees", "tree")+"\n"), 0o644))
	link := filepath.Join(base, "Link")
	require.NoError(t, os.Symlink(filepath.Join(base, "Extra"), link))
	_, err = os.Stat(strings.ToUpper(base))
	gen := spellings{rng: rand.New(rand.NewPCG(1, 2)), fold: err == nil, base: filepath.Dir(base), links: map[string]string{filepath.Join(base, "Extra"): link}}

	policy := func(spell func(string) string) sandbox.Policy {
		return sandbox.Policy{
			Mode: sandbox.WorkspaceWrite, Workspace: spell(ws), WritableRoots: []string{spell(extra), spell(grant)},
			ReadOnly: []string{spell(scripts)}, TempDir: spell(temp),
		}
	}
	want := policy(func(p string) string { return p })
	wantProfile, wantParams := sandbox.SeatbeltProfile(want)
	probes := []string{
		filepath.Join(ws, "a.go"), filepath.Join(ws, ".git", "config"), filepath.Join(extra, ".Agents", "x"),
		filepath.Join(extra, "new", "dir", "f"), filepath.Join(grant, ".git"), filepath.Join(grant, "src", "f"),
		filepath.Join(scripts, "sh-1"), filepath.Join(base, "outside"),
	}
	for i := range 20 {
		got := policy(gen.of)
		assert.Equal(t, want.Writable(), got.Writable(), "variant %d roots", i)
		profile, params := sandbox.SeatbeltProfile(got)
		assert.Equal(t, wantProfile, profile, "variant %d profile", i)
		assert.Equal(t, wantParams, params, "variant %d profile parameters", i)
		assert.Equal(t, sandbox.BwrapArgs(want), sandbox.BwrapArgs(got), "variant %d bubblewrap", i)
		for _, probe := range probes {
			spelled := gen.of(probe)
			assert.Equal(t, want.CanWrite(probe), got.CanWrite(spelled), "variant %d CanWrite %s", i, spelled)
			assert.Equal(t, want.Protects(probe), got.Protects(spelled), "variant %d Protects %s", i, spelled)
			assert.Equal(t, want.InRoot(probe), got.InRoot(spelled), "variant %d InRoot %s", i, spelled)
			assert.Equal(t, want.Holds(probe), got.Holds(spelled), "variant %d Holds %s", i, spelled)
		}

		g := sandbox.NewGrants(ws, nil)
		if g.Add(gen.of(grant), sandbox.GrantApproved) {
			assert.Equal(t, []string{grant}, g.Roots(), "variant %d grant", i)
		}
		spelled, ok := sandbox.Canonical(gen.of(filepath.Join(grant, "Src")) + "/new.go")
		assert.True(t, ok)
		assert.Equal(t, filepath.Join(grant, "Src", "new.go"), spelled, "variant %d Canonical: the existing part as listed, the rest as given", i)
	}
}

// TestSpelling_FailsClosed: a root or a ReadOnly path that does not spell,
// because a directory on the way cannot be listed, leaves the policy
// without that root, or without any root, and a checked path that does
// not spell is not writable and is protected.
func TestSpelling_FailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists any directory")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	locked := filepath.Join(base, "locked")
	root := filepath.Join(locked, "Root")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.Chmod(locked, 0o111))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ws := filepath.Join(base, "ws")
	require.NoError(t, os.MkdirAll(ws, 0o755))

	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, WritableRoots: []string{root}}
	assert.NotContains(t, p.Writable(), root)
	assert.Contains(t, p.Writable(), ws)
	assert.False(t, p.CanWrite(filepath.Join(root, "f")))
	assert.True(t, p.Protects(filepath.Join(root, "f")))

	p.WritableRoots, p.ReadOnly = nil, []string{filepath.Join(root, "x")}
	assert.Empty(t, p.Writable(), "a ReadOnly path that does not spell might be anywhere")
}

// TestSpelling_ProtectedSymlink: a protected name that is a symlink, also
// one to nothing yet, protects where it leads, in every root's rule: a
// .git that leads into another writable root keeps that place read-only.
func TestSpelling_ProtectedSymlink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ws, other := filepath.Join(base, "ws"), filepath.Join(base, "other")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	require.NoError(t, os.MkdirAll(other, 0o755))
	meta := filepath.Join(other, "meta")
	require.NoError(t, os.Symlink(meta, filepath.Join(ws, ".git")))
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, WritableRoots: []string{other}}

	assert.False(t, p.CanWrite(filepath.Join(meta, "hooks", "pre-commit")), "dangling: where it leads is protected")
	assert.True(t, p.Protects(meta))
	_, params := sandbox.SeatbeltProfile(p)
	assert.Contains(t, strings.Join(params, "\n"), "="+meta+"\n", "an exclusion in the profile")
	assert.True(t, p.CanWrite(filepath.Join(other, "x")))
}

// TestSpelling_NoCacheBetweenDecisions: a directory renamed between two
// decisions is spelled as it is now.
func TestSpelling_NoCacheBetweenDecisions(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(base, "Tree"), 0o755))
	got, ok := sandbox.Canonical(filepath.Join(base, "Tree"))
	require.True(t, ok)
	require.Equal(t, filepath.Join(base, "Tree"), got)
	require.NoError(t, os.Rename(filepath.Join(base, "Tree"), filepath.Join(base, "tree")))
	got, ok = sandbox.Canonical(filepath.Join(base, "tree"))
	assert.True(t, ok)
	assert.Equal(t, filepath.Join(base, "tree"), got)
}

// TestSpelling_FailedRootTakesItsHolder: a root left out because its
// protected paths do not spell takes out a root that holds it too, which
// would otherwise write its .git; and a name that may exist, behind a
// directory that cannot be searched, does not spell.
func TestSpelling_FailedRootTakesItsHolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	parent := filepath.Join(base, "parent")
	ws := filepath.Join(parent, "ws")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, ".git"), 0o755))
	require.NoError(t, os.Chmod(ws, 0o311))
	t.Cleanup(func() { _ = os.Chmod(ws, 0o755) })
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, WritableRoots: []string{parent}}

	assert.NotContains(t, p.Writable(), parent)
	assert.NotContains(t, p.Writable(), ws)
	assert.False(t, p.CanWrite(filepath.Join(ws, ".git", "config")))

	hidden := filepath.Join(base, "hidden")
	require.NoError(t, os.MkdirAll(hidden, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hidden, "f"), nil, 0o644))
	require.NoError(t, os.Chmod(hidden, 0o600))
	t.Cleanup(func() { _ = os.Chmod(hidden, 0o755) })
	_, ok := sandbox.Canonical(filepath.Join(hidden, "f"))
	assert.False(t, ok, "a lookup that fails other than for a missing name")
}

// TestBwrap_ProtectedTargetAfterEveryBind: a protected symlink that leads
// into another writable root stays read-only in bubblewrap too: its
// read-only bind comes after that root's writable bind.
func TestBwrap_ProtectedTargetAfterEveryBind(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ws := filepath.Join(base, "ws")
	meta := filepath.Join(ws, "metadata")
	require.NoError(t, os.MkdirAll(meta, 0o755))
	require.NoError(t, os.Symlink(meta, filepath.Join(ws, ".codex")))
	p := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: ws, WritableRoots: []string{meta}}

	args := strings.Join(sandbox.BwrapArgs(p), "\n")
	bind := strings.LastIndex(args, "--bind\n"+meta+"\n")
	ro := strings.LastIndex(args, "--ro-bind\n"+meta+"\n")
	require.GreaterOrEqual(t, bind, 0)
	assert.Greater(t, ro, bind, args)
	assert.False(t, p.CanWrite(filepath.Join(meta, "x")))
}
