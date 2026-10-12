package embedded_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"
	uaharness "github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/hooks"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/testing/fakellm"
	"github.com/viktordanov/uah/testing/harnesstest"
)

const waitTimeout = 20 * time.Second

type env struct {
	*harnesstest.Env
	llm *fakellm.Server
}

func newEnv(t *testing.T, replies ...fakellm.Reply) *env {
	t.Helper()

	return &env{Env: harnesstest.NewEnv(t), llm: fakellm.New(t, replies...)}
}

func (e *env) getenv(key string) string {
	switch key {
	case "OPENAI_API_KEY":
		return "test-key"
	case "SHELL":
		return "/bin/sh"
	}

	return e.Env.Getenv(key)
}

func (e *env) embedded() *embedded.Engine {
	return embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv})
}

func (e *env) settings() session.Settings {
	return session.Settings{Provider: "openai", Model: "gpt-test", Effort: "high", Workspace: e.Workspace, BaseURL: e.llm.URL}
}

// open opens a session and returns a reader of its events.
func (e *env) open(t *testing.T, eng engine.Engine, id string) (*session.Session, *events) {
	t.Helper()
	s, err := session.Open(context.Background(), eng, session.Options{ID: id, Resumed: id != "", Settings: e.settings()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s, &events{t: t, s: s}
}

type events struct {
	t   *testing.T
	s   *session.Session
	all []core.Event
}

// until reads events until match returns true and returns that event.
func (ev *events) until(what string, match func(core.Event) bool) core.Event {
	ev.t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case e, ok := <-ev.s.Events():
			require.True(ev.t, ok, "the session closed while waiting for %s", what)
			ev.all = append(ev.all, e)
			if n, isNotice := e.(session.Notice); isNotice && n.Level == "error" {
				ev.t.Logf("notice: %s", n.Message)
			}
			if match(e) {
				return e
			}
		case <-deadline:
			ev.t.Fatalf("timed out waiting for %s; events: %v", what, kinds(ev.all))
		}
	}
}

func isA[T core.Event](e core.Event) bool { _, ok := e.(T); return ok }

func (ev *events) finished() core.Result {
	ev.t.Helper()

	return ev.until("RunFinished", isA[core.RunFinished]).(core.RunFinished).Result
}

func (ev *events) idle() { ev.t.Helper(); ev.until("Idle", isA[session.Idle]) }

func waitSeen(t *testing.T, s *fakellm.Server, n int) {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case got := <-s.Seen():
			if got >= n {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for model request %d", n)
		}
	}
}

func TestEmbedded_RunsToolsAndAnswers(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "Listing.", Commands: []string{"echo one", "echo two"}},
		fakellm.Reply{Text: "hello"},
	)
	s, ev := e.open(t, e.embedded(), "")

	in, err := s.Submit("say hello")
	require.NoError(t, err)
	delivered := ev.until("delivery", isA[session.InputDelivered]).(session.InputDelivered)
	assert.Equal(t, in.ID, delivered.ID)
	result := ev.finished()
	ev.idle()

	assert.Equal(t, core.StatusOK, result.Status)
	assert.Equal(t, "hello", result.Answer)
	assert.Equal(t, 2, result.Stats.ToolCalls)
	assert.Equal(t, 0, result.Stats.FailedToolCalls)
	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "gpt-test", reqs[0].Model)
	assert.Equal(t, "high", reqs[0].Effort)
	assert.Equal(t, []string{"say hello"}, reqs[0].UserTexts)
	assert.Contains(t, reqs[0].System, strings.TrimSpace(instructions.DefaultPrompt), "uah's default base instructions when the request has none")

	runDir := filepath.Join(e.StateDir, "runs", result.Request.RunID)
	for _, f := range []string{uaharness.RequestFile, uaharness.EventsFile, uaharness.SummaryFile} {
		assert.FileExists(t, filepath.Join(runDir, f))
	}
	assert.FileExists(t, filepath.Join(e.StateDir, "sessions", s.ID()+".session.jsonl"))
}

// TestEmbedded_MatchesTheRunner drives the real runner and the embedded engine
// with one script and compares what the harness sees.
func TestEmbedded_MatchesTheRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("builds uah-core-runner")
	}
	script := func() []fakellm.Reply {
		return []fakellm.Reply{
			{Text: "Checking.", Commands: []string{"echo one", "false"}},
			{Text: "the answer"},
		}
	}
	runWith := func(t *testing.T, e *env, eng engine.Engine) (core.Result, []string, []string) {
		t.Helper()
		s, ev := e.open(t, eng, "")
		_, err := s.Submit("do the thing")
		require.NoError(t, err)
		result := ev.finished()
		ev.idle()
		items := sessionItemKinds(t, filepath.Join(e.StateDir, "sessions", s.ID()+".session.jsonl"))

		return result, summarize(ev.all), items
	}

	pe := newEnv(t, script()...)
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := harnesstest.RunnerEngine(uaharness.Config{RunnerPath: harnesstest.RealRunner(t), StateDir: pe.StateDir, Getenv: pe.getenv, KillGrace: time.Second})
	procResult, procEvents, procItems := runWith(t, pe, runner)

	ee := newEnv(t, script()...)
	embResult, embEvents, embItems := runWith(t, ee, ee.embedded())

	// Parallel tools finish in any order, in both: compare the order
	// of everything else, and the tool results and session items as sets.
	assert.Equal(t, withoutPrefix(procEvents, "done "), withoutPrefix(embEvents, "done "))
	assert.ElementsMatch(t, procEvents, embEvents)
	assert.ElementsMatch(t, procItems, embItems, "the session files hold the same items")
	assert.Equal(t, procResult.Status, embResult.Status)
	assert.Equal(t, procResult.Answer, embResult.Answer)
	assert.Equal(t, procResult.Stats.ToolCalls, embResult.Stats.ToolCalls)
	assert.Equal(t, procResult.Stats.FailedToolCalls, embResult.Stats.FailedToolCalls)
	assert.Equal(t, procResult.Stats.Tokens, embResult.Stats.Tokens)
	assert.Equal(t, e2eRequests(pe.llm), e2eRequests(ee.llm), "the model sees the same requests")
}

func TestEmbedded_SteersALiveRun(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, fakellm.Reply{Text: "first answer", Gate: gate}, fakellm.Reply{Text: "second answer"})
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("start")
	require.NoError(t, err)
	waitSeen(t, e.llm, 1)
	steer, err := s.SteerNow("also this")
	require.NoError(t, err)
	close(gate)

	delivered := ev.until("the steer's delivery", func(x core.Event) bool {
		d, ok := x.(session.InputDelivered)

		return ok && d.ID == steer.ID
	})
	assert.NotNil(t, delivered)
	result := ev.finished()
	ev.idle()

	assert.Equal(t, "second answer", result.Answer, "one run answered both messages")
	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, []string{"start", "also this"}, reqs[1].UserTexts)
	assert.Equal(t, 1, countKind[core.RunStarted](ev.all), "the steer did not restart the run")
}

func TestEmbedded_ChangesSettingsLive(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, fakellm.Reply{Commands: []string{"true"}, Gate: gate}, fakellm.Reply{Text: "done"})
	s, ev := e.open(t, e.embedded(), "")
	assert.True(t, s.Priority(), "openai offers priority processing")

	_, err := s.Submit("go")
	require.NoError(t, err)
	waitSeen(t, e.llm, 1)
	next := e.settings()
	next.Effort, next.Model, next.ServiceTier = "low", "gpt-other", "priority"
	applied, err := s.SetSettings(next)
	require.NoError(t, err)
	assert.Equal(t, session.AppliedLive, applied)
	close(gate)
	ev.finished()

	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, fakellm.Request{Model: "gpt-test", Effort: "high"}, trimmed(reqs[0]))
	assert.Equal(t, fakellm.Request{Model: "gpt-other", Effort: "low", ServiceTier: "priority"}, trimmed(reqs[1]))
}

func TestEmbedded_InterruptThenContinue(t *testing.T) {
	e := newEnv(t, fakellm.Reply{Commands: []string{"sleep 30"}})
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("wait a while")
	require.NoError(t, err)
	ev.until("the tool starting", isA[core.ToolStarted])
	started := time.Now()
	require.NoError(t, s.Interrupt())
	result := ev.finished()
	assert.Equal(t, core.StatusInterrupted, result.Status)
	assert.Less(t, time.Since(started), 5*time.Second, "the hard stop cancels the tool")
	ev.idle()

	_, err = s.Submit("carry on")
	require.NoError(t, err)
	result = ev.finished()
	assert.Equal(t, core.StatusOK, result.Status)
	reqs := e.llm.Requests()
	assert.Equal(t, []string{"wait a while", "carry on"}, reqs[len(reqs)-1].UserTexts, "the session resumes after the hard stop")
}

// TestEmbedded_UnreadMessageGoesWithTheNext: a message the model never
// answered, because the run was stopped, is stored unread; the next
// message's run asks the model once, with both, and no request is
// started and canceled first.
func TestEmbedded_UnreadMessageGoesWithTheNext(t *testing.T) {
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	e := newEnv(t, fakellm.Reply{Text: "never", Gate: gate}, fakellm.Reply{Text: "both"}, fakellm.Reply{Text: "extra"})
	s, ev := e.open(t, e.embedded(), "")

	_, err := s.Submit("first")
	require.NoError(t, err)
	waitSeen(t, e.llm, 1)
	require.NoError(t, s.Interrupt())
	assert.Equal(t, core.StatusInterrupted, ev.finished().Status)
	ev.idle()

	_, err = s.Submit("second")
	require.NoError(t, err)
	result := ev.finished()
	assert.Equal(t, "both", result.Answer)
	reqs := e.llm.Requests()
	require.Len(t, reqs, 2, "one request after the stop")
	assert.Equal(t, []string{"first", "second"}, reqs[1].UserTexts)
}

// TestEmbedded_ResumesAProcessSession resumes a session the real runner
// started, as the removed process engine left them: both write the
// runner's session file and uagent's run records under the state
// directory, so the embedded engine replays its history.
func TestEmbedded_ResumesAProcessSession(t *testing.T) {
	if testing.Short() {
		t.Skip("builds uah-core-runner")
	}
	e := newEnv(t, fakellm.Reply{Text: "first"}, fakellm.Reply{Text: "second"})
	t.Setenv("OPENAI_API_KEY", "test-key")
	runner := harnesstest.RunnerEngine(uaharness.Config{RunnerPath: harnesstest.RealRunner(t), StateDir: e.StateDir, Getenv: e.getenv, KillGrace: time.Second})
	s, ev := e.open(t, runner, "")
	_, err := s.Submit("remember the number 7")
	require.NoError(t, err)
	ev.finished()
	ev.idle()
	id := s.ID()
	require.NoError(t, s.Close())

	s2, ev2 := e.open(t, e.embedded(), id)
	_, err = s2.Submit("what was the number?")
	require.NoError(t, err)
	result := ev2.finished()

	assert.Equal(t, "second", result.Answer)
	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, []string{"remember the number 7", "what was the number?"}, reqs[1].UserTexts, "the embedded run replays the runner's history")
}

func trimmed(r fakellm.Request) fakellm.Request {
	return fakellm.Request{Model: r.Model, Effort: r.Effort, ServiceTier: r.ServiceTier}
}

func e2eRequests(s *fakellm.Server) []fakellm.Request {
	var out []fakellm.Request
	for _, r := range s.Requests() {
		// The system prompt, the raw items (with the order parallel tools
		// finished in), and the cache key (the session's ID) differ by run.
		r.System, r.Input, r.CacheKey = "", nil, ""
		slices.Sort(r.ToolOutputs)
		// apply_patch and the shell tools are the embedded engine's own
		// tools, as is Bash's background argument, and SkillUse comes with
		// its system skills.
		for _, own := range []string{"apply_patch", "SkillUse", "wait_shell", "kill_shell"} {
			r.ToolNames = slices.DeleteFunc(r.ToolNames, func(n string) bool { return n == own })
			r.ToolDefs = slices.DeleteFunc(r.ToolDefs, func(d json.RawMessage) bool { return strings.Contains(string(d), `"name":"`+own+`"`) })
			delete(r.Tools, own)
		}
		if b, ok := r.Tools["Bash"]; ok {
			r.Tools["Bash"] = withoutBackground(b, "properties")
		}
		for i, d := range r.ToolDefs {
			if strings.Contains(string(d), `"name":"Bash"`) {
				r.ToolDefs[i] = json.RawMessage(withoutBackground(string(d), "parameters", "properties"))
			}
		}
		out = append(out, r)
	}

	return out
}

// withoutBackground is a JSON object without Bash's background argument
// at the path, encoded again.
func withoutBackground(raw string, path ...string) string {
	var v map[string]any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	m := v
	for _, k := range path {
		next, ok := m[k].(map[string]any)
		if !ok {
			return raw
		}
		m = next
	}
	delete(m, "background")
	data, err := json.Marshal(v)
	if err != nil {
		return raw
	}

	return string(data)
}

func withoutPrefix(lines []string, prefix string) []string {
	return slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return strings.HasPrefix(l, prefix) })
}

func countKind[T core.Event](all []core.Event) int {
	n := 0
	for _, e := range all {
		if isA[T](e) {
			n++
		}
	}

	return n
}

// summarize reduces events to what must match across engines.
func summarize(all []core.Event) []string {
	var out []string
	for _, e := range all {
		switch v := e.(type) {
		case core.UserMessage:
			out = append(out, "user "+v.Text)
		case core.TurnStarted:
			out = append(out, fmt.Sprintf("turn %d", v.Turn))
		case core.ToolCalled:
			out = append(out, "call "+v.Name+" "+v.Label)
		case core.ToolFinished:
			out = append(out, fmt.Sprintf("done %s ok=%v", v.CallID, v.OK))
		case core.AssistantMessage:
			out = append(out, fmt.Sprintf("say %q final=%v", v.Text, v.Final))
		case core.RunnerError:
			out = append(out, "error "+v.Message)
		case core.RunFinished:
			out = append(out, "finished "+string(v.Result.Status))
		}
	}

	return out
}

func kinds(all []core.Event) []string {
	out := make([]string, 0, len(all))
	for _, e := range all {
		out = append(out, fmt.Sprintf("%T", e))
	}

	return out
}

// sessionItemKinds lists the "type" of each session file line but the
// operation records, which the embedded engine writes fewer of
// (checkpointStore).
func sessionItemKinds(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []string
	for line := range strings.Lines(string(data)) {
		i := strings.Index(line, `"Kind":"`)
		if i < 0 {
			i = strings.Index(line, `"type":"`)
		}
		if i < 0 {
			out = append(out, "?")

			continue
		}
		rest := line[i+8:]
		if kind := rest[:strings.IndexByte(rest, '"')]; kind != "operation" {
			out = append(out, kind)
		}
	}

	return out
}

func TestEmbedded_PreToolUseHooks(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"rm -rf build", "echo original"}},
		fakellm.Reply{Text: "done"},
	)
	runner, err := hooks.New([]hooks.Hook{
		{Event: hooks.PreToolUse, Matcher: "Bash", Source: hooks.SourceUser, Command: `
			input=$(cat)
			case "$input" in
			*rm\ -rf*) echo 'destructive commands are not allowed' >&2; exit 2 ;;
			*original*) echo '{"hookSpecificOutput":{"updatedInput":{"command":"echo rewritten"}}}' ;;
			esac`},
	}, nil, e.Workspace)
	require.NoError(t, err)
	eng := embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Hooks: runner})
	s, err := session.Open(context.Background(), eng, session.Options{Settings: e.settings(), Hooks: runner})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ev := &events{t: t, s: s}

	_, err = s.Submit("clean up")
	require.NoError(t, err)
	result := ev.finished()

	assert.Equal(t, core.StatusOK, result.Status)
	reqs := e.llm.Requests()
	last := strings.Join(reqs[len(reqs)-1].ToolOutputs, "\n")
	assert.Contains(t, last, "Error: blocked by a PreToolUse hook: destructive commands are not allowed", "a block is the tool's error result")
	assert.Contains(t, last, "rewritten", "updatedInput replaced the command")
	assert.Equal(t, 4, countKind[session.HookRan](ev.all)) // each hook also reports "running" as it starts
}
