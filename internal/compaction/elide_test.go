package compaction_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/compaction"
)

// calls is n Bash calls, each with an output of the given tokens.
func calls(n int, tokens func(i int) int) []llm.Item {
	var parts [][]llm.Item
	for i := range n {
		parts = append(parts, bash(fmt.Sprintf("c%d", i), fmt.Sprintf("cmd %d", i), strings.Repeat("x", 4*tokens(i))))
	}

	return history(parts...)
}

func TestElidable(t *testing.T) {
	rules := compaction.Elision{AfterCalls: 3, BigTokens: 1_000, BigAfterCalls: 1}
	view := append([]llm.Item{msg(llm.RoleSystem, "sys")}, calls(6, func(i int) int {
		switch i {
		case 1:
			return 50 // too small to be worth a stub
		case 4:
			return 5_000 // big, one call after it
		}

		return 200
	})...)
	assert.Equal(t, []string{"c0", "c2", "c4"}, rules.Elidable(view, nil), "three calls after, or big with one after")
	assert.Equal(t, []string{"c2", "c4"}, rules.Elidable(view, []string{"c0"}), "already elided")
	assert.Empty(t, compaction.Elision{}.Elidable(view, nil), "off")

	skill := history(
		[]llm.Item{toolCall("s", "SkillUse", map[string]any{"name": "x"}), result("s", strings.Repeat("s", 40_000))},
		calls(5, func(int) int { return 200 }),
	)
	assert.NotContains(t, rules.Elidable(skill, nil), "s", "a skill's body stays")
}

func TestElide_StubsAndStaysPut(t *testing.T) {
	items := history(bash("c1", "go test ./...", strings.Repeat("y", 8_000)+"\nExit code: 1"), bash("c2", "ls", "a"))
	out := compaction.Elide(items, []string{"c1"})
	require.Len(t, out, 4)
	stub := out[1].Data.(llm.ToolResult)
	assert.Equal(t, "[uah elided this output to save context: Bash `go test ./...`, exit 1, 2,004 tokens. Run it again if you need it.]", compaction.ResultText(stub))
	assert.Equal(t, items[3], out[3])
	assert.Equal(t, strings.Repeat("y", 8_000)+"\nExit code: 1", compaction.ResultText(items[1].Data.(llm.ToolResult)), "the input is not changed")

	input := append([]llm.Item{msg(llm.RoleSystem, "sys")}, items...)
	rec := compaction.Record{}.WithElided([]string{"c1"})
	a, err := compaction.Apply(input, rec)
	require.NoError(t, err)
	b, err := compaction.Apply(append(input, msg(llm.RoleUser, "more")), rec)
	require.NoError(t, err)
	assert.Equal(t, a, b[:len(a)], "a later request starts with the same stubs")
}

func TestStillElided(t *testing.T) {
	items := history(bash("c1", "a", "x"), bash("c2", "b", "y"))
	assert.Equal(t, []string{"c2"}, compaction.StillElided(items[2:], []string{"c1", "c2", "c9"}))
}

func TestElideToFit_StubsTheNewestOutputsFirst(t *testing.T) {
	items := append([]llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, "go")}, calls(5, func(int) int { return 1_000 })...)
	total := compaction.EstimateTokens(items)
	before := slices.Clone(items)

	same, ids, ok := compaction.ElideToFit(items, total)
	assert.True(t, ok)
	assert.Empty(t, ids)
	assert.Equal(t, items, same, "nothing is stubbed when it fits")

	out, ids, ok := compaction.ElideToFit(items, total-1_500)
	assert.True(t, ok)
	assert.Equal(t, []string{"c4", "c3"}, ids, "the newest first, and no more than it takes")
	assert.LessOrEqual(t, compaction.EstimateTokens(out), total-1_500)
	require.Len(t, out, len(items))
	for i := range items {
		assert.Equal(t, items[i].Type, out[i].Type, "the order and kinds of items stay")
		if r, isResult := items[i].Data.(llm.ToolResult); isResult {
			assert.Equal(t, r.CallID, out[i].Data.(llm.ToolResult).CallID, "each output keeps its call")
		}
	}
	assert.Equal(t, items[:8], out[:8], "the older outputs stay whole")
	assert.True(t, strings.HasPrefix(compaction.ResultText(out[11].Data.(llm.ToolResult)), "[uah elided this output"))
	assert.Equal(t, before, items, "the input is not changed")

	again, ids, _ := compaction.ElideToFit(out, total-2_500)
	assert.Equal(t, []string{"c2"}, ids, "a stub is not stubbed again")
	assert.Equal(t, out[11], again[11])
}

func TestElideToFit_ReportsWhenStubsAreNotEnough(t *testing.T) {
	items := append([]llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, strings.Repeat("u", 20_000))}, calls(2, func(int) int { return 1_000 })...)
	out, ids, ok := compaction.ElideToFit(items, 1_000)
	assert.False(t, ok)
	assert.Equal(t, []string{"c1", "c0"}, ids, "every output is a stub")
	assert.Greater(t, compaction.EstimateTokens(out), int64(1_000))

	messages := []llm.Item{msg(llm.RoleSystem, "sys"), msg(llm.RoleUser, strings.Repeat("u", 20_000))}
	out, ids, ok = compaction.ElideToFit(messages, 1_000)
	assert.False(t, ok)
	assert.Empty(t, ids)
	assert.Equal(t, messages, out)
}
