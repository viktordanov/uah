package embedded

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/viktordanov/uah/internal/sandbox"
)

// grantWorktrees adds to the session's grants each worktree of the
// workspace's repository that holds one of the paths an escalated command
// names, outside the writable roots, and reports whether it added one. It
// grants only in the workspace-write sandbox, where a writable root
// applies; a grant asks no one, since such a worktree is a checkout of the
// session's own repository (sandbox.Worktrees).
func (b sandboxedBash) grantWorktrees(mode sandbox.Mode, command string) bool {
	if b.grants == nil || mode != sandbox.WorkspaceWrite {
		return false
	}

	return grantWorktrees(b.grants, b.policy(mode), commandPaths(command, b.cwd)) != nil
}

// grantWorktrees adds the worktrees of the workspace's repository that hold
// the paths (resolved) the policy does not let a command write, and
// returns the ones it added.
func grantWorktrees(grants *sandbox.Grants, policy sandbox.Policy, paths []string) []string {
	var added []string
	for _, p := range paths {
		if policy.CanWriteResolved(p) {
			continue
		}
		if root, ok := grants.Worktrees.Of(p); ok && grants.Add(root, sandbox.GrantWorktree) {
			added = append(added, root)
		}
	}

	return added
}

// commandPaths returns the paths a shell command names, resolved against
// cwd: every literal word with a slash, or . or .., in its arguments and
// redirections, the value of a --flag=value word, and a glob's directory.
// It reads the command only to find worktrees to grant, which are checked
// on their own, so a word it misreads grants nothing it should not.
func commandPaths(command, cwd string) []string {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	var paths []string
	seen := map[string]bool{}
	syntax.Walk(file, func(n syntax.Node) bool {
		w, ok := n.(*syntax.Word)
		if !ok {
			return true
		}
		word, ok := literalWord(w)
		if !ok {
			return true // a substitution's own words count
		}
		if _, value, ok := strings.Cut(word, "="); ok && strings.HasPrefix(word, "-") {
			word = value
		}
		if !strings.Contains(word, "/") && word != "." && word != ".." {
			return false
		}
		if i := strings.IndexAny(word, "*?["); i >= 0 {
			word = filepath.Dir(word[:i] + "x")
		}
		if !filepath.IsAbs(word) {
			word = filepath.Join(cwd, word)
		}
		if p := sandbox.ResolvePath(word); !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}

		return false
	})

	return paths
}

// literalWord is a word made of plain text and quotes, without
// expansions; a leading ~ or ~/ is not expanded, so such a word names no
// path here.
func literalWord(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				lit, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}

	return b.String(), true
}
