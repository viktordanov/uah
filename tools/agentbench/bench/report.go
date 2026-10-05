package bench

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Report is the markdown report of results: per model and effort, a
// summary per harness (a uah variant counts as its own harness), a
// uah-against-Codex table per task, each variant against the control per
// task, and every run.
func Report(results []Result, price Price) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent benchmark report\n\nGenerated %s from %d runs. Times are medians in seconds over a task's runs; cost is estimated at $%.2f / $%.3f / $%.2f per million input / cached / output tokens.\n",
		time.Now().UTC().Format(time.DateTime), len(results), price.Input, price.Cached, price.Output)
	groups := map[string][]Result{}
	for _, r := range results {
		g := r.Model + " · effort " + r.Effort
		groups[g] = append(groups[g], r)
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	slices.Sort(names)
	for _, g := range names {
		rs := groups[g]
		fmt.Fprintf(&b, "\n## %s\n", g)
		summary(&b, rs)
		behaviorTable(&b, rs)
		agentUseTable(&b, rs)
		perTask(&b, rs)
		variantTables(&b, rs)
		perRun(&b, rs)
	}
	b.WriteString("\nCodex reports no model requests or per-request tokens: its requests are inferred from the gaps between its commands (see tools/agentbench/README.md), so its model, overlap, and request counts are estimates.\n")

	return b.String()
}

// harnesses are the runs' harness labels: uah, its variants, then Codex.
func harnesses(rs []Result) []string {
	var hs []string
	for _, r := range rs {
		if !slices.Contains(hs, r.Label()) {
			hs = append(hs, r.Label())
		}
	}
	slices.SortFunc(hs, func(a, b string) int { return cmp.Or(cmp.Compare(order(a), order(b)), cmp.Compare(a, b)) })

	return hs
}

func order(label string) int {
	switch {
	case label == HarnessUAH:
		return 0
	case strings.HasPrefix(label, HarnessUAH+"+"):
		return 1
	default:
		return 2
	}
}

func pick(rs []Result, keep func(Result) bool) []Result {
	var out []Result
	for _, r := range rs {
		if keep(r) {
			out = append(out, r)
		}
	}

	return out
}

func median(rs []Result, f func(Metrics) float64) float64 {
	if len(rs) == 0 {
		return 0
	}
	v := make([]float64, len(rs))
	for i, r := range rs {
		v[i] = f(r.Metrics)
	}
	slices.Sort(v)
	if n := len(v); n%2 == 0 {
		return (v[n/2-1] + v[n/2]) / 2
	}

	return v[len(v)/2]
}

func passes(rs []Result) int {
	n := 0
	for _, r := range rs {
		if r.Passed {
			n++
		}
	}

	return n
}

func s(ms int64) float64 { return float64(ms) / 1000 }

func summary(b *strings.Builder, rs []Result) {
	b.WriteString("\n### Per harness\n\n| Harness | Runs | Passed | Wall | Model | Tools | Overlap | Model only | Tools only | Idle | Requests | Calls | Max conc. | Input tok. | Cached | Output tok. | Cost (total) |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, h := range harnesses(rs) {
		hr := pick(rs, func(r Result) bool { return r.Label() == h })
		var tok Tokens
		var cost float64
		for _, r := range hr {
			tok = tok.Add(r.Metrics.Tokens)
			cost += r.Metrics.CostUSD
		}
		fmt.Fprintf(b, "| %s | %d | %d (%.0f%%) | %.1f | %.1f | %.1f | %.1f | %.1f | %.1f | %.1f | %.0f | %.0f | %.0f | %d | %d | %d | $%.2f |\n",
			h, len(hr), passes(hr), 100*float64(passes(hr))/float64(len(hr)),
			median(hr, func(m Metrics) float64 { return s(m.WallMS) }),
			median(hr, func(m Metrics) float64 { return s(m.ModelMS) }),
			median(hr, func(m Metrics) float64 { return s(m.ToolMS) }),
			median(hr, func(m Metrics) float64 { return s(m.OverlapMS) }),
			median(hr, func(m Metrics) float64 { return s(m.ModelOnlyMS) }),
			median(hr, func(m Metrics) float64 { return s(m.ToolOnlyMS) }),
			median(hr, func(m Metrics) float64 { return s(m.IdleMS) }),
			median(hr, func(m Metrics) float64 { return float64(m.Requests) }),
			median(hr, func(m Metrics) float64 { return float64(m.ToolCalls) }),
			median(hr, func(m Metrics) float64 { return float64(m.MaxConcurrent) }),
			tok.Input, tok.Cached, tok.Output, cost)
	}
}

func perTask(b *strings.Builder, rs []Result) {
	hs := harnesses(rs)
	if !slices.Contains(hs, HarnessUAH) || !slices.Contains(hs, HarnessCodex) {
		return
	}
	rs = pick(rs, func(r Result) bool { return r.Variant == "" })
	b.WriteString("\n### uah against Codex, per task\n\nA wall ratio below 1 means uah was faster; tokens are median input tokens per run.\n\n| Task | uah passed | Codex passed | uah wall | Codex wall | Wall ratio | uah overlap | uah max conc. | Codex max conc. | uah tokens | Codex tokens | uah cost | Codex cost |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	var tasks []string
	for _, r := range rs {
		if !slices.Contains(tasks, r.Task) {
			tasks = append(tasks, r.Task)
		}
	}
	slices.Sort(tasks)
	for _, t := range tasks {
		u := pick(rs, func(r Result) bool { return r.Task == t && r.Harness == HarnessUAH })
		c := pick(rs, func(r Result) bool { return r.Task == t && r.Harness == HarnessCodex })
		uw := median(u, func(m Metrics) float64 { return s(m.WallMS) })
		cw := median(c, func(m Metrics) float64 { return s(m.WallMS) })
		ratio := "-"
		if cw > 0 && uw > 0 {
			ratio = fmt.Sprintf("%.2f", uw/cw)
		}
		fmt.Fprintf(b, "| %s | %d/%d | %d/%d | %.1f | %.1f | %s | %.1f | %.0f | %.0f | %.0f | %.0f | $%.3f | $%.3f |\n",
			t, passes(u), len(u), passes(c), len(c), uw, cw, ratio,
			median(u, func(m Metrics) float64 { return s(m.OverlapMS) }),
			median(u, func(m Metrics) float64 { return float64(m.MaxConcurrent) }),
			median(c, func(m Metrics) float64 { return float64(m.MaxConcurrent) }),
			median(u, func(m Metrics) float64 { return float64(m.Tokens.Input) }),
			median(c, func(m Metrics) float64 { return float64(m.Tokens.Input) }),
			median(u, func(m Metrics) float64 { return m.CostUSD }),
			median(c, func(m Metrics) float64 { return m.CostUSD }))
	}
}

func perRun(b *strings.Builder, rs []Result) {
	b.WriteString("\n### Runs\n\n| Task | Harness | # | Status | Passed | Wall | Model only | Tools only | Overlap | Idle | Wait | Req. | Calls | Subagents | Max / avg conc. | Longest call | Tokens in / cached / out | Cost | Diff |\n| --- | --- | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- |\n")
	rs = slices.Clone(rs)
	slices.SortFunc(rs, func(x, y Result) int {
		return cmp.Or(cmp.Compare(x.Task, y.Task), cmp.Compare(order(x.Label()), order(y.Label())), cmp.Compare(x.Label(), y.Label()), cmp.Compare(x.Repeat, y.Repeat))
	})
	for _, r := range rs {
		m := r.Metrics
		status := r.Status
		if r.Error != "" {
			status += " (" + oneLine(r.Error, 60) + ")"
		}
		fmt.Fprintf(b, "| %s | %s | %d | %s | %v | %.1f | %.1f | %.1f | %.1f | %.1f | %.1f | %d | %d | %d | %d / %.1f | %.1fs %s | %d / %d / %d | $%.3f | %s |\n",
			r.Task, r.Label(), r.Repeat, status, r.Passed, s(m.WallMS), s(m.ModelOnlyMS), s(m.ToolOnlyMS), s(m.OverlapMS), s(m.IdleMS), s(m.WaitMS),
			m.Requests, m.ToolCalls, m.Subagents, m.MaxConcurrent, m.AvgConcurrent, s(m.LongestCallMS), "`"+oneLine(m.LongestCall, 40)+"`",
			m.Tokens.Input, m.Tokens.Cached, m.Tokens.Output, m.CostUSD, r.DiffStat)
	}
}

func oneLine(v string, n int) string {
	v = strings.ReplaceAll(strings.Join(strings.Fields(v), " "), "|", "\\|")
	v = strings.ReplaceAll(v, "`", "'")
	if len(v) > n {
		return v[:n] + "…"
	}

	return v
}

// behaviorTable sums each harness's Behavior over its runs.
func behaviorTable(b *strings.Builder, rs []Result) {
	b.WriteString("\n### Where the model time goes, per harness\n\nOutput tokens split by what they wrote (patch, tool arguments, and text by bytes written; Codex's split is rough), then sums over the runs: requests spent on a startup ritual and their time, patch-only requests followed by a separate command request, escalations with the total and median approval latency, requests that did not complete, and context compactions with their summary time.\n\n| Harness | Reasoning | Patch | Tool args | Text | Ritual req. (s) | Patch then verify | Escalations (refused) | Review s (median) | Approval waits (s) | Aborted (s) | Compactions (s) | Requests by effort |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, h := range harnesses(rs) {
		var t Behavior
		efforts := map[string]int{}
		var medians []float64
		for _, r := range pick(rs, func(r Result) bool { return r.Label() == h }) {
			x := r.Metrics.Behavior
			t.OutputReasoning += x.OutputReasoning
			t.OutputPatch += x.OutputPatch
			t.OutputToolArgs += x.OutputToolArgs
			t.OutputText += x.OutputText
			t.RitualRequests += x.RitualRequests
			t.RitualMS += x.RitualMS
			t.PatchThenVerify += x.PatchThenVerify
			t.Escalations += x.Escalations
			t.EscalationsRefused += x.EscalationsRefused
			t.ReviewMS += x.ReviewMS
			t.ApprovalWaits += x.ApprovalWaits
			t.ApprovalWaitMS += x.ApprovalWaitMS
			t.Aborted += x.Aborted
			t.AbortedMS += x.AbortedMS
			t.Compactions += x.Compactions
			t.CompactionMS += x.CompactionMS
			for e, n := range x.Efforts {
				efforts[e] += n
			}
			if x.Escalations > 0 {
				medians = append(medians, s(x.ReviewMedianMS))
			}
		}
		out := max(1, t.OutputReasoning+t.OutputPatch+t.OutputToolArgs+t.OutputText)
		pct := func(n int64) string { return fmt.Sprintf("%d (%.0f%%)", n, 100*float64(n)/float64(out)) }
		slices.Sort(medians)
		med := 0.0
		if len(medians) > 0 {
			med = medians[len(medians)/2]
		}
		var es []string
		for e, n := range efforts {
			es = append(es, fmt.Sprintf("%s %d", e, n))
		}
		slices.Sort(es)
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %d (%.1f) | %d | %d (%d) | %.1f (%.1f) | %d (%.1f) | %d (%.1f) | %d (%.1f) | %s |\n",
			h, pct(t.OutputReasoning), pct(t.OutputPatch), pct(t.OutputToolArgs), pct(t.OutputText),
			t.RitualRequests, s(t.RitualMS), t.PatchThenVerify, t.Escalations, t.EscalationsRefused, s(t.ReviewMS), med, t.ApprovalWaits, s(t.ApprovalWaitMS),
			t.Aborted, s(t.AbortedMS), t.Compactions, s(t.CompactionMS), strings.Join(es, ", "))
	}
}

// agentUseTable sums, per harness, how the uah runs' main agents treated
// their subagents; nothing when no run spawned one.
func agentUseTable(b *strings.Builder, rs []Result) {
	header := false
	for _, h := range harnesses(rs) {
		var t AgentUse
		runs := 0
		for _, r := range pick(rs, func(r Result) bool { return r.Label() == h && r.AgentUse != nil }) {
			x := r.AgentUse
			runs++
			t.Spawns += x.Spawns
			t.Messages += x.Messages
			t.Interrupts += x.Interrupts
			t.ToRunning += x.ToRunning
			t.Closes += x.Closes
			t.ClosedRunning += x.ClosedRunning
			t.Waits += x.Waits
			t.WaitsTimedOut += x.WaitsTimedOut
			t.Notes += x.Notes
			t.BusyRequests += x.BusyRequests
			t.AgentOnlyRequests += x.AgentOnlyRequests
			t.BusyTokens = t.BusyTokens.Add(x.BusyTokens)
		}
		if runs == 0 {
			continue
		}
		if !header {
			b.WriteString("\n### Subagents, per harness\n\nSums over the uah runs whose main agent spawned subagents: its agent calls (messages and interrupts with send_input; \"to running\" reached a child while it worked), waits and how many timed out, the notifications it got, and its model requests while a child worked, with those that only waited or messaged, and their input and output tokens.\n\n| Harness | Runs | Spawns | Messages | Interrupts | To running | Closes (running) | Waits (timed out) | Notes | Requests while children ran (agent-only) | Their input | Their output |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
			header = true
		}
		fmt.Fprintf(b, "| %s | %d | %d | %d | %d | %d | %d (%d) | %d (%d) | %d | %d (%d) | %d | %d |\n",
			h, runs, t.Spawns, t.Messages, t.Interrupts, t.ToRunning, t.Closes, t.ClosedRunning, t.Waits, t.WaitsTimedOut, t.Notes,
			t.BusyRequests, t.AgentOnlyRequests, t.BusyTokens.Input, t.BusyTokens.Output)
	}
}
