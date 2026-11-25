package embedded_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/toolpolicy"
	"github.com/viktordanov/uah/testing/fakellm"
)

// advisorProviders are the default providers with the advisor on openai,
// whose base URL the tests point at fakellm.
func advisorProviders() []embedded.Provider {
	ps := embedded.DefaultProviders()
	for i := range ps {
		if ps[i].Name == "openai" {
			ps[i].Advisor = true
		}
	}

	return ps
}

func (e *env) advising(a embedded.Advisor) *embedded.Engine {
	return embedded.New(embedded.Config{StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Providers: advisorProviders(), Advisor: a})
}

func advisorCall() fakellm.Reply {
	return fakellm.Reply{Calls: []fakellm.Call{{Name: engine.AdvisorToolName, Args: "{}"}}}
}

// TestAdvisor_AdvisesTheAgent: the agent calls the advisor, the advisor's
// model reads the conversation with its own prompt and no tools, and the
// agent's next request has the advice as the call's result.
func TestAdvisor_AdvisesTheAgent(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"echo looked around"}},
		advisorCall(),
		fakellm.Reply{Text: "1. Check the empty input."},
		fakellm.Reply{Text: "done"},
	)
	s, ev := e.open(t, e.advising(embedded.Advisor{Model: "gpt-advisor", Effort: "xhigh"}), "")
	_, err := s.Submit("write the parser")
	require.NoError(t, err)
	assert.Equal(t, "done", ev.finished().Answer)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	assert.Contains(t, reqs[0].ToolNames, engine.AdvisorToolName)
	assert.JSONEq(t, `{"type":"object","properties":{},"additionalProperties":false}`, reqs[0].Tools[engine.AdvisorToolName])

	adv := reqs[2]
	assert.Equal(t, "gpt-advisor", adv.Model)
	assert.Equal(t, "xhigh", adv.Effort)
	assert.Empty(t, adv.ToolNames, "the advisor runs no tools")
	assert.Contains(t, adv.System, "You advise a coding agent")
	// The client sends a key's SHA-256.
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(s.ID()))), reqs[1].CacheKey, "the turn's key is the session's")
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte("uah-advisor-"+s.ID()))), adv.CacheKey, "the session's key, under the advisor's prefix")
	require.Len(t, adv.UserTexts, 3)
	assert.Contains(t, adv.UserTexts[0], "<agent_instructions>\n"+reqs[1].System)
	assert.Contains(t, adv.UserTexts[0], "Its tools: ")
	assert.Equal(t, "write the parser", adv.UserTexts[1])
	assert.Contains(t, adv.UserTexts[2], "Give your advice now")
	assert.Equal(t, reqs[1].CallIDs[0], adv.CallIDs[0], "the history as the agent saw it")
	require.Len(t, adv.ToolOutputs, 2)
	assert.Contains(t, adv.ToolOutputs[0], "looked around")
	assert.Contains(t, adv.ToolOutputs[1], "This is the call to you")

	next := reqs[3]
	assert.Equal(t, "gpt-test", next.Model, "the agent's own model again")
	assert.Equal(t, "1. Check the empty input.", next.ToolOutputs[len(next.ToolOutputs)-1])
	assert.NotContains(t, strings.Join(next.ToolOutputs, "\n"), "Still running")

	var out engine.ToolOutput
	for _, x := range ev.all {
		if o, ok := x.(engine.ToolOutput); ok && o.Result != "" {
			out = o
		}
	}
	assert.Equal(t, "1. Check the empty input.", out.Result, "the transcript shows the advice")

	data, err := os.ReadFile(filepath.Join(e.StateDir, "sessions", s.ID()+engine.AdvisorLogSuffix))
	require.NoError(t, err)
	var rec engine.AdvisorRecord
	require.NoError(t, json.Unmarshal(data, &rec))
	assert.Equal(t, "gpt-advisor", rec.Model)
	assert.Equal(t, "xhigh", rec.Effort)
	assert.Positive(t, rec.Usage.Input)
}

// TestAdvisor_SiblingCallsGetResults: a call beside the advisor's, still
// running, and the advisor's own get placeholder results in its request,
// as the Responses API wants every call answered; the default effort is
// the session's.
func TestAdvisor_SiblingCallsGetResults(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"sleep 1; echo slow"}, Calls: []fakellm.Call{{Name: engine.AdvisorToolName, Args: "{}"}}},
		fakellm.Reply{Text: "advice"},
		fakellm.Reply{Text: "done"},
	)
	s, ev := e.open(t, e.advising(embedded.Advisor{Model: "gpt-advisor"}), "")
	_, err := s.Submit("go")
	require.NoError(t, err)
	assert.Equal(t, "done", ev.finished().Answer)

	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	adv := reqs[1]
	assert.Equal(t, "high", adv.Effort, "the session's effort")
	require.Len(t, adv.ToolOutputs, 2)
	assert.Contains(t, adv.ToolOutputs[0], "Still running when the agent called the advisor")
	assert.Contains(t, adv.ToolOutputs[1], "This is the call to you")
	assert.Contains(t, reqs[2].ToolOutputs, "advice")
	assert.Contains(t, strings.Join(reqs[2].ToolOutputs, "\n"), "slow", "the turn's results wait for both")
}

// TestAdvisor_LimitPerRun: a call past max_uses returns the limit without
// a model call, and the run goes on; the next run counts again.
func TestAdvisor_LimitPerRun(t *testing.T) {
	e := newEnv(t,
		advisorCall(), fakellm.Reply{Text: "first advice"},
		advisorCall(),
		fakellm.Reply{Text: "done"},
		advisorCall(), fakellm.Reply{Text: "next run's advice"},
		fakellm.Reply{Text: "done again"},
	)
	s, ev := e.open(t, e.advising(embedded.Advisor{Model: "gpt-advisor", MaxUses: 1}), "")
	ask(t, s, ev, "go")
	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	assert.Equal(t, "first advice", reqs[2].ToolOutputs[0])
	assert.Equal(t, "The advisor's limit for this run is reached (1 call); go on without it.", reqs[3].ToolOutputs[1])
	assert.Contains(t, reqs[0].Tools, engine.AdvisorToolName)

	ask(t, s, ev, "again")
	reqs = e.llm.Requests()
	require.Len(t, reqs, 7)
	assert.Equal(t, "gpt-advisor", reqs[5].Model)
	assert.Equal(t, "next run's advice", reqs[6].ToolOutputs[len(reqs[6].ToolOutputs)-1])
}

// TestAdvisor_TimeoutIsAResult: a call that outlives the timeout returns
// why, and the run goes on.
func TestAdvisor_TimeoutIsAResult(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	e := newEnv(t, advisorCall(), fakellm.Reply{Text: "too late", Gate: hold}, fakellm.Reply{Text: "done"})
	s, ev := e.open(t, e.advising(embedded.Advisor{Model: "gpt-advisor", Timeout: 300 * time.Millisecond}), "")
	_, err := s.Submit("go")
	require.NoError(t, err)
	result := ev.finished()
	assert.Equal(t, core.StatusOK, result.Status)
	assert.Equal(t, "done", result.Answer)
	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "The advisor is unavailable (timeout after 300ms); go on without it.", reqs[2].ToolOutputs[0])
}

// TestAdvisor_InterruptCancelsTheCall: an interrupt cancels the advisor's
// model call at once.
func TestAdvisor_InterruptCancelsTheCall(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	e := newEnv(t, advisorCall(), fakellm.Reply{Text: "never", Gate: hold}, fakellm.Reply{Text: "after"})
	s, ev := e.open(t, e.advising(embedded.Advisor{Model: "gpt-advisor"}), "")
	_, err := s.Submit("go")
	require.NoError(t, err)
	waitSeen(t, e.llm, 2)
	started := time.Now()
	require.NoError(t, s.Interrupt())
	assert.Equal(t, core.StatusInterrupted, ev.finished().Status)
	assert.Less(t, time.Since(started), 5*time.Second, "the hard stop cancels the advisor")
	ev.idle()

	_, err = s.Submit("carry on")
	require.NoError(t, err)
	assert.Equal(t, "after", ev.finished().Answer)
	reqs := e.llm.Requests()
	require.Len(t, reqs, 3)
	assert.Contains(t, strings.Join(reqs[2].ToolOutputs, "\n"), "canceled")
}

// TestAdvisor_SendsTheRemoteCompactionItem: after a remote compaction the
// advisor's request carries the provider's item where the placeholder was,
// as the agent's requests do.
func TestAdvisor_SendsTheRemoteCompactionItem(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"echo before"}},
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Compaction: "ENCRYPTED-1"},
		advisorCall(),
		fakellm.Reply{Text: "advice"},
		fakellm.Reply{Text: "answer two"},
	)
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Providers: advisorProviders(),
		Compaction: compaction.Settings{Remote: true}, Advisor: embedded.Advisor{Model: "gpt-advisor"},
	})
	s, ev := e.open(t, eng, "")
	ask(t, s, ev, "first")
	require.NoError(t, s.Compact())
	ask(t, s, ev, "second")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 6)
	adv := reqs[4]
	assert.Equal(t, "gpt-advisor", adv.Model)
	assert.True(t, slices.ContainsFunc(adv.Input, func(raw json.RawMessage) bool {
		return strings.Contains(string(raw), `"encrypted_content":"ENCRYPTED-1"`)
	}), "the item, not its placeholder")
	for _, text := range adv.UserTexts {
		assert.NotContains(t, text, "uah-remote-compaction", "the placeholder never reaches the provider")
	}
}

// TestAdvisor_NotOffered: no model, a provider without the model
// backend, UAH_ADVISOR=off, the tool policy, and /advisor off leave the
// tool out of the next run.
func TestAdvisor_NotOffered(t *testing.T) {
	for name, cfg := range map[string]embedded.Config{
		"no model":        {Providers: advisorProviders()},
		"provider":        {Advisor: embedded.Advisor{Model: "gpt-advisor"}},
		"UAH_ADVISOR=off": {Providers: advisorProviders(), Advisor: embedded.Advisor{Model: "gpt-advisor", Off: true}},
		"policy":          {Providers: advisorProviders(), Advisor: embedded.Advisor{Model: "gpt-advisor"}, Tools: toolpolicy.Policy{Deny: []string{engine.AdvisorToolName}}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, fakellm.Reply{Text: "done"})
			cfg.StateDir, cfg.Provider, cfg.Getenv = e.StateDir, "openai", e.getenv
			s, ev := e.open(t, embedded.New(cfg), "")
			ask(t, s, ev, "go")
			assert.NotContains(t, e.llm.Requests()[0].ToolNames, engine.AdvisorToolName)
		})
	}

	e := newEnv(t, fakellm.Reply{Text: "one"}, fakellm.Reply{Text: "two"})
	eng := e.advising(embedded.Advisor{Model: "gpt-advisor"})
	s, ev := e.open(t, eng, "")
	ask(t, s, ev, "go")
	status, ok, err := s.Advisor("off")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Contains(t, status, "off for this session")
	ask(t, s, ev, "again")
	reqs := e.llm.Requests()
	assert.Contains(t, reqs[0].ToolNames, engine.AdvisorToolName)
	assert.NotContains(t, reqs[1].ToolNames, engine.AdvisorToolName, "/advisor off")
}

// TestAdvisor_SeesTheWebSearches: the agent's hosted web searches go into
// the advisor's request as into the agent's next one; the advisor itself
// gets no tool to search with.
func TestAdvisor_SeesTheWebSearches(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "Searching first.", Searches: []fakellm.Search{{Query: "latest Go release"}}},
		advisorCall(),
		fakellm.Reply{Text: "advice"},
		fakellm.Reply{Text: "done"},
	)
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Providers: advisorProviders(), WebSearch: true,
		Advisor: embedded.Advisor{Model: "gpt-advisor"},
	})
	s, ev := e.open(t, eng, "")
	ask(t, s, ev, "which Go is latest?")
	ask(t, s, ev, "check it with the advisor")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 4)
	assert.True(t, searched(reqs[1]), "the agent's request")
	assert.True(t, searched(reqs[2]), "the advisor's request")
	assert.Empty(t, reqs[2].ToolNames)
}

// commandAdvisor is an engine on the plain openai provider, which has no
// model backend, with the advisor's command backend: /bin/sh running
// script in the workspace.
func (e *env) commandAdvisor(script string, search bool) *embedded.Engine {
	return embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, WebSearch: search,
		Advisor: embedded.Advisor{Command: []string{"/bin/sh", "-c", script}, Program: "/bin/sh"},
	})
}

// TestAdvisor_Command: on a provider without the model backend, the
// command reads the text transcript on stdin, in the workspace, with
// UAH_ADVISOR=off, and its stdout is the call's result; the advisor log
// names the command, with no tokens.
func TestAdvisor_Command(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Text: "Searching first.", Searches: []fakellm.Search{{Query: "latest Go release"}}, Commands: []string{"echo looked around"}},
		advisorCall(),
		fakellm.Reply{Text: "done"},
	)
	s, ev := e.open(t, e.commandAdvisor(`cat > in.txt; echo "$UAH_ADVISOR" > env.txt; echo "1. Check the empty input."`, true), "")
	ask(t, s, ev, "write the parser")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 3, "no model call for the advisor")
	assert.Contains(t, reqs[0].ToolNames, engine.AdvisorToolName)
	assert.Equal(t, "1. Check the empty input.", reqs[2].ToolOutputs[len(reqs[2].ToolOutputs)-1])

	in, err := os.ReadFile(filepath.Join(e.Workspace, "in.txt"))
	require.NoError(t, err)
	for _, want := range []string{"<agent_instructions>\n", "] ---\nwrite the parser\n", "looked around", "] ---\nsearched: latest Go release\n", "The conversation above is the agent's", "Give your advice now."} {
		assert.Contains(t, string(in), want)
	}
	env, err := os.ReadFile(filepath.Join(e.Workspace, "env.txt"))
	require.NoError(t, err)
	assert.Equal(t, "off\n", string(env))

	data, err := os.ReadFile(filepath.Join(e.StateDir, "sessions", s.ID()+engine.AdvisorLogSuffix))
	require.NoError(t, err)
	var rec engine.AdvisorRecord
	require.NoError(t, json.Unmarshal(data, &rec))
	assert.Equal(t, "command:/bin/sh", rec.Model)
	assert.Equal(t, engine.AdvisorUsage{}, rec.Usage)
	assert.Empty(t, rec.Error)
}

// TestAdvisor_CommandFails: a command that fails is an unavailable result
// with the end of its stderr, and a failed call, so the run's stats show
// it; the run goes on, and the log has the error.
func TestAdvisor_CommandFails(t *testing.T) {
	e := newEnv(t, advisorCall(), fakellm.Reply{Text: "done"})
	s, ev := e.open(t, e.commandAdvisor(`echo "no login" >&2; exit 3`, false), "")
	_, err := s.Submit("go")
	require.NoError(t, err)
	result := ev.finished()
	assert.Equal(t, core.StatusOK, result.Status)
	assert.Equal(t, "done", result.Answer)
	assert.Equal(t, 1, result.Stats.FailedToolCalls)
	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "The advisor is unavailable (the command /bin/sh failed with exit status 3: no login); go on without it.", reqs[1].ToolOutputs[0])
	data, err := os.ReadFile(filepath.Join(e.StateDir, "sessions", s.ID()+engine.AdvisorLogSuffix))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"error":"the command /bin/sh failed with exit status 3: no login"`)
}

// TestAdvisor_InterruptStopsTheCommand: an interrupt kills the command's
// process group, a grandchild included.
func TestAdvisor_InterruptStopsTheCommand(t *testing.T) {
	e := newEnv(t, advisorCall(), fakellm.Reply{Text: "after"})
	pidFile := filepath.Join(e.Workspace, "child.pid")
	s, ev := e.open(t, e.commandAdvisor(`sleep 60 & echo $! > child.pid; sleep 60`, false), "")
	_, err := s.Submit("go")
	require.NoError(t, err)
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 10*time.Second, 20*time.Millisecond)
	started := time.Now()
	require.NoError(t, s.Interrupt())
	assert.Equal(t, core.StatusInterrupted, ev.finished().Status)
	assert.Less(t, time.Since(started), 5*time.Second)
	assert.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 20*time.Millisecond, "the grandchild is gone")
}

// TestAdvisor_CommandAfterRemoteCompaction: after a remote compaction a
// command reads a note in place of the encrypted item, after the user
// message the compaction kept; neither the item nor its placeholder
// reaches it.
func TestAdvisor_CommandAfterRemoteCompaction(t *testing.T) {
	e := newEnv(t,
		fakellm.Reply{Commands: []string{"echo before"}},
		fakellm.Reply{Text: "answer one"},
		fakellm.Reply{Compaction: "ENCRYPTED-1"},
		advisorCall(),
		fakellm.Reply{Text: "answer two"},
	)
	eng := embedded.New(embedded.Config{
		StateDir: e.StateDir, Provider: "openai", Getenv: e.getenv, Compaction: compaction.Settings{Remote: true},
		Advisor: embedded.Advisor{Command: []string{"/bin/sh", "-c", "cat > in.txt; echo advice"}, Program: "/bin/sh"},
	})
	s, ev := e.open(t, eng, "")
	ask(t, s, ev, "first")
	require.NoError(t, s.Compact())
	ask(t, s, ev, "second")

	reqs := e.llm.Requests()
	require.Len(t, reqs, 5)
	assert.Equal(t, "advice", reqs[4].ToolOutputs[len(reqs[4].ToolOutputs)-1])
	in, err := os.ReadFile(filepath.Join(e.Workspace, "in.txt"))
	require.NoError(t, err)
	text := string(in)
	assert.Contains(t, text, "] ---\nfirst\n\n--- note [")
	assert.Contains(t, text, "compacted by the provider into an encrypted item")
	assert.Contains(t, text, "] ---\nsecond\n")
	assert.NotContains(t, text, "ENCRYPTED-1")
	assert.NotContains(t, text, "uah-remote-compaction")
	assert.NotContains(t, text, "echo before", "the compaction covered the first run's call")
}

// TestAdvisor_CommandLongAdvice: advice past the cap reaches the agent cut,
// with the note that says so.
func TestAdvisor_CommandLongAdvice(t *testing.T) {
	e := newEnv(t, advisorCall(), fakellm.Reply{Text: "done"})
	s, ev := e.open(t, e.commandAdvisor(`head -c 50000 /dev/zero | tr '\0' a`, false), "")
	ask(t, s, ev, "go")
	reqs := e.llm.Requests()
	require.Len(t, reqs, 2)
	out := reqs[1].ToolOutputs[len(reqs[1].ToolOutputs)-1]
	assert.Equal(t, strings.Repeat("a", 32000)+"\n\n(The advice was cut at 32000 bytes.)", out, "the runner does not cut it again")
}
