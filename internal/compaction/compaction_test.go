package compaction_test

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/compaction"
)

func msg(role llm.Role, text string) llm.Item {
	return llm.Item{Type: llm.ItemMessage, Data: llm.Message{Role: role, Text: text}}
}

func call(id string) llm.Item {
	return llm.Item{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: id, Name: "Bash", Arguments: `{}`}}
}

func result(id, text string) llm.Item {
	return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{CallID: id, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}}}}
}

func texts(items []llm.Item) []string {
	var out []string
	for _, it := range items {
		switch d := it.Data.(type) {
		case llm.Message:
			out = append(out, string(d.Role)+": "+d.Text)
		case llm.ToolCall:
			out = append(out, "call "+d.CallID)
		case llm.ToolResult:
			out = append(out, "result "+d.CallID)
		case llm.ConfigurationUpdate:
			out = append(out, "effort "+string(d.ReasoningEffort))
		}
	}

	return out
}

func TestApply_KeepsUserMessagesAndReplacesTheRest(t *testing.T) {
	history := []llm.Item{
		msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "first"), call("a"), msg(llm.RoleAssistant, "working"),
		result("a", "out a"), msg(llm.RoleUser, "second"), call("b"),
	}
	rec, err := compaction.NewRecord(history, "the summary", compaction.TriggerManual, "m", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 6, rec.Covered)

	later := append(history, result("b", "out b"), call("c"), result("c", "out c"), msg(llm.RoleUser, "third"))
	got, err := compaction.Apply(later, rec)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"system: sys", "user: first", "user: second", "user: " + compaction.SummaryPrefix + "\nthe summary",
		"user: Output of the earlier tool call b, which the summary covers:\nout b",
		"call c", "result c", "user: third",
	}, texts(got))
}

func TestApply_KeepsDeveloperMessages(t *testing.T) {
	history := []llm.Item{
		msg(llm.RoleSystem, "sys"), msg(llm.RoleDeveloper, "prepared"), msg(llm.RoleUser, "first"),
		call("a"), result("a", "out a"), msg(llm.RoleAssistant, "done"),
	}
	rec, err := compaction.NewRecord(history, "the summary", compaction.TriggerManual, "m", time.Now())
	require.NoError(t, err)
	rec.Floor = 2 // past the developer message: it is kept anyway

	got, err := compaction.Apply(history, rec)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"system: sys", "developer: prepared", "user: " + compaction.SummaryPrefix + "\nthe summary",
	}, texts(got))
}

func effortUpdate(effort llm.ReasoningEffort) llm.Item {
	return llm.Item{Type: llm.ItemConfigurationUpdate, Data: llm.ConfigurationUpdate{ReasoningEffort: effort}}
}

func TestApply_DropsTheCoveredEffortUpdates(t *testing.T) {
	history := []llm.Item{
		msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "first"), call("a"), effortUpdate(llm.ReasoningEffortLow),
		result("a", "out a"), msg(llm.RoleAssistant, "done"), effortUpdate(llm.ReasoningEffortHigh), msg(llm.RoleUser, "second"),
		call("b"), effortUpdate(llm.ReasoningEffortMedium), result("b", "out b"), msg(llm.RoleAssistant, "done"),
		effortUpdate(llm.ReasoningEffortLow),
	}
	rec, err := compaction.NewRecordCovering(history, 10, "the summary", compaction.TriggerManual, "m", time.Now())
	require.NoError(t, err)

	got, err := compaction.Apply(history, rec)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"system: sys", "user: first", "user: second", "user: " + compaction.SummaryPrefix + "\nthe summary",
		"assistant: done", "effort low",
	}, texts(got), "only the updates after the covered items")
	assert.Equal(t, llm.ReasoningEffortMedium, compaction.Configured(history[1:11]), "the effort the covered items last set")
	assert.Empty(t, compaction.Configured(history[1:3]))
}

func TestNewRecord_LeavesNewUserMessagesAfterTheSummary(t *testing.T) {
	history := []llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "first"), call("a"), result("a", "x"), msg(llm.RoleUser, "new")}
	rec, err := compaction.NewRecord(history, "s", compaction.TriggerManual, "m", time.Now())
	require.NoError(t, err)
	assert.Equal(t, 3, rec.Covered)
	got, err := compaction.Apply(history, rec)
	require.NoError(t, err)
	assert.Equal(t, []string{"system: sys", "user: first", "user: " + compaction.SummaryPrefix + "\ns", "user: new"}, texts(got))
	assert.Equal(t, 0, compaction.Coverable([]llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "only")}))
}

func TestApply_RejectsAnotherHistory(t *testing.T) {
	history := []llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "first"), call("a")}
	rec, err := compaction.NewRecord(history, "s", compaction.TriggerAuto, "m", time.Now())
	require.NoError(t, err)

	_, err = compaction.Apply([]llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "other"), call("a")}, rec)
	require.ErrorIs(t, err, compaction.ErrMismatch)
	_, err = compaction.Apply(history[:2], rec)
	require.ErrorIs(t, err, compaction.ErrMismatch)
	// The system message may change (instructions, skills) without breaking it.
	_, err = compaction.Apply(append([]llm.Item{msg(llm.RoleSystem, "new")}, history[1:]...), rec)
	require.NoError(t, err)
}

func TestSummaryRequest(t *testing.T) {
	system, input := compaction.SummaryRequest([]llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "hi")}, "")
	assert.Equal(t, "sys", system)
	assert.Equal(t, []string{"user: hi", "user: " + compaction.Prompt}, texts(input))
}

func TestWindowAndMeter(t *testing.T) {
	catalog := func(model string) (int64, bool) { return map[string]int64{"red": 372_000}[model], model == "red" }
	assert.Equal(t, int64(272_000), compaction.ContextWindow("unknown", 0, catalog))
	assert.Equal(t, int64(372_000), compaction.ContextWindow("red", 0, catalog), "the catalog's window")
	assert.Equal(t, int64(272_000), compaction.ContextWindow("red", 0, nil), "no catalog: the default")
	assert.Equal(t, int64(1000), compaction.ContextWindow("red", 1000, catalog), "model_context_window wins")
	assert.Equal(t, int64(244_800), compaction.AutoLimit(272_000, 90))
	assert.Equal(t, int64(0), compaction.AutoLimit(272_000, 0))
	assert.Equal(t, 100, compaction.PercentLeft(5_000, 272_000))
	assert.Equal(t, 50, compaction.PercentLeft(142_000, 272_000))
	assert.Equal(t, 0, compaction.PercentLeft(400_000, 272_000))
}

func TestCoverableKeeping(t *testing.T) {
	reasoning := llm.Item{Type: llm.ItemReasoning, Data: llm.Reasoning{Summary: []string{"r"}}}
	input := []llm.Item{
		msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "go"),
		reasoning, call("a"), result("a", "1"),
		reasoning, call("b"), call("c"), result("b", "2"), result("c", "3"),
		msg(llm.RoleAssistant, "done"), msg(llm.RoleUser, "new"),
	}
	assert.Equal(t, 10, compaction.CoverableKeeping(input, 0))
	assert.Equal(t, 4, compaction.CoverableKeeping(input, 1), "c's response also made b: both stay, with its reasoning")
	assert.Equal(t, 4, compaction.CoverableKeeping(input, 2))
	assert.Equal(t, 1, compaction.CoverableKeeping(input, 3), "the user's message is the last covered item")
	assert.Equal(t, 0, compaction.CoverableKeeping(input, 4), "fewer calls than asked: nothing to cover")
}

// TestApply_ToolOutputsRenderedAgain: a resumed run renders each tool output
// again from its operation, with its own version and configuration, so an
// output can change, as when a new sandbox policy drops a hint. A record
// still applies by its shape; one from before Shape checks its hash, and
// any change to the items themselves is a mismatch.
func TestApply_ToolOutputsRenderedAgain(t *testing.T) {
	history := []llm.Item{
		msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "first"), call("a"),
		result("a", "denied\nuah: the workspace-write sandbox likely blocked this."), msg(llm.RoleAssistant, "done"),
	}
	rec, err := compaction.NewRecord(history, "the summary", compaction.TriggerManual, "m", time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, rec.Shape)
	rendered := slices.Clone(history)
	rendered[3] = result("a", "denied")

	got, err := compaction.Apply(rendered, rec)
	require.NoError(t, err)
	assert.Equal(t, []string{"system: sys", "user: first", "user: " + compaction.SummaryPrefix + "\nthe summary"}, texts(got))

	legacy := rec
	legacy.Shape = ""
	_, err = compaction.Apply(history, legacy)
	require.NoError(t, err, "a record from before Shape applies by its hash")
	_, err = compaction.Apply(rendered, legacy)
	require.ErrorIs(t, err, compaction.ErrMismatch)

	edited := slices.Clone(history)
	edited[1] = msg(llm.RoleUser, "edited")
	for _, r := range []compaction.Record{rec, legacy} {
		_, err = compaction.Apply(edited, r)
		require.ErrorIs(t, err, compaction.ErrMismatch, "a changed message")
		_, err = compaction.Apply(slices.Delete(slices.Clone(history), 2, 4), r)
		require.ErrorIs(t, err, compaction.ErrMismatch, "a call left out")
	}
}
