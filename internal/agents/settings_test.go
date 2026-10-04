package agents_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
)

// TestAgents_ModelEffortAndFastPerChild runs children on the parent's
// provider with their own settings: a role with service_tier = "priority"
// (fast mode), and a spawn call with its own model and effort. /context
// and the compaction window follow the child's model.
func TestAgents_ModelEffortAndFastPerChild(t *testing.T) {
	roles := []agents.Role{{Name: "fast-reviewer", Description: "Reviews fast.", ServiceTier: agents.TierPriority, DeveloperInstructions: "Review."}}
	e := newEnv(t, agents.Config{Roles: roles, MaxThreads: 2},
		fakellm.Reply{Calls: []fakellm.Call{
			call("spawn_agent", `{"message":"CHILD-FAST review","agent_type":"fast-reviewer"}`),
			call("spawn_agent", `{"message":"CHILD-MINI count","model":"gpt-mini","reasoning_effort":"low"}`),
		}},
		fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
			return fakellm.Reply{Calls: []fakellm.Call{call("wait_agent", `{"targets":["`+strings.Join(ids(req), `","`)+`"],"timeout_ms":60000}`)}}
		}},
		fakellm.Reply{Text: "reviewed and counted"},
	)
	e.llm.Route("CHILD-FAST", fakellm.Reply{Text: "looks fine"})
	e.llm.Route("CHILD-MINI", fakellm.Reply{Text: "seven"})
	s, ev := e.open(t, false)

	_, err := s.Submit("delegate")
	require.NoError(t, err)
	ev.finished()

	byText := func(prefix string) fakellm.Request {
		reqs := e.llm.Requests()
		i := slices.IndexFunc(reqs, func(r fakellm.Request) bool {
			return slices.ContainsFunc(r.UserTexts, func(u string) bool { return strings.HasPrefix(u, prefix) })
		})
		require.GreaterOrEqual(t, i, 0, "no request from %s", prefix)

		return reqs[i]
	}
	parent, fast, mini := byText("delegate"), byText("CHILD-FAST"), byText("CHILD-MINI")
	assert.Equal(t, "gpt-test", parent.Model)
	assert.Empty(t, parent.ServiceTier)
	assert.Equal(t, "gpt-test", fast.Model, "a role without a model keeps the parent's")
	assert.Equal(t, "priority", fast.ServiceTier, "the role's service_tier turns on fast mode for its agents only")
	assert.Equal(t, "gpt-mini", mini.Model, "same provider, another model")
	assert.Equal(t, "low", mini.Effort)
	assert.Empty(t, mini.ServiceTier)

	var models []string
	for _, id := range ids(lastParent(e)) {
		u, ok := e.mgr.ContextUsage(id)
		require.True(t, ok, "/context works for a child")
		models = append(models, u.Model)
	}
	assert.ElementsMatch(t, []string{"gpt-test", "gpt-mini"}, models, "/context and the compaction window use each child's model")
}

// childSaved is the settings a child's sidecar keeps: what it opened with.
func childSaved(t *testing.T, e *env, id string) session.Saved {
	t.Helper()
	sc, found, err := session.ReadSidecar(e.sessionsDir(), id)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, sc.Settings)

	return *sc.Settings
}

// TestAgents_SpawnAfterALiveChange changes the parent's model, effort,
// fast mode, adaptive effort, and permission mode while its run waits on
// the model, and then the run spawns: the child starts with the settings
// as they are when it spawns, not as the run started.
func TestAgents_SpawnAfterALiveChange(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, agents.Config{},
		fakellm.Reply{Gate: gate, Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-LIVE look"}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-LIVE", fakellm.Reply{Text: "looked"})
	s, ev := e.open(t, false, e.sandboxed(t))
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	awaitRequests(t, e, "delegate", 1)

	changed := e.settings()
	changed.Model, changed.Effort, changed.ServiceTier, changed.AdaptiveEffort = "gpt-live", "low", "priority", session.AdaptiveOneStep
	applied, err := s.SetSettings(changed.WithMode(approval.ModeReadOnly))
	require.NoError(t, err)
	assert.Equal(t, session.AppliedLive, applied)
	close(gate)
	assert.Equal(t, "done", ev.finished().Answer)

	child := requestWith(t, e, "CHILD-LIVE")
	assert.Equal(t, "gpt-live", child.Model, "/model during the run")
	assert.Equal(t, "low", child.Effort, "/effort during the run")
	assert.Equal(t, "priority", child.ServiceTier, "/fast during the run")
	saved := childSaved(t, e, ids(lastParent(e))[0])
	assert.Equal(t, session.AdaptiveOneStep, saved.AdaptiveEffort, "adaptive effort during the run")
	assert.Equal(t, approval.ModeReadOnly, saved.Mode, "a stricter mode during the run")
}

// TestAgents_ResumeKeepsTheChildsSettings resumes a closed child after
// its parent's model, effort, and fast mode changed: the child gets back
// the settings it saved, not the parent's new ones, and its sidecar keeps
// them. Its permission mode is the parent's now.
func TestAgents_ResumeKeepsTheChildsSettings(t *testing.T) {
	for _, fork := range []bool{false, true} {
		t.Run(fmt.Sprint("fork=", fork), func(t *testing.T) {
			e := newEnv(t, agents.Config{},
				fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", fmt.Sprintf(`{"message":"CHILD-KEEP first","fork_context":%t}`, fork))}},
				callWith("wait_agent", `{"targets":["ID"]}`),
				callWith("close_agent", `{"target":"ID"}`),
				fakellm.Reply{Text: "first done"},
				callWith("resume_agent", `{"id":"ID"}`),
				callWith("send_input", `{"target":"ID","message":"second"}`),
				callWith("wait_agent", `{"targets":["ID"]}`),
				fakellm.Reply{Text: "second done"},
			)
			e.llm.Route("CHILD-KEEP", fakellm.Reply{Text: "1"}, fakellm.Reply{Text: "2"})
			s, ev := e.open(t, false, e.sandboxed(t))
			_, err := s.Submit("delegate")
			require.NoError(t, err)
			assert.Equal(t, "first done", ev.finished().Answer)
			id := ids(lastParent(e))[0]
			before := childSaved(t, e, id)

			changed := e.settings()
			changed.Model, changed.Effort, changed.ServiceTier = "gpt-new", "low", "priority"
			_, err = s.SetSettings(changed.WithMode(approval.ModeReadOnly))
			require.NoError(t, err)
			_, err = s.Submit("again")
			require.NoError(t, err)
			assert.Equal(t, "second done", ev.finished().Answer)

			var last fakellm.Request
			for _, r := range e.llm.Requests() {
				if isChild(r) {
					last = r
				}
			}
			require.Contains(t, last.UserTexts, "second")
			assert.Equal(t, "gpt-test", last.Model, "the child's own model")
			assert.Equal(t, "high", last.Effort, "the child's own effort")
			assert.Empty(t, last.ServiceTier, "the child's own fast mode")
			after := childSaved(t, e, id)
			assert.Equal(t, approval.ModeReadOnly, after.Mode, "the parent's permission mode now")
			after.Mode = before.Mode
			assert.Equal(t, before, after, "the sidecar keeps the child's settings")
		})
	}
}

// TestFork_InheritsTheParentsSettings configures subagent defaults that
// differ from the parent's: a plain child gets them, a fork without
// overrides gets the parent's model and effort, so its request keeps the
// parent's prefix, and the defaults are not checked for it.
func TestFork_InheritsTheParentsSettings(t *testing.T) {
	var checked []string
	cfg := agents.Config{Model: "gpt-default", Effort: "low", MaxThreads: 2, Validate: func(_ context.Context, model string) error {
		checked = append(checked, model)

		return nil
	}}
	e := newEnv(t, cfg,
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-FORKED go on","fork_context":true}`)}},
		callWith("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Calls: []fakellm.Call{call("spawn_agent", `{"message":"CHILD-FRESH look"}`)}},
		callWithLast("wait_agent", `{"targets":["ID"]}`),
		fakellm.Reply{Text: "done"},
	)
	e.llm.Route("CHILD-FORKED", fakellm.Reply{Text: "forked"})
	e.llm.Route("CHILD-FRESH", fakellm.Reply{Text: "fresh"})
	s, ev := e.open(t, false)
	_, err := s.Submit("delegate")
	require.NoError(t, err)
	assert.Equal(t, "done", ev.finished().Answer)

	forked, fresh := requestWith(t, e, "CHILD-FORKED"), requestWith(t, e, "CHILD-FRESH")
	assert.Equal(t, "gpt-test", forked.Model, "a fork has the parent's model")
	assert.Equal(t, "high", forked.Effort, "and the parent's effort")
	assert.Equal(t, "gpt-default", fresh.Model, "a plain child has the configured default")
	assert.Equal(t, "low", fresh.Effort)
	assert.Equal(t, []string{"gpt-default"}, checked, "only the plain child's default is checked")
}
