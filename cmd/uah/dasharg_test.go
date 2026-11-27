package main_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/testing/fakellm"
)

// TestFlagsAfterDash: the parser drops what follows the lone - it stops
// at, so a command line that goes on after it is refused, not run without
// its flags; a - that ends the line reads stdin as before.
func TestFlagsAfterDash(t *testing.T) {
	t.Parallel()
	_, env := fakeEnv(t)
	for _, args := range [][]string{
		{"exec", "-", "-m", "x", "--tools", ""},
		{"exec", "-", "--tools", "", "--ephemeral", "-"},
		{"exec", "summarize", "-", "--ephemeral"},
		{"run", "-", "-m", "x"},
		{"review", "-", "--base", "main"},
		{"review", "look", "-", "--base", "main"},
	} {
		res := uahWith(t, env, "the prompt\n", args...)
		assert.Equal(t, 2, res.code, args)
		assert.Contains(t, res.stderr, "put the flags before it", args)
	}

	res := uahWith(t, env, "", "exec", "-m", "x", "-")
	assert.Equal(t, 2, res.code)
	assert.Contains(t, res.stderr, "no prompt on stdin", "a - at the end reads stdin")
}

// TestDashInThePrompt: after a word such as -1, the parser keeps the line
// whole, so a later - is the prompt's text, sent as typed.
func TestDashInThePrompt(t *testing.T) {
	t.Parallel()
	llm := fakellm.New(t, fakellm.Reply{Text: "3"})
	e, env := modelEnv(t, llm)

	res := uahWith(t, env, "", "exec", "-C", e.Workspace, "explain", "-1", "-", "2")

	require.Equal(t, 0, res.code, res.stderr)
	assert.Equal(t, []string{"explain -1 - 2"}, lastRequest(t, llm).UserTexts)
}
