package home_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/home"
)

// setForOthers are the variables uah sets for the commands it runs: the
// hooks', which uah does not read, and the tool bridge's token, which only
// `uah tool` reads, inside a session's commands, never a user.
var setForOthers = []string{"UAH_HOOK_EVENT", "UAH_PROJECT_DIR", "UAH_TOOL_TOKEN"}

// TestVariables: every uah or runner variable the product code names is in
// home.Variables (or is UAH_HOME, or one uah sets for hooks), and every
// listed variable is named in the product code. home.go, where the list is
// declared, does not count as reading it: other files name a variable by
// its string or by home's constant for it, and home.Warnings reads the
// legacy names.
func TestVariables(t *testing.T) {
	root := filepath.Join("..", "..")
	declared := filepath.Join(root, "internal", "home", "home.go")
	consts := homeConsts(t, declared)
	name := regexp.MustCompile(`"((?:UAH|UAGENT)_[A-Z0-9_]+|CODEX_REFRESH_TOKEN_URL_OVERRIDE)"`)
	constRef := regexp.MustCompile(`\bhome\.(Env[A-Za-z]*)\b`)
	named := map[string]bool{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == declared {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range name.FindAllSubmatch(src, -1) {
				named[string(m[1])] = true
			}
			for _, m := range constRef.FindAllSubmatch(src, -1) {
				v, ok := consts[string(m[1])]
				require.True(t, ok, "%s names home.%s, which home.go does not declare", path, m[1])
				named[v] = true
			}
			if strings.Contains(string(src), "home.Warnings(") {
				for _, l := range home.Legacy {
					named[l.Old] = true
				}
			}

			return nil
		})
		require.NoError(t, err)
	}

	for v := range named {
		if v != home.Env && !slices.Contains(setForOthers, v) {
			assert.Contains(t, home.Variables, v, "uah reads %s; add it to home.Variables", v)
		}
	}
	for _, v := range home.Variables {
		assert.True(t, named[v], "uah does not read %s; remove it from home.Variables", v)
	}
}

// homeConsts are the string constants home.go declares, by name.
func homeConsts(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	out := map[string]string{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec) //nolint:forcetypeassert // a const declaration holds value specs
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					out[n.Name], err = strconv.Unquote(lit.Value)
					require.NoError(t, err)
				}
			}
		}
	}

	return out
}
