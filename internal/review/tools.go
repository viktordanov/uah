package review

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"
)

// CommandTimeout bounds one of the reviewer's commands, Codex's default
// exec_command yield time. The reviewer cannot wait for a command that runs
// longer, so it is stopped.
const CommandTimeout = 10 * time.Second

// maxRounds caps the model calls of one review attempt, so a reviewer that
// keeps running commands fails closed before its deadline does.
const maxRounds = 12

// Runner runs one of the reviewer's commands read-only: in the read-only
// sandbox without network, with a temporary directory of the reviewer's
// own. workdir is the model's: absolute, relative to the workspace, or ""
// for the workspace. It returns the combined output and the exit code; err
// means the command did not run or did not finish within ctx.
type Runner func(ctx context.Context, command, workdir string) (output string, exit int, err error)

// execSchema is the parameters of execTool: Codex's cmd and workdir.
const execSchema = `{"type":"object","properties":{` +
	`"cmd":{"type":"string","description":"Shell command to execute."},` +
	`"workdir":{"type":"string","description":"Optional working directory to run the command in; defaults to the workspace."}` +
	`},"required":["cmd"],"additionalProperties":false}`

// execTool is the reviewer's one tool, after Codex's exec_command, which
// its reviewer gets in a read-only sandbox.
var execTool = llm.Tool{
	Type: llm.ToolFunction,
	Name: "exec_command",
	Description: "Runs a read-only shell command (/bin/sh) in the coding agent's environment and returns its output. " +
		"The command cannot write outside its temporary directory and has no network access. " +
		"It is stopped after 10 seconds.",
	Parameters: schema(execSchema),
}

// schema decodes a JSON schema written in this package.
func schema(text string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		panic("review: bad tool schema: " + err.Error())
	}

	return out
}

// runCall runs one of the reviewer's tool calls and returns its result.
func runCall(ctx context.Context, run Runner, call llm.ToolCall, l Limits) llm.Item {
	result := func(text string) llm.Item {
		return llm.Item{Type: llm.ItemToolResult, Data: llm.ToolResult{
			CallID: call.CallID, Output: []llm.ToolResultOutput{{Kind: llm.ToolResultText, Value: text}},
		}}
	}
	if call.Name != execTool.Name {
		return result("unsupported call: " + call.Name)
	}
	var args struct {
		Cmd     string `json:"cmd"`
		Workdir string `json:"workdir"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil || strings.TrimSpace(args.Cmd) == "" {
		return result("failed to parse function arguments: want {\"cmd\": \"...\"}")
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()
	out, exit, err := run(ctx, args.Cmd, args.Workdir)
	wall := time.Since(start).Seconds()
	if err != nil {
		return result(fmt.Sprintf("Wall time: %.1f seconds\nThe command failed: %v\nOutput:\n%s", wall, err, truncate(out, l.OutputBytes)))
	}

	return result(fmt.Sprintf("Process exited with code %d\nWall time: %.1f seconds\nOutput:\n%s", exit, wall, truncate(out, l.OutputBytes)))
}
