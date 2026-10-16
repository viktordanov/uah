package main_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/viktordanov/uah/testing/fakellm"
)

// TestStatsCommand adds up two uah exec sessions: the last 7 days by day
// with all time under them, a grouping, one workspace, and JSON.
func TestStatsCommand(t *testing.T) {
	t.Parallel()
	e, env := fakeEnv(t, fakellm.Reply{Text: "one"}, fakellm.Reply{Text: "two"})
	for _, prompt := range []string{"first", "second"} {
		res := uahWith(t, env, "", "exec", "-q", "-C", e.Workspace, prompt)
		require.Equal(t, 0, res.code, res.stderr)
	}
	today := time.Now().Format(time.DateOnly)

	res := uahWith(t, env, "", "stats")
	require.Equal(t, 0, res.code, res.stderr)
	lines := strings.Split(res.stdout, "\n")
	assert.Regexp(t, `^DAY\s+RUNS\s+SESSIONS\s+INPUT\s+CACHED\s+OUTPUT\s+REASONING\s+TOTAL\s+CACHE HIT$`, lines[0])
	assert.Len(t, lines, 1+7+2+1+4+1, "7 days, their sum and all time, a blank line, 4 statistics")
	assert.Regexp(t, `(?m)^`+today+`\s+2\s+2\s`, res.stdout)
	assert.Regexp(t, `(?m)^all time\s+2\s+2\s`, res.stdout)
	assert.Contains(t, res.stdout, "1 of 1 days active, a streak of 1 day")
	assert.NotContains(t, res.stdout, "\x1b[", "no color on a pipe")

	res = uahWith(t, env, "", "stats", "--by", "source", "-C", e.Workspace)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Regexp(t, `(?m)^exec\s+2\s+2\s`, res.stdout)
	res = uahWith(t, env, "", "stats", "-C", t.TempDir(), "--by", "model")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Regexp(t, `(?m)^`+today+`\s+0\s+0\s`, res.stdout, "another workspace has none")

	res = uahWith(t, env, "", "stats", "--json", "--since", today, "--until", today)
	require.Equal(t, 0, res.code, res.stderr)
	var report struct {
		By    string `json:"by"`
		Rows  []struct{ Key string } `json:"rows"`
		Total struct {
			Runs  int   `json:"runs"`
			Input int64 `json:"input"`
			Total int64 `json:"total"`
		} `json:"total"`
		Days struct {
			Active int `json:"active"`
		} `json:"days"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.stdout), &report), res.stdout)
	assert.Equal(t, "day", report.By)
	require.Len(t, report.Rows, 1)
	assert.Equal(t, today, report.Rows[0].Key)
	assert.Equal(t, 2, report.Total.Runs)
	assert.Positive(t, report.Total.Input)
	assert.Equal(t, 1, report.Days.Active)

	res = uahWith(t, env, "", "stats", "--by", "year")
	assert.Equal(t, 2, res.code)
	assert.Contains(t, res.stderr, `invalid --by "year"`)
}
