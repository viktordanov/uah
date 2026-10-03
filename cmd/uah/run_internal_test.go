package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/session"
)

// TestReadLines pins that a line of any length is a message, that line
// endings go, and that a read error is reported after the lines before it,
// not taken for the end of stdin.
func TestReadLines(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 3<<20)
	var got []stdinLine
	for l := range readLines(strings.NewReader("first\r\n" + long + "\nlast")) {
		got = append(got, l)
	}
	require.Len(t, got, 3)
	assert.Equal(t, "first", got[0].text)
	assert.Len(t, got[1].text, len(long), "a line over the old 1 MiB limit is whole")
	assert.Equal(t, stdinLine{text: "last"}, got[2], "a last line without a newline")

	broken := errors.New("broken pipe")
	got = nil
	for l := range readLines(io.MultiReader(strings.NewReader("one\n"), iotest.ErrReader(broken))) {
		got = append(got, l)
	}
	assert.Equal(t, []stdinLine{{text: "one"}, {err: broken}}, got)
}

// TestRunOutputFailures pins that work which fails after a run answered
// fails the command, and that the earlier answer is not the last message.
func TestRunOutputFailures(t *testing.T) {
	t.Parallel()
	ok := core.RunFinished{Result: core.Result{Status: core.StatusOK, Answer: "old answer"}}
	for name, failure := range map[string][]core.Event{
		"a run does not start": {
			session.InputFailed{IDs: []string{"2"}, Reason: "failed to open the session"},
			session.Notice{Level: session.LevelError, Message: "failed to open the session"},
		},
		"a run ends in an error":      {session.Notice{Level: session.LevelError, Message: "the runner failed"}},
		"a hook blocks a message":     {session.InputFailed{IDs: []string{"2"}, Reason: "blocked by a UserPromptSubmit hook: no"}},
		"stdin cannot be read (fail)": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			o := runOutput{stdout: &stdout}
			o.handle(ok)
			for _, e := range failure {
				o.handle(e)
			}
			if failure == nil {
				o.fail("failed to read stdin: broken pipe")
			}
			o.handle(session.Idle{At: time.Now()})

			assert.Nil(t, o.last, "no answer for -o")
			err := o.exit(false)
			var exit cli.ExitCoder
			require.ErrorAs(t, err, &exit)
			assert.Equal(t, exitFailed, exit.ExitCode())
			assert.NotEmpty(t, err.Error(), "--quiet still says why")
			assert.Equal(t, "old answer\n", stdout.String())
		})
	}

	t.Run("a later answer is the last message, and the failure stays", func(t *testing.T) {
		t.Parallel()
		o := runOutput{stdout: io.Discard, progress: newPrinter(io.Discard, false)}
		o.handle(ok)
		o.handle(session.InputFailed{IDs: []string{"2"}, Reason: "failed"})
		o.handle(core.RunFinished{Result: core.Result{Status: core.StatusOK, Answer: "new answer"}})
		require.NotNil(t, o.last)
		assert.Equal(t, "new answer", o.last.Answer)
		var exit cli.ExitCoder
		require.ErrorAs(t, o.exit(false), &exit)
		assert.Equal(t, exitFailed, exit.ExitCode())
		assert.Empty(t, exit.Error(), "the progress lines already said why")
	})

	t.Run("an interrupt still wins", func(t *testing.T) {
		t.Parallel()
		o := runOutput{stdout: io.Discard}
		o.fail("failed")
		var exit cli.ExitCoder
		require.ErrorAs(t, o.exit(true), &exit)
		assert.Equal(t, exitInterrupt, exit.ExitCode())
	})
}

// TestRunOutputJSONFailure pins that a failure of the command itself is a
// JSON event with --json.
func TestRunOutputJSONFailure(t *testing.T) {
	t.Parallel()
	var stdout bytes.Buffer
	o := runOutput{stdout: &stdout, jsonl: newJSONLWriter(&stdout)}
	o.fail("failed to read stdin: broken pipe")
	assert.Contains(t, stdout.String(), "failed to read stdin: broken pipe")
	assert.Contains(t, stdout.String(), `"error"`)
}
