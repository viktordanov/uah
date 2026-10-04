package bubble_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

const migrationCall = `{"questions":[` +
	`{"id":"strategy","header":"Migration","question":"Which way should the migration take?","options":[` +
	`{"label":"Expand and contract (Recommended)","description":"Add, backfill, drop later."},` +
	`{"label":"Rename in place","description":"A short lock on the table."}]},` +
	`{"id":"rollout","header":"Rollout","question":"When should it run?","options":[` +
	`{"label":"Next deploy","description":"Ships with the code."},` +
	`{"label":"By hand","description":"In a quiet hour."}]}]}`

// questionDeps opens interactive sessions on an embedded engine that
// offers request_user_input, with a model that asks the migration's
// questions, then answers.
func questionDeps(t *testing.T) (bubble.Deps, *fakellm.Server) {
	t.Helper()
	env := harnesstest.NewEnv(t)
	llm := fakellm.New(t,
		fakellm.Reply{Calls: []fakellm.Call{{Name: engine.QuestionToolName, Args: migrationCall}}},
		fakellm.Reply{Text: "Planned it your way."},
	)
	getenv := func(key string) string {
		switch key {
		case "OPENAI_API_KEY":
			return "test-key"
		case "SHELL":
			return "/bin/sh"
		}

		return env.Getenv(key)
	}
	eng := embedded.New(embedded.Config{StateDir: env.StateDir, Provider: "openai", Getenv: getenv, AskUser: true})
	settings := session.Settings{Provider: "openai", Model: "gpt-test", Effort: "high", Workspace: env.Workspace, BaseURL: llm.URL}

	return bubble.Deps{
		Open: func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
			s, err := session.Open(ctx, eng, session.Options{ID: id, Settings: settings, Interactive: true})

			return s, nil, err
		},
		Sessions: func() ([]session.Info, error) { return nil, nil },
	}, llm
}

// TestTUI_AnswerTheAgentsQuestions: the picker shows the first question;
// a stray letter does nothing, ↓ and n write a note on an option, enter
// keeps it and enter answers; the second question's own-answer row takes
// typed words; the model's next request carries both answers.
func TestTUI_AnswerTheAgentsQuestions(t *testing.T) {
	deps, llm := questionDeps(t)
	d := start(t, deps)
	d.typeText("plan the migration")
	d.key(term.KeyEnter, 0)

	d.waitFor("Which way should the migration take?")
	assert.Contains(t, d.view(), "› 1. Expand and contract (Recommended)")
	assert.Contains(t, d.view(), "Waiting for your answer")
	d.typeText("x")
	assert.Empty(t, d.draft(), "the options take no text")
	d.key(term.KeyDown, 0)
	assert.Contains(t, d.view(), "› 2. Rename in place")
	d.typeText("n")
	assert.Contains(t, d.view(), "Note on Rename in place")
	d.typeText("lock it after 6pm")
	d.key(term.KeyEnter, 0)
	assert.Contains(t, d.view(), "✎ lock it after 6pm")
	d.key(term.KeyEnter, 0)

	d.waitFor("When should it run?")
	d.typeText("3")
	assert.Contains(t, d.view(), "› 3. Type your own answer")
	d.typeText("after the Friday backup")
	d.key(term.KeyEnter, 0)

	d.waitFor("Planned it your way.")
	assert.Contains(t, d.view(), "Migration: Rename in place · lock it after 6pm")
	assert.Contains(t, d.view(), "Rollout: after the Friday backup")
	reqs := llm.Requests()
	require.Len(t, reqs, 2)
	assert.JSONEq(t, `{"answers":{"strategy":{"answers":["Rename in place","user_note: lock it after 6pm"]},"rollout":{"answers":["None of the above","user_note: after the Friday backup"]}}}`,
		reqs[1].ToolOutputs[0])
	assert.Empty(t, d.draft(), "the composer is free again")
}

// TestTUI_DismissTheAgentsQuestions: esc interrupts the run, as Codex's
// picker does; the draft stays, and the next message goes to the agent
// with the call cancelled.
func TestTUI_DismissTheAgentsQuestions(t *testing.T) {
	deps, llm := questionDeps(t)
	d := start(t, deps)
	d.typeText("plan the migration")
	d.key(term.KeyEnter, 0)

	d.waitFor("Which way should the migration take?")
	d.key('2', 0) // a number picks and answers
	d.waitFor("When should it run?")
	d.key('3', 0)
	d.typeText("use expand and contract")
	d.key(term.KeyEscape, 0)
	d.until("the picker closes", func() bool { return !strings.Contains(d.view(), "When should it run?") })
	assert.Equal(t, "use expand and contract", d.draft())
	d.waitIdle()

	d.key(term.KeyEnter, 0)
	d.waitFor("Planned it your way.")
	reqs := llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, []string{"request_user_input was cancelled before receiving a response"}, reqs[1].ToolOutputs)
	assert.Equal(t, []string{"plan the migration", "use expand and contract"}, reqs[1].UserTexts)
}
