package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/toolbridge"
)

// toolUsage is `uah tool`'s help, which the model reads too.
const toolUsage = `usage: uah tool list | describe <name> | call <name> [json | -]

Inside a command of a uah session, calls the session's tools (its MCP
servers' tools and resources), so a script can call them and filter their
results before you read them:

  uah tool list                      the tools you may call, one per line
  uah tool describe <name>           a tool's description and input schema, as JSON
  uah tool call <name> '{"q":"x"}'   call it; its result goes to stdout
  uah tool call <name> -             the arguments from stdin

A call runs as your own call of the tool would: the same policy, hooks, and
approval. It exits 1 with the reason on stderr when the call fails or is
refused.`

// toolCommand is `uah tool` in the command list and the help; main runs it
// before anything else (runTool), so it needs no configuration or state.
func toolCommand() *cli.Command {
	return &cli.Command{
		Name:            "tool",
		Usage:           "inside a session's command: list, describe, or call the session's MCP tools",
		Description:     toolUsage,
		SkipFlagParsing: true,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if code := runTool(ctx, cmd.Args().Slice(), os.Stdin, os.Stdout, os.Stderr, os.Getenv); code != 0 {
				return cli.Exit("", code)
			}

			return nil
		},
	}
}

// runTool runs `uah tool` with its arguments and returns the exit code.
func runTool(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stdout, toolUsage)

		return exitOK
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "uah tool: %v\n", err)

		return exitFailed
	}
	req, err := toolRequest(args, stdin)
	if err != nil {
		return fail(err)
	}
	req.Token = getenv(toolbridge.TokenVar)
	resp, err := toolbridge.Do(ctx, getenv("TMPDIR"), req)
	if err != nil {
		return fail(err)
	}
	if !resp.OK {
		return fail(errors.New(resp.Error))
	}
	if err := printToolResponse(req.Op, resp, stdout, stderr); err != nil {
		return fail(err)
	}

	return exitOK
}

// toolRequest reads `uah tool`'s arguments as a request.
func toolRequest(args []string, stdin io.Reader) (toolbridge.Request, error) {
	switch args[0] {
	case toolbridge.OpList:
		return toolbridge.Request{Op: toolbridge.OpList}, nil
	case toolbridge.OpDescribe:
		if len(args) != 2 {
			return toolbridge.Request{}, errors.New("describe takes one tool name")
		}

		return toolbridge.Request{Op: toolbridge.OpDescribe, Name: args[1]}, nil
	case toolbridge.OpCall:
	default:
		return toolbridge.Request{}, fmt.Errorf("unknown command %q; run uah tool --help", args[0])
	}
	if len(args) < 2 || len(args) > 3 {
		return toolbridge.Request{}, errors.New("call takes a tool name and its arguments as a JSON object (or - for stdin)")
	}
	req := toolbridge.Request{Op: toolbridge.OpCall, Name: args[1], Arguments: json.RawMessage("{}")}
	if len(args) == 3 {
		raw := []byte(args[2])
		if args[2] == "-" {
			data, err := io.ReadAll(stdin)
			if err != nil {
				return toolbridge.Request{}, fmt.Errorf("failed to read the arguments: %w", err)
			}
			raw = data
		}
		if !json.Valid(raw) {
			return toolbridge.Request{}, errors.New("the arguments are not valid JSON")
		}
		req.Arguments = raw
	}

	return req, nil
}

// printToolResponse prints an answer: the tools, a tool's description as
// JSON, or a call's result as the tool returned it.
func printToolResponse(op string, resp toolbridge.Response, stdout, stderr io.Writer) error {
	switch op {
	case toolbridge.OpList:
		for _, t := range resp.Tools {
			fmt.Fprintf(stdout, "%s\t%s\n", t.Name, firstLine(t.Description, 120))
		}
	case toolbridge.OpDescribe:
		data, err := json.MarshalIndent(resp.Tool, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(data))
	default:
		fmt.Fprint(stdout, resp.Text)
		if resp.Text != "" && !strings.HasSuffix(resp.Text, "\n") {
			fmt.Fprintln(stdout)
		}
		if resp.Images > 0 {
			fmt.Fprintf(stderr, "uah tool: the result also had %d image(s), which a command cannot show\n", resp.Images)
		}
	}

	return nil
}

// firstLine is a description's first line, cut to n characters.
func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}

	return s
}
