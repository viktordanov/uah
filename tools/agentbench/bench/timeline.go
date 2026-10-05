package bench

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Timeline is one run as both harnesses can describe it: the model
// requests, the tool calls, and the tokens, with times as offsets from the
// run's start. Each parser fills what its harness reports; Inferred says
// which parts were derived rather than read.
type Timeline struct {
	Harness  string    `json:"harness"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	Requests []Request `json:"requests"`
	Calls    []Call    `json:"calls"`
	// Compactions are the main agent's context compactions (uah only:
	// Codex's events do not show them).
	Compactions []Compaction `json:"compactions,omitempty"`
	// Turns counts user turns: one per prompt for both harnesses.
	Turns  int    `json:"turns"`
	Tokens Tokens `json:"tokens"`
	// Inferred lists what was estimated: "requests" when model requests
	// come from the gaps between tool calls (Codex reports none),
	// "request_tokens" when tokens are known per turn only.
	Inferred []string `json:"inferred,omitempty"`
	Answer   string   `json:"answer,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

// Request is one model request. FirstByte is zero when unknown.
type Request struct {
	Agent string `json:"agent,omitempty"` // "" for the main agent, else the subagent's session
	// Turn is the user turn a main agent's request belongs to, from 1: the
	// prompt's, then each follow-up's. 0 for a subagent, or when unknown.
	Turn      int    `json:"turn,omitempty"`
	StartMS   int64  `json:"start_ms"`
	FirstMS   int64  `json:"first_byte_ms,omitempty"`
	EndMS     int64  `json:"end_ms"`
	Tokens    Tokens `json:"tokens"`
	ToolCalls int    `json:"tool_calls"`
	// Effort is the reasoning effort the request ran at: for uah, from the
	// request's model_attempt diagnostics when they have it, else the
	// session's; EffortReason is why, with adaptive effort.
	Effort       string `json:"effort,omitempty"`
	EffortReason string `json:"effort_reason,omitempty"`
	// Stop is how the response ended ("complete", or a cancel or
	// failure); "" when it never ended.
	Stop string `json:"stop,omitempty"`
	// TextBytes is the length of the assistant text it wrote.
	TextBytes int `json:"text_bytes,omitempty"`
}

// Compaction is one compaction of the context: its summary call ran from
// StartMS to EndMS, when the context held Tokens (as the last response
// reported). Trigger is "auto" (the context reached the limit) or what
// else started it; Error is why it failed, else "".
type Compaction struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Trigger string `json:"trigger"`
	Tokens  int64  `json:"tokens"`
	Error   string `json:"error,omitempty"`
}

// Call is one tool call: issued by the model, started, finished.
type Call struct {
	Agent    string `json:"agent,omitempty"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // "tool", "wait" (blocks on other work), or "agent" (spawns or steers a subagent)
	Args     string `json:"args,omitempty"`
	IssuedMS int64  `json:"issued_ms"`
	StartMS  int64  `json:"start_ms"`
	EndMS    int64  `json:"end_ms"`
	OK       bool   `json:"ok"`
	Detail   string `json:"detail,omitempty"`
	// Request is the index of the request that issued it, or -1.
	Request int `json:"request"`
	// ArgsBytes is the length of its arguments as the model wrote them.
	ArgsBytes int `json:"args_bytes"`
	// Escalated says the call asked to run outside the sandbox.
	Escalated bool `json:"escalated,omitempty"`
}

// Tokens are a request's or a run's token counts. Input includes Cached,
// and Output includes Reasoning, as both providers count them.
type Tokens struct {
	Input     int64 `json:"input"`
	Cached    int64 `json:"cached"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning,omitempty"`
}

// Add returns the sum of two counts.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{Input: t.Input + o.Input, Cached: t.Cached + o.Cached, Output: t.Output + o.Output, Reasoning: t.Reasoning + o.Reasoning}
}

// ms is the offset of at from start in milliseconds.
func ms(start, at time.Time) int64 { return at.Sub(start).Milliseconds() }

// argsLimit bounds an argument summary.
const argsLimit = 160

// summarize shortens a tool's arguments to one line.
func summarize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > argsLimit {
		return s[:argsLimit] + "…"
	}

	return s
}

// toolWaitAgent is the agent tool that waits for subagents.
const toolWaitAgent = "wait_agent"

// Call kinds.
const (
	KindTool  = "tool"  // does work: a command, an edit, a search
	KindWait  = "wait"  // blocks until other work finishes
	KindAgent = "agent" // spawns or steers a subagent
)

// callKind classifies a tool by name.
func callKind(name string) string {
	switch strings.ToLower(name) {
	case "wait", toolWaitAgent, "await", "wait_for":
		return KindWait
	case "spawn_agent", "send_input", "close_agent", "resume_agent", "agent", "task", "collab_tool_call":
		return KindAgent
	}

	return KindTool
}

// Metrics are what the report compares, all in milliseconds unless named
// otherwise.
type Metrics struct {
	WallMS int64 `json:"wall_ms"`
	// ModelMS is the time at least one model request, or a compaction's
	// summary call, was in flight.
	ModelMS int64 `json:"model_ms"`
	// ToolMS is the time at least one tool call ran (waits excluded).
	ToolMS int64 `json:"tool_ms"`
	// WaitMS is the time a wait call blocked with no tool or model busy.
	WaitMS int64 `json:"wait_ms"`
	// OverlapMS is the time a model request and a tool call ran at once:
	// the async gain.
	OverlapMS int64 `json:"overlap_ms"`
	// ModelOnlyMS, ToolOnlyMS, OverlapMS, and IdleMS split the wall time:
	// the critical path is the model alone, the tools alone, both, or
	// neither (startup, harness overhead, waits on nothing).
	ModelOnlyMS int64 `json:"model_only_ms"`
	ToolOnlyMS  int64 `json:"tool_only_ms"`
	IdleMS      int64 `json:"idle_ms"`
	// FirstByteMS is the median time to the first byte of a request, when known.
	FirstByteMS int64 `json:"first_byte_ms,omitempty"`
	// LongestCallMS is the longest tool call; LongestCall names it.
	LongestCallMS int64  `json:"longest_call_ms"`
	LongestCall   string `json:"longest_call,omitempty"`

	Turns     int `json:"turns"`
	Requests  int `json:"requests"`
	ToolCalls int `json:"tool_calls"`
	Subagents int `json:"subagents"`
	// MaxConcurrent is the most tool calls running at once; AvgConcurrent
	// is the mean while any ran.
	MaxConcurrent int     `json:"max_concurrent"`
	AvgConcurrent float64 `json:"avg_concurrent"`

	Tokens  Tokens  `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`

	// Behavior is what the session mining found costs time: see
	// behavior.go.
	Behavior Behavior `json:"behavior"`
}

// Price is a model's price in US dollars per million tokens.
type Price struct {
	Input  float64 `json:"input"`
	Cached float64 `json:"cached"`
	Output float64 `json:"output"`
}

// Cost is what tokens cost at the price.
func (p Price) Cost(t Tokens) float64 {
	return (float64(t.Input-t.Cached)*p.Input + float64(t.Cached)*p.Cached + float64(t.Output)*p.Output) / 1e6
}

type span struct{ a, b int64 }

// union merges spans into disjoint sorted ones.
func union(spans []span) []span {
	spans = slices.DeleteFunc(slices.Clone(spans), func(s span) bool { return s.b <= s.a })
	slices.SortFunc(spans, func(x, y span) int { return cmp.Compare(x.a, y.a) })
	var out []span
	for _, s := range spans {
		if n := len(out); n > 0 && s.a <= out[n-1].b {
			out[n-1].b = max(out[n-1].b, s.b)

			continue
		}
		out = append(out, s)
	}

	return out
}

func total(spans []span) int64 {
	var t int64
	for _, s := range spans {
		t += s.b - s.a
	}

	return t
}

// intersect returns the time two disjoint sorted span lists share.
func intersect(x, y []span) []span {
	var out []span
	for i, j := 0, 0; i < len(x) && j < len(y); {
		a, b := max(x[i].a, y[j].a), min(x[i].b, y[j].b)
		if a < b {
			out = append(out, span{a, b})
		}
		if x[i].b < y[j].b {
			i++
		} else {
			j++
		}
	}

	return out
}

// Compute derives the metrics of a timeline whose run took wall.
func (tl *Timeline) Compute(wall time.Duration, price Price) Metrics {
	m := Metrics{WallMS: wall.Milliseconds(), Turns: tl.Turns, Requests: len(tl.Requests), Tokens: tl.Tokens, CostUSD: price.Cost(tl.Tokens)}
	var model, tools, waits []span
	var firsts []int64
	agents := map[string]bool{}
	for _, r := range tl.Requests {
		model = append(model, span{r.StartMS, r.EndMS})
		if r.FirstMS > 0 {
			firsts = append(firsts, r.FirstMS-r.StartMS)
		}
		if r.Agent != "" {
			agents[r.Agent] = true
		}
	}
	for _, c := range tl.Compactions {
		// The summary is a model call too.
		model = append(model, span{c.StartMS, c.EndMS})
	}
	var events []span // +1 at a, -1 at b, for concurrency
	var busySum int64
	for _, c := range tl.Calls {
		s := span{c.StartMS, c.EndMS}
		m.ToolCalls++
		if c.Kind == KindWait {
			waits = append(waits, s)

			continue
		}
		tools = append(tools, s)
		events = append(events, s)
		busySum += max(0, c.EndMS-c.StartMS)
		if d := c.EndMS - c.StartMS; d > m.LongestCallMS {
			m.LongestCallMS, m.LongestCall = d, c.Name+" "+c.Args
		}
	}
	m.Subagents = len(agents)
	model, tools = union(model), union(tools)
	m.ModelMS, m.ToolMS = total(model), total(tools)
	m.OverlapMS = total(intersect(model, tools))
	m.ModelOnlyMS = m.ModelMS - m.OverlapMS
	m.ToolOnlyMS = m.ToolMS - m.OverlapMS
	busy := union(append(slices.Clone(model), tools...))
	m.WaitMS = max(0, total(union(waits))-total(intersect(union(waits), busy)))
	m.IdleMS = max(0, m.WallMS-total(busy))
	if m.ToolMS > 0 {
		m.AvgConcurrent = float64(busySum) / float64(m.ToolMS)
	}
	m.MaxConcurrent = maxConcurrent(events)
	m.Behavior = tl.behavior()
	if len(firsts) > 0 {
		slices.Sort(firsts)
		m.FirstByteMS = firsts[len(firsts)/2]
	}

	return m
}

func maxConcurrent(spans []span) int {
	type edge struct {
		at    int64
		delta int
	}
	edges := make([]edge, 0, 2*len(spans))
	for _, s := range spans {
		// A call Codex reports as instant still ran: give it a millisecond.
		edges = append(edges, edge{s.a, 1}, edge{max(s.a+1, s.b), -1})
	}
	// Ends sort before starts at the same instant: back-to-back calls do
	// not overlap.
	slices.SortFunc(edges, func(x, y edge) int { return cmp.Or(cmp.Compare(x.at, y.at), cmp.Compare(x.delta, y.delta)) })
	n, best := 0, 0
	for _, e := range edges {
		n += e.delta
		best = max(best, n)
	}

	return best
}
