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
	assert.Regexp(t, `^uah stats · last 7 days, `, lines[0])
	assert.Regexp(t, `^  \S+ tokens · 2 runs · 2 sessions · \d+% from the cache$`, lines[2])
	assert.Regexp(t, `(?m)^DAY\s+RUNS\s+SESSIONS\s+TOTAL\s+CACHE$`, res.stdout)
	assert.Regexp(t, `(?m)^\w{3} \w{3} \d+\s+2\s+2\s+\S+\s+\d+%\s+█+\s+today$`, res.stdout)
	assert.Regexp(t, `(?m)^  all time\s+\S+ since \w{3} \d+ · 2 runs · 2 sessions$`, res.stdout)
	assert.Contains(t, res.stdout, "1 day · 1 of 1 days active")
	assert.Regexp(t, `(?m)^  workspaces\s+(temporary|workspace) 100%$`, res.stdout)
	assert.NotContains(t, res.stdout, "\x1b[", "no color on a pipe")
	assert.NotRegexp(t, `(?m) $`, res.stdout, "no line ends in spaces")

	res = uahWith(t, env, "", "stats", "--wide", "--heatmap")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Regexp(t, `(?m)^Mon( [·░▒▓█])+$`, res.stdout)
	assert.Regexp(t, `(?m)^\s+less · ░ ▒ ▓ █ more$`, res.stdout)
	assert.Contains(t, res.stdout, "total is input not served from the cache plus output")

	res = uahWith(t, env, "", "stats", "--by", "source", "-C", e.Workspace)
	require.Equal(t, 0, res.code, res.stderr)
	assert.Regexp(t, `(?m)^exec\s+2\s+2\s`, res.stdout)
	res = uahWith(t, env, "", "stats", "-C", t.TempDir(), "--by", "model")
	require.Equal(t, 0, res.code, res.stderr)
	assert.Contains(t, res.stdout, "\n  no tokens in this range\n", "another workspace has none")

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
