package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/usage"
)

// usageCommand is `uah usage`: the subscription's usage windows, as Codex's
// /status card shows them.
func usageCommand() *cli.Command {
	return &cli.Command{
		Name:  "usage",
		Usage: "show your ChatGPT plan's usage: each window, the percent used and left, and when it resets",
		Description: "Reads the usage of the ChatGPT subscription behind the openai-codex provider with one\n" +
			"read-only request, as Codex's /status does, and prints a line per window, named by its length\n" +
			"(5h, weekly). Other providers have no usage to show.",
		Flags:        append(sessionFlags(), &cli.BoolFlag{Name: flagJSON, Usage: "print the usage as JSON"}),
		OnUsageError: onUsageError,
		Action:       usageAction,
	}
}

func usageAction(ctx context.Context, cmd *cli.Command) error {
	report, err := app.ReadUsage(ctx, inputs(cmd), os.Getenv)
	if err != nil {
		return exitError(err)
	}
	if cmd.Bool(flagJSON) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(usageJSONFor(report)); err != nil {
			return fmt.Errorf("failed to write the usage: %w", err)
		}

		return nil
	}
	printUsage(os.Stdout, report, time.Now())

	return nil
}

// usageJSON is `uah usage --json`.
type usageJSON struct {
	Provider   string         `json:"provider"`
	Plan       string         `json:"plan"`
	CapturedAt time.Time      `json:"captured_at"`
	Reached    bool           `json:"limit_reached"`
	Windows    []windowJSON   `json:"windows"`
	Credits    *usage.Credits `json:"credits,omitempty"`
}

type windowJSON struct {
	Limit       string     `json:"limit"`
	Name        string     `json:"name"`
	Minutes     int64      `json:"minutes,omitempty"`
	UsedPercent float64    `json:"used_percent"`
	LeftPercent float64    `json:"left_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}

func usageJSONFor(r app.UsageReport) usageJSON {
	s := r.Snapshot
	out := usageJSON{Provider: r.Provider, Plan: s.Plan, CapturedAt: s.CapturedAt, Reached: s.Reached(), Credits: s.Credits, Windows: []windowJSON{}}
	for _, row := range s.Rows() {
		w := windowJSON{Limit: row.LimitID, Name: row.Label, Minutes: row.Window.Minutes, UsedPercent: row.Window.UsedPercent, LeftPercent: row.Window.LeftPercent()}
		if at := row.Window.ResetsAt; !at.IsZero() {
			w.ResetsAt = &at
		}
		out.Windows = append(out.Windows, w)
	}

	return out
}

func printUsage(w io.Writer, r app.UsageReport, now time.Time) {
	w = colorOut(w)
	s := r.Snapshot
	plan := s.Plan
	if plan == "" {
		plan = "unknown"
	}
	fmt.Fprintf(w, "%s plan (%s)\n", plan, r.Provider)
	rows := s.Rows()
	if len(rows) == 0 {
		fmt.Fprintln(w, "no usage windows")
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row.Label))
	}
	for _, row := range rows {
		line := fmt.Sprintf("%-*s  %s %3.0f%% used · %s", width, row.Label, barStyle(row.Window.LeftPercent()).Render(usage.Bar(row.Window.LeftPercent())), row.Window.UsedPercent, row.Left())
		if at := row.Window.ResetsAt; !at.IsZero() {
			line += " · resets " + usage.ResetLabel(at, now)
		}
		fmt.Fprintln(w, line)
	}
	if c := s.Credits; c != nil && (c.HasCredits || c.Unlimited) {
		balance := c.Balance
		if c.Unlimited {
			balance = "unlimited"
		}
		fmt.Fprintf(w, "credits: %s\n", trimZeros(balance))
	}
	if s.Reached() {
		fmt.Fprintln(w, "the usage limit is reached")
	}
}

// barStyle colors a window's bar by what is left: green, yellow under
// half, red under a fifth.
func barStyle(left float64) lipgloss.Style {
	switch {
	case left < 20:
		return cliStyles.bad
	case left < 50:
		return cliStyles.warn
	}

	return cliStyles.ok
}

// trimZeros drops a decimal balance's trailing zeros: the API sends
// "62500.0000000000".
func trimZeros(balance string) string {
	if !strings.Contains(balance, ".") {
		return balance
	}

	return strings.TrimSuffix(strings.TrimRight(balance, "0"), ".")
}
