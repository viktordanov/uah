package agents_test

import (
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/approval"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/sandbox"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

const waitTimeout = 20 * time.Second

type env struct {
	*harnesstest.Env
	llm *fakellm.Server
	cfg agents.Config
	mgr *agents.Manager
	// hooks, when set, are the sessions' hooks.
	hooks *hooks.Runner
	// stream opens the parent as the TUI does, asking for its text as it
	// arrives.
	stream bool
	// yolo opens the parent as --yolo does, in yolo mode.
	yolo bool
}

// newEnv starts one fake model for the parent (the main script) and its
// children (routes), and a manager for an embedded engine.
func newEnv(t *testing.T, cfg agents.Config, replies ...fakellm.Reply) *env {
	t.Helper()
	e := &env{Env: harnesstest.NewEnv(t), llm: fakellm.New(t, replies...)}
	if cfg.MaxDepth == 0 {
		cfg.MaxDepth = agents.DefaultMaxDepth
	}
	e.cfg = cfg
	e.mgr = agents.New(cfg)

	return e
}

func (e *env) sessionsDir() string { return filepath.Join(e.StateDir, "sessions") }

func (e *env) getenv(key string) string {
	switch key {
	case "OPENAI_API_KEY":
		return "test-key"
	case "SHELL":
		return "/bin/sh"
	}

	return e.Env.Getenv(key)
}

// open opens the parent session; with configure, it adjusts the engine.
func (e *env) open(t *testing.T, interactive bool, configure ...func(*embedded.Config)) (*session.Session, *events) {
	t.Helper()

	return e.openID(t, "", interactive, configure...)
}

// openID opens the parent session with an ID, resuming it when set.
func (e *env) openID(t *testing.T, id string, interactive bool, configure ...func(*embedded.Config)) (*session.Session, *events) {
	t.Helper()
	cfg := embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Subagents: e.mgr}
	for _, c := range configure {
		c(&cfg)
	}
	cfg.Hooks = e.hooks
	eng := embedded.New(cfg)
	opts := session.Options{
		ID: id, Resumed: id != "", Settings: e.settings(), Hooks: e.hooks,
		SessionsDir: e.sessionsDir(), Source: session.SourceTUI, Interactive: interactive, Stream: e.stream, Yolo: e.yolo,
	}
	if e.yolo {
		opts.Settings = opts.Settings.WithMode(approval.ModeYolo)
	}
	e.mgr.Bind(eng, opts)
	s, err := session.Open(context.Background(), eng, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s, &events{t: t, s: s}
}

func (e *env) settings() session.Settings {
	return session.Settings{Provider: "openai", Model: "gpt-test", Effort: "high", Workspace: e.Workspace, BaseURL: e.llm.URL}
}

// sandboxed configures the workspace-write sandbox, or skips the test
// where there is none.
func (e *env) sandboxed(t *testing.T) func(*embedded.Config) {
	t.Helper()
	policy := sandbox.Policy{Mode: sandbox.WorkspaceWrite, Workspace: e.Workspace}
	if _, err := policy.Wrap([]string{"/bin/sh"}); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}

	return func(c *embedded.Config) { c.Sandbox, c.SandboxDir = &policy, filepath.Join(e.StateDir, "sandbox") }
}

type events struct {
	t   *testing.T
	s   *session.Session
	all []core.Event
}

func (ev *events) until(what string, match func(core.Event) bool) core.Event {
	ev.t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case e, ok := <-ev.s.Events():
			require.True(ev.t, ok, "the session closed while waiting for %s", what)
			ev.all = append(ev.all, e)
			if match(e) {
				return e
			}
		case <-deadline:
			ev.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (ev *events) finished() core.Result {
	ev.t.Helper()

	return ev.until("RunFinished", func(e core.Event) bool { _, ok := e.(core.RunFinished); return ok }).(core.RunFinished).Result
}

// agentState returns the first update of a child to state, seen or next.
func (ev *events) agentState(state string) engine.AgentUpdated {
	ev.t.Helper()
	match := func(x core.Event) bool {
		u, ok := x.(engine.AgentUpdated)
		return ok && u.State == state
	}
	if i := slices.IndexFunc(ev.all, match); i >= 0 {
		return ev.all[i].(engine.AgentUpdated)
	}

	return ev.until("agent "+state, match).(engine.AgentUpdated)
}

func (ev *events) approval() session.ApprovalRequested {
	ev.t.Helper()

	return ev.until("ApprovalRequested", func(x core.Event) bool { _, ok := x.(session.ApprovalRequested); return ok }).(session.ApprovalRequested)
}

func call(name, args string) fakellm.Call { return fakellm.Call{Name: name, Args: args} }

var idPattern = regexp.MustCompile(`"agent_id":"((?:subagent-)?[0-9a-f-]{36})"`)

// ids are the agent IDs in the request's tool results, in order.
func ids(req fakellm.Request) []string {
	var out []string
	for _, o := range req.ToolOutputs {
		for _, m := range idPattern.FindAllStringSubmatch(o, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}

	return out
}

// callWith is a reply whose one call uses the first agent ID so far.
func callWith(name, args string) fakellm.Reply {
	return fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
		return fakellm.Reply{Calls: []fakellm.Call{call(name, strings.ReplaceAll(args, "ID", ids(req)[0]))}}
	}}
}

// callWithLast is callWith with the latest agent ID: the second child's
// after a second spawn_agent.
func callWithLast(name, args string) fakellm.Reply {
	return fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
		all := ids(req)

		return fakellm.Reply{Calls: []fakellm.Call{call(name, strings.ReplaceAll(args, "ID", all[len(all)-1]))}}
	}}
}

// isChild reports whether a request is a child's: children's messages
// start with CHILD.
func isChild(r fakellm.Request) bool {
	return slices.ContainsFunc(r.UserTexts, func(s string) bool { return strings.HasPrefix(s, "CHILD") })
}

// lastParent is the parent's last request.
func lastParent(e *env) fakellm.Request {
	var last fakellm.Request
	for _, r := range e.llm.Requests() {
		if !isChild(r) {
			last = r
		}
	}

	return last
}

func lastOutputs(e *env) string { return strings.Join(lastParent(e).ToolOutputs, "\n") }

// answers are the parent's last request's tool results and developer
// messages: a child that finishes before the parent's wait_agent gives its
// answer in a <subagent_notification> in the run, and the wait points at it.
func answers(e *env) string {
	last := lastParent(e)

	return strings.Join(slices.Concat(last.ToolOutputs, last.DeveloperTexts), "\n")
}

// childOutputs are the tool results in the requests of the child whose
// first message starts with prefix.
func childOutputs(e *env, prefix string) string {
	var out []string
	for _, r := range e.llm.Requests() {
		if slices.ContainsFunc(r.UserTexts, func(s string) bool { return strings.HasPrefix(s, prefix) }) {
			out = append(out, r.ToolOutputs...)
		}
	}

	return strings.Join(out, "\n")
}
