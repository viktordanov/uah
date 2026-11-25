package embedded

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/llm/responsesapi"
	"github.com/viktordanov/uah-core/harness/operation"
	"github.com/viktordanov/uah-core/harness/tool"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/toolpolicy"
)

func advisorMessage(role llm.Role, text string) llm.Item {
	return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
}

func advisorCall(id, name string) llm.Item {
	return llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: name, Arguments: "{}"}}
}

func advisorResult(id, text string) llm.Item {
	return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}}
}

// TestLastRequests_Transcript: the advisor reads the last request without
// its system message, then the response up to its own call, with a
// placeholder for each call still without a result, and the request's
// remote item; a response without the call is a stale pair.
func TestLastRequests_Transcript(t *testing.T) {
	var l lastRequests
	record := l.recorder("s")
	req := llm.Request{
		Model: llm.Model{ID: "m", ReasoningEffort: "low"},
		Input: []llm.Item{advisorMessage(llm.RoleSystem, "sys"), advisorMessage(llm.RoleUser, "fix it"), advisorCall("a", "Bash"), advisorResult("a", "ok")},
		Tools: []llm.Tool{{Name: "Bash"}, {Name: toolAdvisor}},
	}
	reasoning := llm.Item{Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: []byte(`{"type":"reasoning","encrypted_content":"x"}`)}}
	resp := llm.Response{Output: []llm.Item{reasoning, advisorCall("b", "Bash"), advisorCall("adv", toolAdvisor), advisorCall("c", "Bash")}}
	item := remoteItem{marker: "m", item: []byte(`{"type":"compaction"}`)}
	record(req, resp, "high", item)

	tr, ok := l.transcript("s", "adv")
	require.True(t, ok)
	assert.Equal(t, item, tr.remote)
	assert.Equal(t, "sys", tr.system)
	assert.Equal(t, []string{"Bash", toolAdvisor}, tr.tools)
	assert.Equal(t, "m", tr.model)
	assert.Equal(t, "high", tr.effort, "the session's effort, not the request's")
	assert.Equal(t, []llm.Item{
		advisorMessage(llm.RoleUser, "fix it"), advisorCall("a", "Bash"), advisorResult("a", "ok"),
		reasoning, advisorCall("b", "Bash"), advisorCall("adv", toolAdvisor),
		advisorResult("b", advisorPendingResult), advisorResult("adv", advisorOwnResult),
	}, tr.items, "the calls after it are cut; the calls without a result get one")
	assert.Len(t, req.Input, 4, "the recorded request is not changed")

	_, ok = l.transcript("s", "c9")
	assert.False(t, ok, "a call the response does not hold")
	_, ok = l.transcript("other", "adv")
	assert.False(t, ok, "no request")

	record(req, llm.Response{Output: []llm.Item{advisorMessage(llm.RoleAssistant, "done")}}, "high", remoteItem{})
	_, ok = l.transcript("s", "adv")
	assert.False(t, ok, "a later request replaced it")

	record(req, resp, "high", remoteItem{})
	l.rewind("s", 1, "fix it", 0)
	_, ok = l.transcript("s", "adv")
	assert.False(t, ok, "a rewind drops the response")
}

// scriptedClient answers each request with the next of its replies.
type scriptedClient struct {
	reqs    []llm.Request
	replies []func() (llm.Response, error)
}

func (c *scriptedClient) Respond(_ context.Context, req llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	c.reqs = append(c.reqs, req)
	if len(c.replies) == 0 {
		return llm.Response{Output: []llm.Item{advisorMessage(llm.RoleAssistant, "ok")}}, nil
	}
	next := c.replies[0]
	c.replies = c.replies[1:]

	return next()
}

func (*scriptedClient) Close() error { return nil }

type seenRequest struct {
	req    llm.Request
	resp   llm.Response
	effort string
}

// TestSwitcher_SeesTurnRequestsOnly: a turn request is recorded with its
// response and the session's effort, before adaptive effort lowers it; a
// direct call, the advisor's included, is not, so it never replaces the
// session's last request.
func TestSwitcher_SeesTurnRequestsOnly(t *testing.T) {
	client := &scriptedClient{}
	sw, err := newSwitcher("m", variant{}, 1, func(variant) (Client, error) { return client, nil })
	require.NoError(t, err)
	var seen []seenRequest
	sw.seen = func(req llm.Request, resp llm.Response, effort string, _ remoteItem) {
		seen = append(seen, seenRequest{req, resp, effort})
	}
	sw.adaptive.steps = 2
	followUp := llm.Request{Model: llm.Model{ID: "m", ReasoningEffort: "high"}, Input: []llm.Item{
		advisorMessage(llm.RoleSystem, "sys"), advisorMessage(llm.RoleUser, "go"), advisorCall("a", "Bash"), advisorResult("a", "ok"),
	}}

	_, err = sw.Respond(t.Context(), followUp, llm.RequestOptions{})
	require.NoError(t, err)
	_, err = sw.direct().Respond(t.Context(), llm.Request{Input: followUp.Input}, llm.RequestOptions{})
	require.NoError(t, err)
	_, err = sw.directKind(kindAdvisor).Respond(t.Context(), llm.Request{Input: followUp.Input}, llm.RequestOptions{})
	require.NoError(t, err)

	require.Len(t, client.reqs, 3)
	require.Len(t, seen, 1, "only the turn request")
	assert.Equal(t, llm.ReasoningEffortLow, client.reqs[0].Model.ReasoningEffort, "adaptive effort lowered the follow-up")
	assert.Equal(t, "high", seen[0].effort, "the session's effort")
	assert.Equal(t, "ok", seen[0].resp.Output[0].Data.(llm.Message).Text) //nolint:forcetypeassert // the scripted answer
}

// TestSwitcher_FallBackIsSeen: the retry of a request whose effort updates
// the backend rejected is recorded, as the request that went out.
func TestSwitcher_FallBackIsSeen(t *testing.T) {
	rejection := &responsesapi.APIError{StatusCode: 400, Code: "invalid_value", Message: "Invalid value: 'configuration_update'.", Param: "input[2].type"}
	client := &scriptedClient{replies: []func() (llm.Response, error){
		func() (llm.Response, error) { return llm.Response{}, rejection },
		func() (llm.Response, error) {
			return llm.Response{Output: []llm.Item{advisorCall("adv", toolAdvisor)}}, nil
		},
	}}
	sw, err := newSwitcher("m", variant{}, 1, func(variant) (Client, error) { return client, nil })
	require.NoError(t, err)
	sw.base, sw.updates = "high", func(string) bool { return true }
	var seen []seenRequest
	sw.seen = func(req llm.Request, resp llm.Response, effort string, _ remoteItem) {
		seen = append(seen, seenRequest{req, resp, effort})
	}
	update := llm.Item{Type: llm.ItemConfigurationUpdate, Data: llm.ConfigurationUpdate{ReasoningEffort: "medium"}}
	req := llm.Request{Model: llm.Model{ID: "m", ReasoningEffort: "high"}, Input: []llm.Item{advisorMessage(llm.RoleSystem, "sys"), update, advisorMessage(llm.RoleUser, "go")}}

	_, err = sw.Respond(t.Context(), req, llm.RequestOptions{})
	require.NoError(t, err)
	require.Len(t, client.reqs, 2)
	require.Len(t, seen, 1)
	assert.NotContains(t, seen[0].req.Input, update, "the retry, without the updates")
	assert.Equal(t, "high", seen[0].effort, "the session's effort, not an update's")
	assert.Equal(t, toolAdvisor, seen[0].resp.Output[0].Data.(llm.ToolCall).Name) //nolint:forcetypeassert // the scripted answer
}

// TestWaitHold_Advisor: an advisor call is held for its timeout plus the
// slack, unless that is within the hold.
func TestWaitHold_Advisor(t *testing.T) {
	call := llm.ToolCall{Name: toolAdvisor, Arguments: "{}"}
	assert.Equal(t, 10*time.Minute+waitSlack, waitHold(5*time.Minute, time.Hour, 10*time.Minute, nil)(call))
	assert.Zero(t, waitHold(5*time.Minute, time.Hour, 2*time.Second, nil)(call), "a short timeout keeps the hold")
	assert.Zero(t, waitHold(5*time.Minute, time.Hour, 10*time.Minute, nil)(llm.ToolCall{Name: "Bash"}))
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute: "10m", 2 * time.Second: "2s", time.Hour: "1h", 90 * time.Minute: "1h30m", 1500 * time.Millisecond: "1.5s",
	} {
		assert.Equal(t, want, shortDuration(d))
	}
}

func advisorEngine(t *testing.T, provider string, a Advisor) *Engine {
	t.Helper()

	return New(Config{StateDir: t.TempDir(), Provider: provider, Advisor: a})
}

// TestEngine_Advisor: /advisor shows the advisor, turns it off and on for
// the session tree, and picks its model; a subagent follows its root.
func TestEngine_Advisor(t *testing.T) {
	e := advisorEngine(t, "openai-codex", Advisor{Model: "m1"})
	status, err := e.Advisor("root", "openai-codex", "")
	require.NoError(t, err)
	assert.Equal(t, "advisor: m1 at the session's effort; a call waits at most 10m, and a run makes at most 5", status)

	e.SetCacheKey("child", "root")
	status, err = e.Advisor("root", "openai-codex", "off")
	require.NoError(t, err)
	assert.Equal(t, "advisor: off for this session (/advisor on turns it back on)", status)
	_, on := e.advisorFor("openai-codex", "child")
	assert.False(t, on, "the subagent follows its root")

	_, err = e.Advisor("root", "openai-codex", "on")
	require.NoError(t, err)
	setup, on := e.advisorFor("openai-codex", "child")
	assert.True(t, on)
	assert.Equal(t, advisorSetup{model: "m1"}, setup)

	status, err = e.Advisor("child", "openai-codex", "m2 xhigh")
	require.NoError(t, err)
	assert.Contains(t, status, "advisor: m2 at xhigh")
	setup, _ = e.advisorFor("openai-codex", "root")
	assert.Equal(t, advisorSetup{model: "m2", effort: "xhigh"}, setup, "a subagent's /advisor is its tree's")

	_, err = e.Advisor("root", "openai-codex", "m2 hot")
	require.ErrorContains(t, err, `unknown effort "hot"`)
	_, err = e.Advisor("root", "openai-codex", "a b c")
	require.ErrorContains(t, err, "usage")
	_, err = e.Advisor("root", "openai-codex", "off typo")
	require.ErrorContains(t, err, "usage")
	setup, on = e.advisorFor("openai-codex", "root")
	assert.True(t, on, "a malformed off changes nothing")
	assert.Equal(t, advisorSetup{model: "m2", effort: "xhigh"}, setup)

	e.Forget("root")
	setup, _ = e.advisorFor("openai-codex", "root")
	assert.Equal(t, advisorSetup{model: "m1"}, setup, "the choice ends with the session")

	none := advisorEngine(t, "openai-codex", Advisor{})
	status, err = none.Advisor("s", "openai-codex", "")
	require.NoError(t, err)
	assert.Contains(t, status, "set [advisor] model")
	_, err = none.Advisor("s", "openai-codex", "on")
	require.ErrorContains(t, err, "no advisor is set")
	_, err = none.Advisor("s", "openai-codex", "command")
	require.ErrorContains(t, err, "no advisor command is set")
	_, err = none.Advisor("s", "openai-codex", "gpt-6.1-sol")
	require.NoError(t, err, "a model turns it on")
	_, on = none.advisorFor("openai-codex", "s")
	assert.True(t, on)

	other := advisorEngine(t, "openai", Advisor{Model: "m1"})
	status, _ = other.Advisor("s", "openai", "")
	assert.Equal(t, "advisor: off (its model backend needs the openai-codex provider)", status)
	_, err = other.Advisor("s", "openai", "m2")
	require.ErrorContains(t, err, "needs the openai-codex provider")
	_, err = other.Advisor("s", "openai", "on")
	require.ErrorContains(t, err, "needs the openai-codex provider", "a model is all there is to turn on")
	_, err = other.Advisor("s", "openai", "off")
	require.NoError(t, err, "off always works")
	status, err = other.Advisor("s", "openai-codex", "m2")
	require.NoError(t, err, "the session's provider now, not the engine's first")
	assert.Contains(t, status, "advisor: m2")

	bare := advisorEngine(t, "openai", Advisor{})
	status, _ = bare.Advisor("s", "openai", "")
	assert.Equal(t, "advisor: off (set [advisor] command in the config)", status)

	why := "its program, /ws/bin/claude, is where the agent's commands can write"
	for _, provider := range []string{"openai", "openai-codex"} {
		refused := advisorEngine(t, provider, Advisor{Refused: why})
		status, _ = refused.Advisor("s", provider, "")
		assert.Equal(t, "advisor: off ("+why+")", status, "a command uah refused, not one to set: %s", provider)
		_, err = refused.Advisor("s", provider, "on")
		require.ErrorContains(t, err, why)
		_, err = refused.Advisor("s", provider, "command")
		require.ErrorContains(t, err, why)
	}

	unset := advisorEngine(t, "openai-codex", Advisor{})
	status, _ = unset.Advisor("s", "openai-codex", "off")
	assert.Equal(t, "advisor: off for this session (/advisor <model> picks one)", status, "no model for /advisor on")

	off := advisorEngine(t, "openai-codex", Advisor{Model: "m1", Off: true})
	status, _ = off.Advisor("s", "openai-codex", "off")
	assert.Equal(t, "advisor: off (UAH_ADVISOR=off)", status, "the reason that keeps it off")
	_, err = off.Advisor("s", "openai-codex", "m2")
	require.ErrorContains(t, err, "UAH_ADVISOR=off")

	denied := New(Config{StateDir: t.TempDir(), Provider: "openai-codex", Advisor: Advisor{Model: "m1"}, Tools: toolpolicy.Policy{Deny: []string{toolAdvisor}}})
	status, _ = denied.Advisor("s", "openai-codex", "")
	assert.Equal(t, "advisor: off (the tool policy does not allow it)", status)
}

// TestEngine_AdvisorCommand: a configured command is the advisor on any
// provider; /advisor <model> replaces it on openai-codex only, and
// /advisor command goes back to it; the status names the program and the
// input.
func TestEngine_AdvisorCommand(t *testing.T) {
	cmd := Advisor{Command: []string{"claude", "-p"}, Input: engine.AdvisorInputJSON}
	for _, provider := range []string{"openai-codex", "openai"} {
		e := advisorEngine(t, provider, cmd)
		setup, on := e.advisorFor(provider, "s")
		assert.True(t, on, provider)
		assert.Equal(t, advisorSetup{command: []string{"claude", "-p"}}, setup, provider)
		status, err := e.Advisor("s", provider, "")
		require.NoError(t, err)
		assert.Equal(t, "advisor: the command claude, which reads the transcript as json; a call waits at most 10m, and a run makes at most 5", status, provider)
		assert.True(t, e.advisorOffered(provider, "s"), provider)

		_, err = e.Advisor("s", provider, "off")
		require.NoError(t, err)
		_, on = e.advisorFor(provider, "s")
		assert.False(t, on, provider)
		status, err = e.Advisor("s", provider, "on")
		require.NoError(t, err, provider)
		assert.Contains(t, status, "advisor: the command claude", provider)
	}

	other := advisorEngine(t, "openai", cmd)
	_, err := other.Advisor("s", "openai", "gpt-6.1-sol")
	require.ErrorContains(t, err, "the advisor's model backend needs the openai-codex provider; this session's is openai")
	_, on := other.advisorFor("openai", "s")
	assert.True(t, on, "a refused model keeps the command")

	codex := advisorEngine(t, "openai-codex", cmd)
	status, err := codex.Advisor("s", "openai-codex", "gpt-6.1-sol high")
	require.NoError(t, err)
	assert.Contains(t, status, "advisor: gpt-6.1-sol at high")
	setup, _ := codex.advisorFor("openai-codex", "s")
	assert.Equal(t, advisorSetup{model: "gpt-6.1-sol", effort: "high"}, setup, "the model replaces the command")
	status, _ = codex.Advisor("s", "openai", "")
	assert.Equal(t, "advisor: off (its model backend needs the openai-codex provider; /advisor command goes back to the configured command)", status,
		"the model holds after the session moves to another provider")
	status, err = codex.Advisor("s", "openai", "command")
	require.NoError(t, err)
	assert.Contains(t, status, "advisor: the command claude")
	setup, _ = codex.advisorFor("openai-codex", "s")
	assert.Equal(t, advisorSetup{command: []string{"claude", "-p"}}, setup)
	_, err = codex.Advisor("s", "openai", "command now")
	require.ErrorContains(t, err, "usage")
}

type forkingAgents struct{ engine.Subagents }

func (forkingAgents) Forked(id string) bool { return strings.HasSuffix(id, "-fork") }

// TestOffersAdvisor: the main agent gets the advisor while it is on;
// subagents with [advisor] subagents, and a fork, refused at the call; a
// disallowed tool or the policy leaves it out.
func TestOffersAdvisor(t *testing.T) {
	wiringFor := func(a Advisor, p toolpolicy.Policy) *wiring {
		return &wiring{e: New(Config{StateDir: t.TempDir(), Provider: "openai-codex", Advisor: a, Tools: p, Subagents: forkingAgents{}})}
	}
	req := func(id string, disallowed ...string) core.Request {
		return core.Request{Provider: "openai-codex", SessionID: id, DisallowedTools: disallowed}
	}
	child, fork := "subagent-1", "subagent-2-fork"
	on := wiringFor(Advisor{Model: "m"}, toolpolicy.Policy{})
	on.e.SetCacheKey("old-child", "main") // a child from before the prefix, resumed
	for _, tt := range []struct {
		name             string
		w                *wiring
		req              core.Request
		offered, refused bool
	}{
		{"main", on, req("main"), true, false},
		{"another provider", on, core.Request{Provider: "openai", SessionID: "main"}, false, false},
		{"disallowed", on, req("main", toolAdvisor), false, false},
		{"subagent", on, req(child), false, false},
		{"a child without the prefix", on, req("old-child"), false, false},
		{"fork", on, req(fork), true, true},
		{"subagents on", wiringFor(Advisor{Model: "m", Subagents: true}, toolpolicy.Policy{}), req(child), true, false},
		{"no model", wiringFor(Advisor{}, toolpolicy.Policy{}), req("main"), false, false},
		{"a command on another provider", wiringFor(Advisor{Command: []string{"claude"}}, toolpolicy.Policy{}), core.Request{Provider: "openai", SessionID: "main"}, true, false},
		{"UAH_ADVISOR=off", wiringFor(Advisor{Model: "m", Off: true}, toolpolicy.Policy{}), req("main"), false, false},
		{"policy", wiringFor(Advisor{Model: "m"}, toolpolicy.Policy{Allow: []string{"Bash"}}), req("main"), false, false},
	} {
		offered, refused := tt.w.offersAdvisor(tt.req)
		assert.Equal(t, tt.offered, offered, tt.name)
		assert.Equal(t, tt.refused, refused, tt.name)
	}
}

// TestAdvisorRegistry: the tool has no parameters and its description says
// when to call it; its name resolves when it is not offered.
func TestAdvisorRegistry(t *testing.T) {
	r := withAdvisor(tool.NewRegistry(tool.StaticTranslators{}), advisorTranslator{offered: true}, 3)
	defs := r.StaticDefinitions()
	require.Len(t, defs, 1)
	tl := defs[0].Tool
	assert.Equal(t, toolAdvisor, tl.Name)
	assert.Equal(t, map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, tl.Parameters)
	for _, want := range []string{
		"stronger model", "cannot run tools", "before substantive work", "when you are stuck", "changing your approach",
		"before you say the task is done", "on its own", "at most 3", "say so in another call",
	} {
		assert.Contains(t, tl.Description, want)
	}

	off := withAdvisor(tool.NewRegistry(tool.StaticTranslators{}), advisorTranslator{}, 3)
	assert.Empty(t, off.StaticDefinitions())
	tr, ok := off.Resolve(toolAdvisor)
	require.True(t, ok, "a past call resolves")
	assert.Contains(t, tr.Translate(nil, llm.ToolCall{Name: toolAdvisor}).Error, "not available")
	tr, _ = withAdvisor(tool.NewRegistry(tool.StaticTranslators{}), advisorTranslator{offered: true, refused: true}, 3).Resolve(toolAdvisor)
	assert.Equal(t, advisorNotForSubagents, tr.Translate(nil, llm.ToolCall{Name: toolAdvisor}).Error)
}

// blockingAdapter waits for its context, or answers with reply.
type blockingAdapter struct {
	reqs  chan llm.Request
	reply func(context.Context) (llm.Response, error)
}

func (blockingAdapter) Close() error { return nil }

func (a blockingAdapter) Respond(ctx context.Context, req llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	if a.reqs != nil {
		a.reqs <- req
	}
	if a.reply != nil {
		return a.reply(ctx)
	}
	<-ctx.Done()

	return llm.Response{}, ctx.Err()
}

// TestAdvisorModel_Request: the advisor prompt is the instructions, the
// session's system text comes first as quoted context, then the history,
// then the ask; no tools; the effort and cache key; the remote item's
// context.
func TestAdvisorModel_Request(t *testing.T) {
	reqs := make(chan llm.Request, 1)
	answer := func(context.Context) (llm.Response, error) {
		return llm.Response{Output: []llm.Item{advisorMessage(llm.RoleAssistant, "1. Check the edge case.")}, Usage: llm.Usage{InputTokens: 900, CachedInputTokens: 300, OutputTokens: 40}}, nil
	}
	var item remoteItem
	reply := func(ctx context.Context) (llm.Response, error) {
		item, _ = ctx.Value(remoteItemsKey{}).(remoteItem)

		return answer(ctx)
	}
	m := advisorModel{adapter: blockingAdapter{reqs: reqs, reply: reply}, model: "adv", effort: "xhigh", window: 272_000, cacheKey: "root"}
	history := []llm.Item{advisorMessage(llm.RoleUser, "fix it"), advisorCall("adv", toolAdvisor), advisorResult("adv", advisorOwnResult)}
	recorded := remoteItem{marker: "uah-remote-compaction-1", item: []byte(`{"type":"compaction","encrypted_content":"E"}`)}
	a, err := m.advise(t.Context(), advisorTranscript{system: "SYSTEM TEXT", tools: []string{"Bash", toolAdvisor}, items: history, remote: recorded})
	require.NoError(t, err)
	assert.Equal(t, advice{text: "1. Check the edge case.", model: "adv", effort: "xhigh", usage: llm.Usage{InputTokens: 900, CachedInputTokens: 300, OutputTokens: 40}}, a)
	assert.Equal(t, recorded, item, "the item the agent's request carried")

	req := <-reqs
	assert.Equal(t, "adv", req.Model.ID)
	assert.Equal(t, llm.ReasoningEffortXHigh, req.Model.ReasoningEffort)
	assert.Empty(t, req.Tools)
	require.Len(t, req.Input, 6)
	assert.Equal(t, advisorMessage(llm.RoleSystem, advisorPrompt), req.Input[0])
	lead := req.Input[1].Data.(llm.Message) //nolint:forcetypeassert // built above
	assert.Equal(t, llm.RoleUser, lead.Role)
	assert.Contains(t, lead.Text, "<agent_instructions>\nSYSTEM TEXT\n</agent_instructions>")
	assert.Contains(t, lead.Text, "Its tools: Bash, advisor.")
	assert.Equal(t, history, req.Input[2:5])
	assert.Equal(t, advisorMessage(llm.RoleUser, advisorAsk), req.Input[5])

	small := m
	small.window = 2_000
	long := []llm.Item{advisorMessage(llm.RoleUser, strings.Repeat("old ", 4_000)), advisorMessage(llm.RoleUser, "new")}
	_, err = small.advise(t.Context(), advisorTranscript{items: long})
	require.NoError(t, err)
	req = <-reqs
	assert.Equal(t, advisorMessage(llm.RoleUser, "new"), req.Input[2], "the oldest items dropped to fit")
}

// TestAdvisorJobs_Advise: whatever goes wrong is the call's result: a
// stale transcript, the provider's error, the timeout, the limit, and off;
// a call that reached the model is in the advisor log.
func TestAdvisorJobs_Advise(t *testing.T) {
	dir := t.TempDir()
	e := New(Config{StateDir: dir, Provider: "openai-codex", Advisor: Advisor{Model: "adv", Timeout: 50 * time.Millisecond, MaxUses: 3}})
	logPath := filepath.Join(dir, "s"+engine.AdvisorLogSuffix)
	jobs := func(client Client) *advisorJobs {
		sw, err := newSwitcher("m", variant{}, 1, func(variant) (Client, error) { return client, nil })
		require.NoError(t, err)

		return &advisorJobs{ctx: t.Context(), e: e, sw: sw, provider: "openai-codex", sessionID: "s", logPath: logPath}
	}
	failing := jobs(&scriptedClient{replies: []func() (llm.Response, error){
		func() (llm.Response, error) { return llm.Response{}, errors.New("boom\nmore") },
	}})

	text, why := failing.advise(t.Context(), "adv")
	assert.Contains(t, text, "The advisor is unavailable (the conversation it reads is not available")
	assert.Contains(t, why, "the conversation it reads is not available", "the call fails")
	e.last.recorder("s")(llm.Request{Model: llm.Model{ID: "m"}, Input: []llm.Item{advisorMessage(llm.RoleSystem, "sys"), advisorMessage(llm.RoleUser, "go")}},
		llm.Response{Output: []llm.Item{advisorCall("adv", toolAdvisor)}}, "high", remoteItem{})
	text, why = failing.advise(t.Context(), "adv")
	assert.Equal(t, "The advisor is unavailable (failed to call the model: boom); go on without it.", text)
	assert.Equal(t, "failed to call the model: boom", why)

	slow := jobs(blockingAdapter{})
	text, why = slow.advise(t.Context(), "adv")
	assert.Equal(t, "The advisor is unavailable (timeout after 50ms); go on without it.", text)
	assert.Equal(t, "timeout after 50ms", why)
	slow.uses.Store(3)
	text, why = slow.advise(t.Context(), "adv")
	assert.Equal(t, advisorLimit(3), text)
	assert.Empty(t, why, "the limit is not a failure")

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(data), `"model":"adv"`), "the two calls that went out")
	assert.Contains(t, string(data), `"error":"failed to call the model: boom"`)

	_, err = e.Advisor("s", "openai-codex", "off")
	require.NoError(t, err)
	text, why = failing.advise(t.Context(), "adv")
	assert.Equal(t, advisorOff, text)
	assert.Empty(t, why, "off is not a failure")
}

// TestAdvisorJobs_Restored: a call found awaiting after a restart completes
// without a model call, since its transcript was in memory; one the tool
// policy cancels is canceled.
func TestAdvisorJobs_Restored(t *testing.T) {
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: advisorPlanType, Version: advisorPlanVersion, Data: jsontext.Value(`{"call_id":"adv"}`)})
	require.NoError(t, err)
	for status, want := range map[operation.Status]operation.Status{
		operation.StatusAwaiting:  operation.StatusCompleted,
		operation.StatusCanceling: operation.StatusCanceled,
	} {
		jobs := &advisorJobs{ctx: t.Context(), updates: make(chan operation.Operation), cancels: map[operation.ID]context.CancelFunc{}}
		op := operation.Operation{ID: "op-1", Type: spec.Type, Version: spec.Version, MaxOutputLength: spec.MaxOutputLength, State: spec.State, Status: status}
		require.NoError(t, jobs.AddRemoteJob(op))
		select {
		case got := <-jobs.RemoteJobUpdates():
			assert.Equal(t, want, got.Status, status)
			result, err := advisorTranslator{}.TranslateResult("adv", tool.CallStatus{}, []operation.Operation{got})
			require.NoError(t, err)
			if want == operation.StatusCompleted {
				assert.Equal(t, advisorInterrupted, result.Output[0].Value)
			} else {
				assert.Equal(t, "The advisor call was canceled.", result.Output[0].Value)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no update")
		}
	}
}

// TestDirectKind_AdvisorEffort: at ultra an advisor call goes at the
// effort it picked, max included; only ultra goes at ultra.
func TestDirectKind_AdvisorEffort(t *testing.T) {
	built := map[variant]*scriptedClient{}
	sw, err := newSwitcher("m", variant{ultra: true}, 1, func(v variant) (Client, error) {
		c := &scriptedClient{}
		built[v] = c

		return c, nil
	})
	require.NoError(t, err)
	for effort, ultra := range map[llm.ReasoningEffort]bool{llm.ReasoningEffortMax: false, effortUltra: true} {
		_, err := sw.directKind(kindAdvisor).Respond(t.Context(), llm.Request{Model: llm.Model{ID: "adv", ReasoningEffort: effort}}, llm.RequestOptions{})
		require.NoError(t, err)
		c := built[variant{ultra: ultra}]
		require.NotNil(t, c, effort)
		last := c.reqs[len(c.reqs)-1]
		if ultra {
			assert.Empty(t, last.Model.ReasoningEffort, "the ultra client sends it")
		} else {
			assert.Equal(t, llm.ReasoningEffortMax, last.Model.ReasoningEffort)
		}
	}
	_, err = sw.direct().Respond(t.Context(), llm.Request{Model: llm.Model{ReasoningEffort: llm.ReasoningEffortMax}}, llm.RequestOptions{})
	require.NoError(t, err)
	assert.Len(t, built[variant{ultra: true}].reqs, 2, "another direct call keeps the session's ultra")
}

// TestAdvisorModel_TooLarge: instructions that alone take the window send
// nothing.
func TestAdvisorModel_TooLarge(t *testing.T) {
	reqs := make(chan llm.Request, 1)
	m := advisorModel{adapter: blockingAdapter{reqs: reqs}, model: "adv", window: 1_000}
	_, err := m.advise(t.Context(), advisorTranscript{system: strings.Repeat("rule ", 2_000)})
	require.ErrorIs(t, err, errTooLargeForAdvisor)
	assert.Empty(t, reqs)
}
