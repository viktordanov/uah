package cmdparse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uah/internal/cmdparse"
)

const root = "/Users/me/Work/proj"

var env = cmdparse.Env{Workspace: root, Home: "/Users/me"}

func TestStrip(t *testing.T) {
	for in, want := range map[string]string{
		"rtk proxy sh -c 'cat a.txt'":        "cat a.txt",
		"rtk proxy python3 x.py":             "python3 x.py",
		"rtk git status":                     "git status",
		"rtk gain":                           "rtk gain",
		`bash -lc "ls -la"`:                  "ls -la",
		"/bin/zsh -lc 'rg foo'":              "rg foo",
		"sh -c 'a' 'b'":                      "sh -c 'a' 'b'",
		"sh -c ls":                           "sh -c ls",
		"  rtk proxy bash -c 'rtk proxy ls'": "ls",
		"python3 - <<'PY'\nprint(1)\nPY":     "python3 - <<'PY'\nprint(1)\nPY",
	} {
		assert.Equal(t, want, cmdparse.Strip(in), in)
	}
}

func TestRelative(t *testing.T) {
	for in, want := range map[string]string{
		"cat " + root + "/README.md":            "cat README.md",
		"cd " + root + " && ls":                 "cd . && ls",
		"cat /Users/me/AGENTS.md":               "cat ~/AGENTS.md",
		"cat /Users/me":                         "cat ~",
		"cat /Users/mentor/x":                   "cat /Users/mentor/x",
		"cat /x" + root + "/a":                  "cat /x" + root + "/a",
		`sed -n "1,2p" "` + root + `/a.go"`:     `sed -n "1,2p" "a.go"`,
		"git -C " + root + " diff -- a.go":      "git diff -- a.go",
		"cat '" + root + "/api/README.md'":      "cat 'api/README.md'",
		"python3 -c 'p=Path(\"" + root + "\")'": "python3 -c 'p=Path(\".\")'",
	} {
		assert.Equal(t, want, cmdparse.Relative(in, env), in)
	}
}

// TestAbsolute: a path a command reads, made absolute for its link.
func TestAbsolute(t *testing.T) {
	for in, want := range map[string]string{
		"a.go":            root + "/a.go",
		"./pkg/../b.go":   root + "/b.go",
		"/etc/hosts":      "/etc/hosts",
		"~/notes.md":      "/Users/me/notes.md",
		"~":               "/Users/me",
		"~other/x":        root + "/~other/x",
		"/tmp/../tmp/c.g": "/tmp/c.g",
	} {
		assert.Equal(t, want, cmdparse.Absolute(in, env), in)
	}
	assert.Equal(t, "a.go", cmdparse.Absolute("a.go", cmdparse.Env{}), "no workspace")
	assert.Equal(t, "~/a.go", cmdparse.Absolute("~/a.go", cmdparse.Env{}), "no home")
}

func TestSummarize(t *testing.T) {
	type part = cmdparse.Part
	plain := func(s string) part { return part{Text: s, Style: cmdparse.Plain} }
	dim := func(s string) part { return part{Text: s, Style: cmdparse.Dim} }
	file := func(s, rel, lines string) part {
		return part{Text: s, Style: cmdparse.Plain, Path: root + "/" + rel, Lines: lines}
	}
	cases := []struct {
		name, command string
		want          cmdparse.Summary
	}{
		{
			"reads two files",
			"rtk proxy sh -c 'cat " + root + "/README.md " + root + "/api/README.md'",
			cmdparse.Summary{Label: "READ", Parts: []part{file("README.md", "README.md", ""), dim(", "), file("api/README.md", "api/README.md", "")}},
		},
		{
			"sed ranges",
			`rtk proxy sh -c 'sed -n "1,360p" ` + root + `/api/ontology/ontology.go; sed -n "356,365p" ` + root + `/api/ontology/loader.go'`,
			cmdparse.Summary{Label: "READ", Parts: []part{
				file("api/ontology/ontology.go", "api/ontology/ontology.go", "1-360"), dim(":1-360"), dim(", "),
				file("loader.go", "api/ontology/loader.go", "356-365"), dim(":356-365"),
			}},
		},
		{
			"head in a pipe and a file in the same directory",
			"cat " + root + "/web/a/Dialog.tsx | head -160; cat " + root + "/web/a/useStudies.ts",
			cmdparse.Summary{Label: "READ", Parts: []part{file("web/a/Dialog.tsx", "web/a/Dialog.tsx", "1-160"), dim(":1-160"), dim(", "), file("useStudies.ts", "web/a/useStudies.ts", "")}},
		},
		{
			"list with globs",
			`rtk proxy sh -c 'rg --files ` + root + `/web/src -g "*CI*" -g "*OUS*"'`,
			cmdparse.Summary{Label: "LIST", Parts: []part{plain("web/src"), dim("  *CI* *OUS*")}},
		},
		{
			"search in two paths",
			`rtk proxy sh -c 'rg -n "RootData|ontologyVersion" ` + root + `/api ` + root + `/web/src'`,
			cmdparse.Summary{Label: "SEARCH", Parts: []part{plain("RootData|ontologyVersion"), dim(" in "), plain("api, web/src")}},
		},
		{
			"search with no path",
			"rg -n TODO",
			cmdparse.Summary{Label: "SEARCH", Parts: []part{plain("TODO")}},
		},
		{
			"list the workspace",
			"ls -la",
			cmdparse.Summary{Label: "LIST", Parts: []part{plain(".")}},
		},
		{
			"unknown stays a command",
			"rtk proxy sh -c 'git -C " + root + " diff -- api/rest/server.go; diff " + root + "/a.go " + root + "/a.go.orig'",
			cmdparse.Summary{Parts: []part{plain("git diff -- api/rest/server.go; diff a.go a.go.orig")}},
		},
		{
			"mixed kinds run",
			"cat a.go && rg foo",
			cmdparse.Summary{Parts: []part{plain("cat a.go && rg foo")}},
		},
		{
			"heredoc",
			"rtk proxy python3 - <<'PY'\nfrom pathlib import Path\n\n# load\nimport re,json\np=Path('" + root + "/api/ontology.go')\nPY",
			cmdparse.Summary{Parts: []part{plain("python3 <<PY "), {Text: "5 lines · from pathlib import Path; import re,json; p=Path('api/ontology.go')", Style: cmdparse.Faint}}},
		},
		{
			"one-line heredoc",
			"cat > notes.md <<EOF\nhi\nEOF",
			cmdparse.Summary{Parts: []part{plain("cat > notes.md <<EOF "), {Text: "1 line · hi", Style: cmdparse.Faint}}},
		},
		{
			"multi-line command on one line",
			"for p in " + root + "/a " + root + "/b; do\n  cat \"$p\"\ndone",
			cmdparse.Summary{Parts: []part{plain(`for p in a b; do cat "$p" done`)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, cmdparse.Summarize(tc.command, env))
		})
	}
}

func TestParseScript_Extensions(t *testing.T) {
	got := cmdparse.ParseScript("rg --files web/src -g '*.go' --glob '*.md'")
	assert.Equal(t, []string{"web/src"}, got[0].Paths)
	assert.Equal(t, []string{"*.go", "*.md"}, got[0].Globs)

	got = cmdparse.ParseScript("head -n 40 a.go")
	assert.Equal(t, "1-40", got[0].Lines)

	got = cmdparse.ParseScript("cat a.go | sed -n '5,9p'")
	assert.Equal(t, "5-9", got[0].Lines)

	got = cmdparse.ParseScript("cat a.go b.go")
	assert.Len(t, got, 2)
	assert.Equal(t, "b.go", got[1].Path)

	got = cmdparse.ParseScript("grep -rn foo src lib")
	assert.Equal(t, []string{"src", "lib"}, got[0].Paths)

	// A script with an expansion, a glob, or a heredoc is one unknown.
	for _, s := range []string{"cat $HOME/a", "ls *.go", "cat <<EOF\nx\nEOF", "FOO=1 ls", "(ls)", "ls # comment"} {
		assert.Equal(t, cmdparse.Unknown, cmdparse.ParseScript(s)[0].Kind, s)
	}
}
