package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/citations"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/goal"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/patch"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
	"github.com/viktordanov/uah/internal/usage/cachestats"
)

// sessionsCommand is `uah sessions`: list sessions, or show one.
func sessionsCommand() *cli.Command {
	stateDir := &cli.StringFlag{
		Name: "state-dir", Usage: "sessions, logs, and run records",
		Value: defaultStateDir(), Sources: cli.EnvVars(home.EnvStateDir), TakesFile: true,
	}
	jsonFlag := &cli.BoolFlag{Name: flagJSON, Usage: "print JSON"}
	all := &cli.BoolFlag{Name: flagAll, Usage: "list sessions from every directory"}
	search := &cli.StringFlag{Name: "search", Usage: "only sessions whose prompts or answers contain these words (any directory)"}
	workspace := &cli.StringFlag{Name: flagWorkspace, Aliases: []string{"C"}, Usage: "list this directory's sessions (also with --all or --search)", DefaultText: "the current directory", TakesFile: true}
	since := &cli.StringFlag{Name: flagSince, Usage: "only sessions active after this RFC 3339 time, such as 2026-09-29T08:00:00Z", Validator: func(v string) error {
		_, err := parseSince(v)

		return err
	}}

	return &cli.Command{
		Name:         "sessions",
		Usage:        "list this directory's sessions, most recent first (--all for every directory)",
		Flags:        []cli.Flag{stateDir, jsonFlag, all, workspace, search, since},
		OnUsageError: onUsageError,
		Action:       listSessions,
		Commands: []*cli.Command{sessionsRmCommand(stateDir), {
			Name:         subShow,
			Usage:        "print a session's transcript",
			ArgsUsage:    "<id or unique prefix>",
			Flags:        []cli.Flag{stateDir, jsonFlag},
			OnUsageError: onUsageError,
			Action:       showSession,
		}},
	}
}

func listSessions(ctx context.Context, cmd *cli.Command) error {
	stateDir, err := filepath.Abs(cmd.String("state-dir"))
	if err != nil {
		return fmt.Errorf("failed to resolve state dir: %w", err)
	}
	infos, err := store.List(ctx, stateDir)
	if err == nil {
		infos, err = session.WithUnused(stateDir, infos)
	}
	if q := cmd.String("search"); q != "" {
		infos, err = store.SearchIn(ctx, stateDir, q)
	}
	if err != nil {
		return err
	}
	cwd, err := currentDir(cmd)
	if err != nil {
		return err
	}
	showAll := (cmd.Bool(flagAll) || cmd.String("search") != "") && !cmd.IsSet(flagWorkspace)
	if !showAll {
		infos = session.InDir(infos, cwd)
	}
	if v := cmd.String(flagSince); v != "" {
		t, _ := parseSince(v) // the flag's validator checked it
		infos = session.ActiveSince(infos, t)
	}
	if cmd.Bool(flagJSON) {
		if infos == nil {
			infos = []session.Info{} // [] rather than null
		}

		return writeJSON(os.Stdout, infos)
	}
	if len(infos) == 0 {
		if showAll {
			fmt.Fprintln(os.Stderr, "no sessions in "+stateDir)
		} else {
			fmt.Fprintln(os.Stderr, "no sessions in "+cwd+" (--all lists every directory)")
		}

		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if showAll {
		fmt.Fprintln(tw, "SESSION\tACTIVE\tRUNS\tSTATUS\tMODEL\tFROM\tDIRECTORY\tFIRST PROMPT")
	} else {
		fmt.Fprintln(tw, "SESSION\tACTIVE\tRUNS\tSTATUS\tMODEL\tFROM\tFIRST PROMPT")
	}
	// Subagents follow their parent, indented.
	for _, in := range session.Tree(infos) {
		dir := ""
		if showAll {
			dir = homeShort(in.Workspace) + "\t"
		}
		from, status := cmp.Or(in.Source, "-"), cmp.Or(string(in.Status), "-") // "-": no sidecar, or never ran
		id := short(in.ID)
		if in.Depth > 0 {
			id = strings.Repeat("  ", in.Depth-1) + "└ " + id
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s%s\n", id, ago(in.LastActivity), in.Runs, status, modelLabel(in.Model), from, dir, oneLine(in.FirstPrompt, 60))
	}

	return tw.Flush()
}

// flagSince is `uah sessions --since`.
const flagSince = "since"

// parseSince reads --since, an RFC 3339 time.
func parseSince(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --since %q (want an RFC 3339 time, such as 2026-09-29T08:00:00Z)", v)
	}

	return t, nil
}

func showSession(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return cli.Exit("usage: uah sessions show <id or unique prefix>", exitUsage)
	}
	stateDir, err := filepath.Abs(cmd.String("state-dir"))
	if err != nil {
		return fmt.Errorf("failed to resolve state dir: %w", err)
	}
	info, err := app.FindSession(ctx, stateDir, cmd.Args().First())
	if err != nil {
		return exitError(err)
	}
	runs, err := session.Load(stateDir, info.ID)
	if err != nil {
		return err
	}
	reqs := cachestats.Attribute(session.CacheRequests(runs), cachestats.TTL)
	if cmd.Bool(flagJSON) {
		return writeJSON(os.Stdout, struct {
			Session session.Info `json:"session"`
			Runs    []runView    `json:"runs"`
			Cache   cacheView    `json:"cache"`
		}{info, runViews(runs), cacheViewOf(reqs)})
	}
	printTranscript(os.Stdout, info, runs)
	fmt.Fprintln(os.Stdout, "\nprompt "+cachestats.Summarize(reqs, cachestats.APIPrice).Line())

	return nil
}

// cacheView is the prompt cache accounting in `uah sessions show --json`:
// the summary, and each model request with its misses.
type cacheView struct {
	cachestats.Summary

	// LongestGapMS is Summary.LongestGap.
	LongestGapMS int64              `json:"longest_idle_gap_ms"`
	Requests     []cacheRequestView `json:"by_request"`
}

type cacheRequestView struct {
	Start    time.Time `json:"start"`
	Model    string    `json:"model"`
	Effort   string    `json:"effort"`
	Input    int64     `json:"input"`
	Cached   int64     `json:"cached"`
	Expected int64     `json:"expected"`
	// GapMS is the time since the request before.
	GapMS  int64             `json:"gap_ms"`
	Misses []cachestats.Miss `json:"misses"`
}

func cacheViewOf(reqs []cachestats.Attributed) cacheView {
	v := cacheView{Summary: cachestats.Summarize(reqs, cachestats.APIPrice), Requests: make([]cacheRequestView, 0, len(reqs))}
	v.LongestGapMS = v.LongestGap.Milliseconds()
	for _, r := range reqs {
		misses := r.Misses
		if misses == nil {
			misses = []cachestats.Miss{}
		}
		v.Requests = append(v.Requests, cacheRequestView{
			Start: r.Start, Model: r.Model, Effort: r.Effort, Input: r.Input, Cached: r.Cached, Expected: r.Expected,
			GapMS: r.Gap.Milliseconds(), Misses: misses,
		})
	}

	return v
}

// runView is the JSON shape of one run in `uah sessions show --json`.
type runView struct {
	RunID    string      `json:"run_id"`
	Status   core.Status `json:"status"`
	Started  time.Time   `json:"started"`
	Messages []string    `json:"messages"`
	Answer   string      `json:"answer"`
}

func runViews(runs []session.LoadedRun) []runView {
	views := make([]runView, 0, len(runs))
	for _, r := range runs {
		v := runView{RunID: r.Record.Result.Request.RunID, Status: r.Record.Result.Status, Started: r.Record.Result.StartedAt, Messages: []string{}}
		for _, e := range r.Events {
			switch m := e.(type) {
			case core.UserMessage:
				v.Messages = append(v.Messages, m.Text)
			case core.AssistantMessage:
				if m.Final {
					v.Answer = m.Text
				}
			}
		}
		views = append(views, v)
	}

	return views
}

func printTranscript(w io.Writer, info session.Info, runs []session.LoadedRun) {
	fmt.Fprintf(w, "session %s · %s/%s · %s\n", info.ID, info.Provider, modelLabel(info.Model), info.Workspace)
	said := map[string]string{} // messages by ID, for a rewind
	for _, r := range runs {
		res := r.Record.Result
		fmt.Fprintf(w, "\n── run %s · %s · %s\n", res.Request.RunID, res.Status, res.StartedAt.Local().Format("2006-01-02 15:04"))
		for _, e := range r.Events {
			switch m := e.(type) {
			case core.DeveloperMessage:
				fmt.Fprintf(w, "  (%s, %d bytes)\n", developerLabel(m.Text), len(m.Text))
			case core.UserMessage:
				if contextprep.IsPrepared(m.Text) { // a session from before the developer role
					fmt.Fprintf(w, "  (prepared context, %d bytes)\n", len(m.Text))

					continue
				}
				if note, ok := goalLabel(m.Text); ok {
					fmt.Fprintf(w, "  (%s)\n", note)

					continue
				}
				said[m.ID] = m.Text
				fmt.Fprintf(w, "› %s\n", m.Text)
			case core.ToolCalled:
				label := m.Label
				if files := patch.Describe(m.Arguments); m.Name == patch.ToolName && files != "" {
					label = files
				}
				fmt.Fprintf(w, "  → %s  %s\n", m.Name, label)
			case engine.PatchApplied:
				fmt.Fprint(w, patch.Plain(m.Files, "    "))
			case core.AssistantMessage:
				if m.Final {
					fmt.Fprintf(w, "✓ %s\n", citations.Strip(m.Text))
				} else if m.Text != "" {
					fmt.Fprintf(w, "· %s\n", oneLine(citations.Strip(m.Text), 200))
				}
			case core.RunnerError:
				fmt.Fprintf(w, "error: %s\n", m.Message)
			case engine.Compacted:
				fmt.Fprintf(w, "⋯ %s\n", compactedLine(m))
			case engine.Rewound:
				fmt.Fprintf(w, "↺ went back to before %q; it and what followed left the agent's context\n", oneLine(images.Display(said[m.MessageID]), 60))
			}
		}
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("failed to write JSON: %w", err)
	}

	return nil
}

// homeShort writes paths under the home directory with ~.
func homeShort(path string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, h) {
		return "~" + strings.TrimPrefix(path, h)
	}

	return path
}

// ago formats a time as a short relative age.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "-"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}

	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// goalLabel names one of the goal's messages (/goal), uah's rather than
// the user's: "goal continuation, automatic" or the user's change.
func goalLabel(text string) (string, bool) {
	switch kind, body := goal.Parse(text); kind {
	case goal.KindContinuation:
		return "goal continuation, automatic", true
	case goal.KindBudgetLimit:
		return "goal budget reached", true
	case goal.KindObjectiveUpdated:
		return "goal objective updated", true
	case goal.KindUser:
		return "goal: " + strings.ReplaceAll(body, "\n", " · "), true
	case goal.KindNone:
	}

	return "", false
}

// developerLabel names a developer message, uah's context for the model.
func developerLabel(text string) string {
	if contextprep.IsPrepared(text) {
		return "prepared context"
	}
	if note, ok := goalLabel(text); ok {
		return note
	}
	if id, state, ok := engine.ParseSubagentNotification(text); ok {
		return "agent " + id + " " + state
	}

	return "developer message"
}
