package session_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/harnesstest"
)

// grantRepo is a repository at <base>/main with a linked worktree at
// <base>/bar, and the worktree's real path.
func grantRepo(t *testing.T) (main, bar string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	require.NoError(t, os.MkdirAll(main, 0o755))
	harnesstest.Git(t, main, "init", "-q")
	harnesstest.Git(t, main, "commit", "-q", "--allow-empty", "-m", "first")
	harnesstest.Git(t, main, "worktree", "add", "-q", "-b", "bar", filepath.Join(base, "bar"))
	bar, err := filepath.EvalSymlinks(filepath.Join(base, "bar"))
	require.NoError(t, err)

	return main, bar
}

func noticeMatching(text string) func(core.Event) bool {
	return func(e core.Event) bool {
		n, ok := e.(session.Notice)

		return ok && strings.Contains(n.Message, text)
	}
}

// TestSession_GrantsKeptAndCheckedOnResume pins the grants' life: a
// session gives its runs its grants, shows each new one, and keeps each
// worktree grant in its sidecar, not an approved one; a resume gets back
// each that still holds, with a notice, and drops one that no longer
// does, from the sidecar too.
func TestSession_GrantsKeptAndCheckedOnResume(t *testing.T) {
	dir := t.TempDir()
	main, bar := grantRepo(t)
	s1 := settings()
	s1.Workspace = main
	open := func(id string) (*session.Session, *fakeEngine) {
		eng := newFakeEngine(fakeCaps{})
		s, err := session.Open(context.Background(), eng, session.Options{ID: id, Resumed: id != "", Settings: s1, SessionsDir: dir, Source: session.SourceTUI})
		require.NoError(t, err)
		t.Cleanup(func() { _ = s.Close() })

		return s, eng
	}

	s, eng := open("")
	h := &harness{t: t, eng: eng, s: s}
	_, err := s.Submit("go")
	require.NoError(t, err)
	run := h.nextRun()
	require.NotNil(t, run.opts.Grants, "the run gets the session's grants")
	require.True(t, run.opts.Grants.Add(bar, sandbox.GrantWorktree))
	h.until(noticeMatching("writable for this session: " + bar + " (a git worktree of this repository)"))
	approved, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.True(t, run.opts.Grants.Add(approved, sandbox.GrantApproved))
	h.until(noticeMatching("writable for this session: " + approved + " (your approval)"))
	sc, _, err := session.ReadSidecar(dir, s.ID())
	require.NoError(t, err)
	assert.Equal(t, []sandbox.Grant{{Path: bar, Reason: sandbox.GrantWorktree}}, sc.Grants, "an approval is not kept")
	id := s.ID()
	require.NoError(t, s.Close())

	s, eng = open(id)
	h = &harness{t: t, eng: eng, s: s}
	h.until(noticeMatching("writable for this session: " + bar + " (a git worktree of this repository), kept from before the resume"))
	_, err = s.Submit("again")
	require.NoError(t, err)
	assert.Equal(t, []string{bar}, h.nextRun().opts.Grants.Roots())
	require.NoError(t, s.Close())

	harnesstest.Git(t, main, "worktree", "remove", bar)
	require.NoError(t, os.MkdirAll(bar, 0o755))
	s, eng = open(id)
	h = &harness{t: t, eng: eng, s: s}
	h.until(noticeMatching("no longer writable for this session: " + bar))
	sc, _, err = session.ReadSidecar(dir, id)
	require.NoError(t, err)
	assert.Empty(t, sc.Grants, "dropped from the sidecar")
	_, err = s.Submit("once more")
	require.NoError(t, err)
	assert.Empty(t, h.nextRun().opts.Grants.Roots())
}

// TestSession_SharedGrants: a session given grants, as a subagent is
// given its parent's, passes them to its runs and keeps nothing itself.
func TestSession_SharedGrants(t *testing.T) {
	dir := t.TempDir()
	shared := sandbox.NewGrants("/workspace", nil)
	eng := newFakeEngine(fakeCaps{})
	s, err := session.Open(context.Background(), eng, session.Options{Settings: settings(), SessionsDir: dir, Source: session.SourceSubagent, Grants: shared})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	h := &harness{t: t, eng: eng, s: s}
	_, err = s.Submit("go")
	require.NoError(t, err)
	run := h.nextRun()
	assert.Same(t, shared, run.opts.Grants)

	granted := t.TempDir()
	granted, err = filepath.EvalSymlinks(granted)
	require.NoError(t, err)
	require.True(t, run.opts.Grants.Add(granted, sandbox.GrantApproved))
	sc, _, err := session.ReadSidecar(dir, s.ID())
	require.NoError(t, err)
	assert.Empty(t, sc.Grants)
}
