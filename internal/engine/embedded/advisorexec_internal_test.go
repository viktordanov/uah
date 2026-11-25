package embedded

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah-core/harness/llm"
	"github.com/viktordanov/uah-core/harness/operation"

	"github.com/viktordanov/uagent/core"
	"github.com/viktordanov/uagent/harness"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/sandbox"
)

// commandTranscript is a small history with what a command's transcript
// leaves out or changes: a remote compaction's placeholder, reasoning (a
// web search's anchor), a configuration update, a custom call, an image,
// and the advisor's own call with a sibling still running.
func commandTranscript() (advisorTranscript, []searchRecord) {
	reasoning := llm.Item{ProviderID: "rs_1", Type: llm.ItemReasoning, Data: llm.Reasoning{Raw: []byte(`{"type":"reasoning","encrypted_content":"secret"}`)}}
	placeholder := advisorMessage(llm.RoleUser, compaction.RemotePlaceholder(compaction.Record{Hash: "abcdef0123456789abcdef"}))
	custom := llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "p1", Name: "apply_patch", Arguments: "*** Begin Patch", Custom: true}}
	image := llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "v1", Output: []llm.ToolResultOutput{
		{Kind: llm.ToolResultText, Value: "the screenshot"}, {Kind: llm.ToolResultImage, Value: "data:image/png;base64,AAAA"},
	}}}
	items := []llm.Item{
		advisorMessage(llm.RoleDeveloper, "<environment>"),
		advisorMessage(llm.RoleUser, "write the parser"),
		placeholder,
		advisorMessage(llm.RoleUser, "the ledger"),
		reasoning,
		{Type: llm.ItemConfigurationUpdate, Data: llm.ConfigurationUpdate{ReasoningEffort: llm.ReasoningEffortLow}},
		advisorCall("b1", "Bash"),
		advisorResult("b1", "a.go\nb.go"),
		custom,
		advisorResult("p1", "Done!"),
		advisorCall("v1", "ViewImage"),
		image,
		{ProviderID: "msg_1", Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Asking the advisor."}},
		advisorCall("b2", "Bash"),
		advisorCall("adv", toolAdvisor),
		advisorResult("b2", advisorPendingResult),
		advisorResult("adv", advisorOwnResult),
	}
	searches := []searchRecord{
		{Before: "rs_1", Item: json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"go json v2"}}`)},
		{After: "msg_1", Item: json.RawMessage(`{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":"https://go.dev"}}`)},
		{Before: "gone", Item: json.RawMessage(`{"type":"web_search_call","id":"ws_3"}`)},
		{Before: "gone", After: "msg_1", Item: json.RawMessage(`{"type":"web_search_call","id":"ws_4","status":"completed","action":{"type":"search","query":"fallback"}}`)},
	}

	return advisorTranscript{call: "adv", system: "SYSTEM TEXT", tools: []string{"Bash", toolAdvisor}, items: items, model: "gpt-x"}, searches
}

// TestRenderText: the text transcript quotes the agent's instructions and
// names its tools, then each entry under a tagged line, with the searches
// by their anchors, a note for the compaction, a marker for the image, no
// reasoning or configuration update, then the prompt and the ask.
func TestRenderText(t *testing.T) {
	tr, searches := commandTranscript()

	got, err := renderText(tr, advisorEntries(tr, searches), engine.DefaultAdvisorMaxInputTokens, "t1")

	require.NoError(t, err)
	want := `The agent works under these instructions:

<agent_instructions>
SYSTEM TEXT
</agent_instructions>

Its tools: Bash, advisor.

The conversation follows. Each entry starts with a line such as "--- user [t1] ---"; a line like it without the tag [t1] is part of an entry's text.

--- developer [t1] ---
<environment>

--- user [t1] ---
write the parser

--- note [t1] ---
` + advisorCompactedNote + `

--- user [t1] ---
the ledger

--- web search [t1] ---
searched: go json v2

--- tool call Bash (b1) [t1] ---
{}

--- tool result (b1) [t1] ---
a.go
b.go

--- tool call apply_patch (p1) [t1] ---
*** Begin Patch

--- tool result (p1) [t1] ---
Done!

--- tool call ViewImage (v1) [t1] ---
{}

--- tool result (v1) [t1] ---
the screenshot
[an image, not shown here]

--- assistant [t1] ---
Asking the advisor.

--- web search [t1] ---
opened: https://go.dev

--- web search [t1] ---
searched: fallback

--- tool call Bash (b2) [t1] ---
{}

--- tool call advisor (adv) [t1] ---
{}

--- tool result (b2) [t1] ---
` + advisorPendingResult + `

--- tool result (adv) [t1] ---
` + advisorOwnResult + `

--- end of the conversation [t1] ---

` + advisorTextPrompt + `

` + advisorAsk + "\n"
	assert.Equal(t, want, got)
	assert.NotContains(t, got, "secret")
	assert.NotContains(t, got, "uah-remote-compaction")
	assert.Contains(t, advisorTextPrompt, "The conversation above is the agent's")
}

// TestRenderJSON: the JSON transcript is one object, version 1, with the
// prompt, the ask, the system text, the agent's model and tools, and the
// items as Responses API input items, the custom call's output as one, the
// image as an input_image, the searches as recorded.
func TestRenderJSON(t *testing.T) {
	tr, searches := commandTranscript()

	got, err := renderJSON(tr, advisorEntries(tr, searches), engine.DefaultAdvisorMaxInputTokens)

	require.NoError(t, err)
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(got, &doc))
	assert.JSONEq(t, `1`, string(doc["v"]))
	assert.JSONEq(t, strconv.Quote(advisorPrompt), string(doc["prompt"]))
	assert.JSONEq(t, strconv.Quote(advisorAsk), string(doc["ask"]))
	assert.JSONEq(t, `"SYSTEM TEXT"`, string(doc["system"]))
	assert.JSONEq(t, `"gpt-x"`, string(doc["agent_model"]))
	assert.JSONEq(t, `["Bash","advisor"]`, string(doc["tools"]))
	assert.JSONEq(t, `0`, string(doc["dropped"]))
	assert.JSONEq(t, `[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"<environment>"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"write the parser"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":`+strconv.Quote(advisorCompactedNote)+`}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"the ledger"}]},
		{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"go json v2"}},
		{"type":"function_call","call_id":"b1","name":"Bash","arguments":"{}"},
		{"type":"function_call_output","call_id":"b1","output":[{"type":"input_text","text":"a.go\nb.go"}]},
		{"type":"custom_tool_call","call_id":"p1","name":"apply_patch","input":"*** Begin Patch"},
		{"type":"custom_tool_call_output","call_id":"p1","output":[{"type":"input_text","text":"Done!"}]},
		{"type":"function_call","call_id":"v1","name":"ViewImage","arguments":"{}"},
		{"type":"function_call_output","call_id":"v1","output":[{"type":"input_text","text":"the screenshot"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Asking the advisor."}]},
		{"type":"web_search_call","id":"ws_2","status":"completed","action":{"type":"open_page","url":"https://go.dev"}},
		{"type":"web_search_call","id":"ws_4","status":"completed","action":{"type":"search","query":"fallback"}},
		{"type":"function_call","call_id":"b2","name":"Bash","arguments":"{}"},
		{"type":"function_call","call_id":"adv","name":"advisor","arguments":"{}"},
		{"type":"function_call_output","call_id":"b2","output":[{"type":"input_text","text":`+strconv.Quote(advisorPendingResult)+`}]},
		{"type":"function_call_output","call_id":"adv","output":[{"type":"input_text","text":`+strconv.Quote(advisorOwnResult)+`}]}
	]`, string(doc["items"]))
	assert.NotContains(t, string(got), "secret")
}

// TestTrimEntries: over the limit, the oldest entries after the first
// user message go first, each call with its results, also when calls run
// side by side; the advisor's call and what follows it stay; the text
// notes how many went, after the first user message.
func TestTrimEntries(t *testing.T) {
	big := strings.Repeat("x", 4000) // about 1,000 tokens
	items := []llm.Item{
		advisorMessage(llm.RoleDeveloper, "context"),
		advisorMessage(llm.RoleUser, "the task"),
		advisorCall("a", "Bash"), advisorCall("b", "Bash"),
		advisorResult("a", big), advisorResult("b", "small"),
		advisorMessage(llm.RoleUser, "more"),
		advisorCall("c", "Bash"), advisorResult("c", big),
		advisorCall("adv", toolAdvisor), advisorResult("adv", advisorOwnResult),
	}
	tr := advisorTranscript{call: "adv", items: items}

	got, err := renderText(tr, advisorEntries(tr, nil), 1800, "t")

	require.NoError(t, err)
	assert.Contains(t, got, "--- developer [t] ---\ncontext")
	assert.Contains(t, got, "--- user [t] ---\nthe task\n\n--- note [t] ---\n2 earlier entries of the conversation are left out here")
	assert.NotContains(t, got, "(a) [t]", "the oldest call goes with its result")
	assert.Contains(t, got, "--- tool call Bash (b) [t] ---", "the call beside it stays, with its result")
	assert.Contains(t, got, "--- tool result (b) [t] ---\nsmall")
	assert.Contains(t, got, "--- user [t] ---\nmore")
	assert.Contains(t, got, "(c) [t]")
	assert.Contains(t, got, "--- tool result (adv) [t] ---")

	got, err = renderText(tr, advisorEntries(tr, nil), 400, "t")
	require.NoError(t, err)
	assert.NotContains(t, got, "(c) [t]")
	assert.Contains(t, got, "--- tool call advisor (adv) [t] ---", "the advisor's call stays")
	assert.Contains(t, got, "7 earlier entries")

	running := llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "s", Name: "Bash", Arguments: `{"command":"` + big + `"}`}}
	sibling := advisorTranscript{call: "adv", items: []llm.Item{
		advisorMessage(llm.RoleUser, "the task"), advisorCall("a", "Bash"), advisorResult("a", big),
		running, advisorCall("adv", toolAdvisor), advisorResult("s", advisorPendingResult), advisorResult("adv", advisorOwnResult),
	}}
	got, err = renderText(sibling, advisorEntries(sibling, nil), 1800, "t")
	require.NoError(t, err)
	assert.NotContains(t, got, "(a) [t]")
	assert.Contains(t, got, "--- tool call Bash (s) [t] ---", "a call still running beside the advisor's stays")
	assert.Contains(t, got, "--- tool result (s) [t] ---\n"+advisorPendingResult)
	_, err = renderText(sibling, advisorEntries(sibling, nil), 1000, "t")
	require.ErrorIs(t, err, errTranscriptTooLarge, "it is not dropped to fit")

	doc, err := renderJSON(tr, advisorEntries(tr, nil), 1800)
	require.NoError(t, err)
	var parsed struct {
		Dropped int               `json:"dropped"`
		Items   []json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(doc, &parsed))
	assert.Equal(t, 2, parsed.Dropped)
	assert.Len(t, parsed.Items, len(items)-2)

	huge := advisorTranscript{call: "adv", items: []llm.Item{
		advisorMessage(llm.RoleUser, strings.Repeat("pasted log line\n", 1000)), advisorCall("x", "Bash"), advisorResult("x", "ok"),
		advisorCall("adv", toolAdvisor), advisorResult("adv", advisorOwnResult),
	}}
	_, err = renderText(huge, advisorEntries(huge, nil), 1000, "t")
	require.ErrorIs(t, err, errTranscriptTooLarge, "the first user message alone is over the limit")
	_, err = renderJSON(huge, advisorEntries(huge, nil), 1000)
	require.ErrorIs(t, err, errTranscriptTooLarge)

	full, err := renderText(tr, advisorEntries(tr, nil), 1_000_000, "t")
	require.NoError(t, err)
	snug := approxTokens(len(full)) + int64(len(items)) // each entry's estimate rounds up once
	got, err = renderText(tr, advisorEntries(tr, nil), snug, "t")
	require.NoError(t, err)
	assert.Equal(t, full, got, "a transcript that fits keeps every entry, with no room left for a note")
	fullJSON, err := renderJSON(tr, advisorEntries(tr, nil), 1_000_000)
	require.NoError(t, err)
	gotJSON, err := renderJSON(tr, advisorEntries(tr, nil), approxTokens(len(fullJSON))+int64(len(items)))
	require.NoError(t, err)
	assert.Equal(t, string(fullJSON), string(gotJSON))

	_, err = renderText(advisorTranscript{system: strings.Repeat("rule ", 1000)}, nil, 100, "t")
	require.ErrorIs(t, err, errInstructionsTooLarge)
	_, err = renderJSON(advisorTranscript{system: strings.Repeat("rule ", 1000)}, nil, 100)
	require.ErrorIs(t, err, errInstructionsTooLarge)
}

// TestAdvisorEntries_Placeholder: only the placeholder itself becomes the
// note: not a user message that starts with a marker, nor a placeholder of
// another compaction than the one the request carried.
func TestAdvisorEntries_Placeholder(t *testing.T) {
	rec := compaction.Record{Hash: "abcdef0123456789abcdef"}
	other := compaction.Record{Hash: "0000000000000000ffff"}
	pasted := "[" + compaction.RemoteMarker(rec) + "] what does this marker mean?"
	tr := advisorTranscript{items: []llm.Item{
		advisorMessage(llm.RoleUser, compaction.RemotePlaceholder(rec)),
		advisorMessage(llm.RoleUser, pasted),
		advisorMessage(llm.RoleUser, compaction.RemotePlaceholder(other)),
	}, remote: remoteItem{marker: compaction.RemoteMarker(rec), item: []byte(`{}`)}}

	entries := advisorEntries(tr, nil)

	require.Len(t, entries, 3)
	assert.Equal(t, advisorCompactedNote, entries[0].note)
	assert.Equal(t, advisorMessage(llm.RoleUser, pasted), entries[1].item, "a pasted marker stays the user's text")
	assert.Empty(t, entries[2].note, "another compaction's placeholder is not the one the request carried")
	tr.remote = remoteItem{}
	assert.Equal(t, advisorCompactedNote, advisorEntries(tr, nil)[2].note, "with no item recorded, any whole placeholder is a note")
}

// TestTrimEntries_Images: an image in the JSON transcript counts as about
// 1,844 tokens, not by its data URL's length.
func TestTrimEntries_Images(t *testing.T) {
	image := llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: "v", Output: []llm.ToolResultOutput{{Kind: llm.ToolResultImage, Value: "data:image/png;base64," + strings.Repeat("A", 400_000)}}}}
	tr := advisorTranscript{call: "adv", items: []llm.Item{
		advisorMessage(llm.RoleUser, "look"), advisorCall("v", "ViewImage"), image, advisorCall("adv", toolAdvisor), advisorResult("adv", advisorOwnResult),
	}}
	doc, err := renderJSON(tr, advisorEntries(tr, nil), 3000)
	require.NoError(t, err)
	assert.Contains(t, string(doc), `"dropped":0`)
}

func runSh(t *testing.T, ctx context.Context, script, dir string, stdin []byte) (string, error) {
	t.Helper()

	return runAdvisorCommand(ctx, "/bin/sh", []string{"/bin/sh", "-c", script}, dir, stdin)
}

// TestRunAdvisorCommand: the command runs in the workspace with
// UAH_ADVISOR=off and the transcript on stdin, and its trimmed stdout is
// the advice; a failure or no output is a commandError with the end of
// its stderr; one that does not read a large stdin still advises.
func TestRunAdvisorCommand(t *testing.T) {
	dir := t.TempDir()
	saved := filepath.Join(dir, "stdin.txt")

	advice, err := runSh(t, t.Context(), `cat > stdin.txt; echo "$UAH_ADVISOR $(pwd -P) $PWD"; echo; echo "  1. check it  "`, dir, []byte("the transcript"))
	require.NoError(t, err)
	real, _ := filepath.EvalSymlinks(dir)
	assert.Equal(t, "off "+real+" "+dir+"\n\n  1. check it", advice, "PWD is the workspace, not uah's directory")
	data, err := os.ReadFile(saved)
	require.NoError(t, err)
	assert.Equal(t, "the transcript", string(data))

	_, err = runSh(t, t.Context(), `echo partial; echo "first" >&2; echo "last line" >&2; exit 3`, dir, nil)
	var ce *commandError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "the command /bin/sh failed with exit status 3: last line", err.Error())
	assert.Equal(t, "The end of the command's stderr:\nfirst\nlast line", ce.detail())
	assert.Equal(t, "The advisor is unavailable (the command /bin/sh failed with exit status 3: last line); go on without it.\n\nThe end of the command's stderr:\nfirst\nlast line",
		unavailable(oneLineError(err))+errorDetail(err))

	_, err = runSh(t, t.Context(), `printf '  \n\t\n'`, dir, nil)
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "the command /bin/sh printed no advice", err.Error())
	assert.Empty(t, ce.detail())

	_, err = runSh(t, t.Context(), `head -c 5000 /dev/zero | tr '\0' e >&2; exit 1`, dir, nil)
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "…"+strings.Repeat("e", advisorStderrTail), ce.stderr, "only the end of stderr")

	big := []byte(strings.Repeat("transcript line\n", 200_000)) // 3.2 MB, far past a pipe's buffer
	advice, err = runSh(t, t.Context(), `echo "advice without reading"`, dir, big)
	require.NoError(t, err, "a command that exits 0 without reading stdin is not an error")
	assert.Equal(t, "advice without reading", advice)

	advice, err = runSh(t, t.Context(), `head -c 50000 /dev/zero | tr '\0' a`, dir, nil)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", advisorMaxAdvice)+"\n\n(The advice was cut at 32000 bytes.)", advice)

	_, err = runAdvisorCommand(t.Context(), "", []string{"no-such-advisor-command"}, dir, nil)
	require.ErrorContains(t, err, "the command no-such-advisor-command was not in PATH when the session opened")
	_, err = runAdvisorCommand(t.Context(), "sh", []string{"sh", "-c", "echo advice"}, dir, nil)
	require.Error(t, err, "a name is not looked up again")
	_, err = runAdvisorCommand(t.Context(), filepath.Join(dir, "missing"), []string{filepath.Join(dir, "missing")}, dir, nil)
	require.ErrorContains(t, err, "failed to start the command")
}

// TestAdvisorCommand_NotStarted: a command that did not start names no
// command, so the advisor log leaves it out.
func TestAdvisorCommand_NotStarted(t *testing.T) {
	tr, _ := commandTranscript()
	for _, program := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		c := advisorCommand{argv: []string{"advise"}, program: program, maxTokens: 150000, dir: t.TempDir()}
		a, err := c.advise(t.Context(), tr)
		require.Error(t, err, program)
		assert.Equal(t, advice{}, a, program)
	}
}

// gone reports whether the process with the pid in path has ended, once
// its new parent has reaped it.
func gone(t *testing.T, path string) bool {
	t.Helper()
	var data []byte
	require.Eventually(t, func() bool {
		var err error
		data, err = os.ReadFile(path)
		return err == nil && len(strings.TrimSpace(string(data))) > 0
	}, 5*time.Second, 10*time.Millisecond, "no pid in %s", path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)

	return assert.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 20*time.Millisecond)
}

// TestRunAdvisorCommand_Group: a timeout or cancel returns at once and
// kills the command's group, a grandchild that holds stdout included; a
// command that exits while a process it left holds stdout returns within
// advisorWaitDelay, with its advice when it exited 0.
func TestRunAdvisorCommand_Group(t *testing.T) {
	dir := t.TempDir()
	for name, cancel := range map[string]func(context.Context) (context.Context, context.CancelFunc){
		"timeout": func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 300*time.Millisecond)
		},
		"cancel": func(ctx context.Context) (context.Context, context.CancelFunc) {
			ctx, stop := context.WithCancel(ctx)
			time.AfterFunc(300*time.Millisecond, stop)

			return ctx, stop
		},
	} {
		t.Run(name, func(t *testing.T) {
			pidFile := filepath.Join(dir, name+".pid")
			ctx, stop := cancel(t.Context())
			defer stop()
			start := time.Now()
			_, err := runSh(t, ctx, `sleep 30 & echo $! > `+pidFile+`; sleep 30`, dir, nil)
			assert.Less(t, time.Since(start), 300*time.Millisecond+advisorWaitDelay+time.Second)
			require.Error(t, err)
			assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled), err)
			assert.True(t, gone(t, pidFile), "the grandchild is gone")
		})
	}

	pidFile := filepath.Join(dir, "left.pid")
	start := time.Now()
	advice, err := runSh(t, t.Context(), `echo advice; sleep 30 & echo $! > `+pidFile, dir, nil)
	require.NoError(t, err)
	assert.Equal(t, "advice", advice, "what it wrote before it exited")
	assert.Less(t, time.Since(start), advisorWaitDelay+2*time.Second, "a process it left does not hold the call")
	stopLeft(t, pidFile)

	start = time.Now()
	_, err = runSh(t, t.Context(), `echo fail >&2; sleep 30 & echo $! > `+pidFile+`; exit 3`, dir, nil)
	require.ErrorContains(t, err, "exit status 3")
	assert.Less(t, time.Since(start), advisorWaitDelay+2*time.Second, "nor a failed one's")
	stopLeft(t, pidFile)
}

// stopLeft kills the process whose pid is in path, which a command left.
func stopLeft(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	require.NoError(t, os.Remove(path))
}

// TestAdvisorCommand_Advise: the backend renders the transcript as its
// input asks and names the command for the advisor log, with no usage.
func TestAdvisorCommand_Advise(t *testing.T) {
	dir := t.TempDir()
	tr, searches := commandTranscript()
	for input, want := range map[string]string{engine.AdvisorInputText: "--- user [", engine.AdvisorInputJSON: `{"v":1,`} {
		c := advisorCommand{argv: []string{"/bin/sh", "-c", "cat > in.txt; echo advice"}, program: "/bin/sh", input: input, maxTokens: 150000, dir: dir, searches: searches}
		a, err := c.advise(t.Context(), tr)
		require.NoError(t, err)
		assert.Equal(t, advice{text: "advice", model: "command:/bin/sh"}, a)
		data, err := os.ReadFile(filepath.Join(dir, "in.txt"))
		require.NoError(t, err)
		assert.Contains(t, string(data), want, input)
		assert.Contains(t, string(data), "go json v2", input)
	}

	c := advisorCommand{argv: []string{"/bin/sh", "-c", "echo ran > ran.txt; echo advice"}, program: "/bin/sh", maxTokens: 10, dir: dir}
	a, err := c.advise(t.Context(), tr)
	require.ErrorIs(t, err, errInstructionsTooLarge)
	assert.Equal(t, advice{}, a, "nothing ran, so nothing names the command for the log")
	assert.NoFileExists(t, filepath.Join(dir, "ran.txt"))

	c.maxTokens, c.changeable = 150000, func(path string) bool { return path == "/bin/sh" }
	a, err = c.advise(t.Context(), tr)
	require.ErrorContains(t, err, "the command /bin/sh is now where the agent's commands can write")
	assert.Equal(t, advice{}, a)
	assert.NoFileExists(t, filepath.Join(dir, "ran.txt"), "a program the agent could change since does not run")
}

// TestTailBuffer: the last max bytes, with "…" only when bytes before
// them were dropped.
func TestTailBuffer(t *testing.T) {
	exact := &tailBuffer{max: 4}
	_, _ = exact.Write([]byte("abcd"))
	assert.Equal(t, "abcd", exact.String(), "nothing dropped")
	_, _ = exact.Write([]byte("ef"))
	assert.Equal(t, "…cdef", exact.String())
	big := &tailBuffer{max: 4}
	_, _ = big.Write([]byte("abcdefgh"))
	assert.Equal(t, "…efgh", big.String())
	after := &tailBuffer{max: 4}
	_, _ = after.Write([]byte("x"))
	_, _ = after.Write([]byte("abcd"))
	assert.Equal(t, "…abcd", after.String())
}

// TestAdvisorJobs_CloseStopsTheCommand: the run's end cancels a call in
// flight and waits for its command, whose group is gone and whose log
// line is written when close returns; no call starts after it.
func TestAdvisorJobs_CloseStopsTheCommand(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	e := New(Config{StateDir: t.TempDir(), Provider: "openai", Advisor: Advisor{Command: []string{"/bin/sh", "-c", "sleep 60 & echo $! > child.pid; sleep 60"}, Program: "/bin/sh"}})
	e.last.recorder("s")(llm.Request{Input: []llm.Item{advisorMessage(llm.RoleSystem, "sys"), advisorMessage(llm.RoleUser, "go")}},
		llm.Response{Output: []llm.Item{advisorCall("adv", toolAdvisor)}}, "high", remoteItem{})
	sw, err := newSwitcher("m", variant{}, 1, func(variant) (Client, error) { return &scriptedClient{}, nil })
	require.NoError(t, err)
	jobs := &advisorJobs{ctx: t.Context(), e: e, sw: sw, provider: "openai", sessionID: "s", workspace: dir, logPath: filepath.Join(dir, "log"), cancels: map[operation.ID]context.CancelFunc{}}
	ctx, cancel := context.WithCancel(t.Context())
	jobs.cancels["op"] = cancel
	done := make(chan string, 1)
	go func() { text, _ := jobs.advise(ctx, "adv"); done <- text }()
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 10*time.Second, 20*time.Millisecond)

	require.NoError(t, jobs.close())

	data, err := os.ReadFile(filepath.Join(dir, "log"))
	require.NoError(t, err, "the call is logged before close returns")
	assert.Contains(t, string(data), `"model":"command:/bin/sh"`)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the call did not return")
	}
	assert.True(t, gone(t, pidFile))
	text, why := jobs.advise(t.Context(), "adv")
	assert.Equal(t, unavailable("the run ended"), text)
	assert.Equal(t, "the run ended", why)
}

// TestAdvisorJobs_Changeable: an advisor program becomes one the session's
// commands could change once a grant covers its directory, so the call
// refuses it; an engine without a sandbox does not check.
func TestAdvisorJobs_Changeable(t *testing.T) {
	ws, granted := t.TempDir(), t.TempDir()
	if resolved := sandbox.ResolvePath(granted); strings.HasPrefix(resolved, "/tmp/") || strings.HasPrefix(resolved, "/private/tmp/") {
		t.Skip("the temporary directory is under /tmp, which the sandbox lets commands write")
	}
	t.Setenv("TMPDIR", t.TempDir()) // the sandbox's $TMPDIR, apart from granted
	policy := sandbox.Policy{}
	grants := sandbox.NewGrants(ws, nil)
	w := &wiring{e: &Engine{cfg: Config{Sandbox: &policy}}, l: harness.Launch{SessionsDir: t.TempDir()}, grants: grants}
	req := core.Request{Workspace: ws, SessionID: "s"}
	j := w.advisorJobs(t.Context(), nil, req, "s")
	program := filepath.Join(sandbox.ResolvePath(granted), "advise") // as app pins it

	assert.True(t, j.changeable(filepath.Join(ws, "advise")))
	assert.False(t, j.changeable(program), "outside the workspace and the session's temporary directory")
	link := filepath.Join(granted, "env")
	require.NoError(t, os.Symlink(sandbox.ResolvePath("/usr/bin/env"), link))
	assert.True(t, j.changeable(link), "a pinned program that leads elsewhere now")
	require.True(t, grants.Add(sandbox.ResolvePath(granted), sandbox.GrantApproved))
	assert.True(t, j.changeable(program), "the grant covers it now")

	bare := &wiring{e: &Engine{}, l: harness.Launch{SessionsDir: t.TempDir()}}
	assert.Nil(t, bare.advisorJobs(t.Context(), nil, req, "s").changeable)
}
