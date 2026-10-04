package perf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/testing/fakellm"
)

// Each scenario builds what it needs in a fresh environment, measures one
// block with a Probe, and returns one or more named samples.

// target is what a scenario runs on: a synthetic size or a copied real
// session.
type target struct {
	name string
	// build makes the session in e and describes it.
	build func(e *Env) (Fixture, error)
	// real targets run no tool calls: their workspace is not the one the
	// session was recorded in.
	real bool
}

// run is one scenario run's context.
type run struct {
	ctx     context.Context
	opts    Options
	scratch string
	n       int
}

// env makes a fresh environment for a scenario.
func (r *run) env(name string) (*Env, error) {
	r.n++

	return NewEnv(filepath.Join(r.scratch, fmt.Sprintf("%02d-%s", r.n, safeName(name))))
}

func (r *run) probe(name string, e *Env) Probe {
	return Probe{
		Name: name, ProfileDir: r.opts.ProfileDir, CPUProfile: r.opts.CPUProfile, MemProfile: r.opts.MemProfile,
		Goroutines: r.opts.Goroutines, State: e.Home, Conns: e.LLM.Conns,
	}
}

// Named is a sample with its scenario's name and fixture.
type Named struct {
	Name    string
	Fixture *Fixture
	Sample  Sample
}

// sessionFile is the fixture's session file in e.
func sessionFile(e *Env, id string) string {
	return filepath.Join(e.Home, "sessions", id+".session.jsonl")
}

// lineCount counts the lines of a file.
func lineCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	return bytes.Count(data, []byte("\n"))
}

// firstRequest is how long after since the first request arrived.
func firstRequest(llm *fakellm.Server, since time.Time) float64 {
	arrivals := llm.Arrivals()
	if len(arrivals) == 0 {
		return 0
	}

	return ms(arrivals[0].At.Sub(since))
}

// requestStats adds the requests: their count, megabytes sent, and the
// fake model's own time reading them.
func requestStats(s *Sample, llm *fakellm.Server) {
	arrivals := llm.Arrivals()
	var sent int
	var parse time.Duration
	for _, a := range arrivals {
		sent += a.Bytes
		parse += a.Parse
	}
	s.Extra["requests"] = float64(len(arrivals))
	s.Extra["request_mb"] = mb(int64(sent))
	s.Extra["server_ms"] = ms(parse)
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }

// load opens a session as `uah resume` does and sends one message: the
// time to the first model request, with the index already built (its
// build time is index_ms, outside the block).
func (r *run) load(t target) ([]Named, error) {
	name := "load/" + t.name
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	e.LLM.Light = true
	start := time.Now()
	if _, err := store.List(r.ctx, e.Home); err != nil {
		return nil, fmt.Errorf("failed to index the sessions: %w", err)
	}
	index := time.Since(start)
	historyStart := time.Now()
	if _, err := session.Load(e.Home, fx.SessionID); err != nil {
		return nil, fmt.Errorf("failed to load the history: %w", err)
	}
	history := time.Since(historyStart)
	var s *session.Session
	sample, err := r.probe(name, e).Measure(func(x *Sample) error {
		start := time.Now()
		var err error
		if s, _, err = e.Open(r.ctx, fx.SessionID, true); err != nil {
			return err
		}
		x.Extra["open_ms"] = ms(time.Since(start))
		submitted, syncs := time.Now(), embedded.SessionSyncs()
		if _, err := Turn(s, "Where were we?"); err != nil {
			return err
		}
		x.Extra["first_request_ms"] = firstRequest(e.LLM, submitted)
		x.Extra["session_syncs"] = float64(embedded.SessionSyncs() - syncs)
		requestStats(x, e.LLM)

		return nil
	}, func() { closeSession(s) })
	sample.Extra["index_ms"], sample.Extra["history_ms"] = ms(index), ms(history)

	return []Named{{Name: name, Fixture: &fx, Sample: sample}}, err
}

func closeSession(s *session.Session) {
	if s != nil {
		_ = s.Close()
	}
}

// scrollSteps is how many page-ups, then page-downs, the TUI scenario
// sends.
const scrollSteps = 20

// tui resumes the session in the TUI: the time to the first frame that
// shows the transcript's end, then scrolling by pages.
func (r *run) tui(t target) ([]Named, error) {
	name := "tui/" + t.name
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	e.LLM.Light = true
	if _, err := store.List(r.ctx, e.Home); err != nil {
		return nil, fmt.Errorf("failed to index the sessions: %w", err)
	}
	var ui *TUI
	sample, err := r.probe(name, e).Measure(func(x *Sample) error {
		start := time.Now()
		ui = StartTUI(r.ctx, e, fx.SessionID)
		viewed, err := ui.probe.waitOpened()
		if err != nil {
			return err
		}
		if fx.Marker != "" {
			if err := ui.probe.waitScreen("the transcript's end", shows(fx.Marker)); err != nil {
				return err
			}
		}
		x.Extra["first_frame_ms"] = ms(viewed.Sub(start))
		paint, err := ui.painted(viewed)
		if err != nil {
			return err
		}
		x.Extra["first_paint_ms"] = ms(paint.Sub(start))
		_, before := ui.probe.stats()
		var steps []time.Duration
		for i := range 2 * scrollSteps {
			key := term.KeyPgUp
			if i >= scrollSteps {
				key = term.KeyPgDown
			}
			step, err := ui.step(term.KeyPressMsg{Code: key})
			if err != nil {
				return err
			}
			steps = append(steps, step)
		}
		p50, p95, top := percentiles(steps)
		x.Extra["scroll_p50_ms"], x.Extra["scroll_p95_ms"], x.Extra["scroll_max_ms"] = ms(p50), ms(p95), ms(top)
		_, views := ui.probe.stats()
		p50, p95, top = percentiles(views[len(before):])
		x.Extra["view_p50_ms"], x.Extra["view_p95_ms"], x.Extra["view_max_ms"] = ms(p50), ms(p95), ms(top)
		ui.frameStats(x)

		return nil
	}, func() { stopTUI(ui) })

	return []Named{{Name: name, Fixture: &fx, Sample: sample}}, err
}

func stopTUI(ui *TUI) {
	if ui != nil {
		_ = ui.Stop()
	}
}

// step sends msg and waits for the view that follows it. It sends it a
// frame after the last one, as a key pressed after a pause, which term
// draws at once: the time is the update's, the view's, and the write's.
// Sooner, it would wait for the frame budget (term.FrameRate).
func (t *TUI) step(msg term.Msg) (time.Duration, error) {
	time.Sleep(time.Second / term.FrameRate)
	_, views := t.probe.stats()
	start := time.Now()
	t.Send(msg)
	deadline := time.After(waitTimeout)
	for {
		if _, now := t.probe.stats(); len(now) > len(views) {
			return time.Since(start), nil
		}
		select {
		case <-t.probe.changed:
		case <-time.After(time.Millisecond):
		case <-deadline:
			return 0, errors.New("timed out waiting for a frame")
		}
	}
}

// painted waits for the renderer's first write at or after at (term
// writes a frame right after its view) and returns its time.
func (t *TUI) painted(at time.Time) (time.Time, error) {
	deadline := time.Now().Add(waitTimeout)
	for {
		if w, ok := t.term.firstWriteAfter(at); ok {
			return w, nil
		}
		if time.Now().After(deadline) {
			return time.Time{}, errors.New("timed out waiting for the renderer")
		}
		time.Sleep(time.Millisecond)
	}
}

// frameStats adds the views and updates so far and what the terminal got.
func (t *TUI) frameStats(x *Sample) {
	updates, views := t.probe.stats()
	bytes, writes := t.term.stats()
	x.Extra["updates"], x.Extra["views"] = float64(updates), float64(len(views))
	x.Extra["term_kb"], x.Extra["term_writes"] = float64(bytes)/1024, float64(writes)
}

// turn resumes the session and runs the workload turn headless: what a
// turn costs outside the model, and what it writes.
func (r *run) turn(t target) ([]Named, error) {
	name := "turn/" + t.name
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	e.LLM.Light = true
	s, _, err := e.Open(r.ctx, fx.SessionID, true)
	if err != nil {
		return nil, err
	}
	replies := turnReplies("perf")
	if t.real {
		replies = textOnly(replies)
	}
	e.LLM.Script(replies...)
	file := sessionFile(e, fx.SessionID)
	records := lineCount(file)
	sample, err := r.probe(name, e).Measure(func(x *Sample) error {
		start, syncs := time.Now(), embedded.SessionSyncs()
		events, err := Turn(s, "Check the handler again and note what changed.")
		if err != nil {
			return err
		}
		took := time.Since(start)
		x.Extra["turn_ms"] = ms(took)
		x.Extra["first_request_ms"] = firstRequest(e.LLM, start)
		x.Extra["events"] = float64(len(events))
		x.Extra["events_per_s"] = float64(len(events)) / took.Seconds()
		x.Extra["records_appended"] = float64(lineCount(file) - records)
		x.Extra["session_syncs"] = float64(embedded.SessionSyncs() - syncs)
		requestStats(x, e.LLM)

		return nil
	}, func() { closeSession(s) })

	return []Named{{Name: name, Fixture: &fx, Sample: sample}}, err
}

// textOnly keeps the replies' text and reasoning and drops their tool
// calls, for a session whose workspace is not here.
func textOnly(replies []fakellm.Reply) []fakellm.Reply {
	last := replies[len(replies)-1]

	return []fakellm.Reply{{Reasoning: last.Reasoning, Deltas: last.Deltas}}
}

// idleFor is how long the idle scenario watches the TUI.
const idleFor = 3 * time.Second

// tuiTurn runs the workload turn in the TUI on the small fixture, then
// watches the idle TUI: the clock must stop when the turn ends.
func (r *run) tuiTurn(t target) ([]Named, error) {
	name := "tui-turn/" + t.name
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	e.LLM.Light = true
	ui := StartTUI(r.ctx, e, fx.SessionID)
	defer stopTUI(ui)
	if err := ui.probe.waitScreen("the transcript", shows(fx.Marker)); err != nil {
		return nil, err
	}
	e.LLM.Script(turnReplies("tuiturn")...)
	updates0, views0 := ui.probe.stats()
	bytes0, writes0 := ui.term.stats()
	turn, err := r.probe(name, e).Measure(func(x *Sample) error {
		start := time.Now()
		ui.Type("Check the handler again and note what changed.")
		if err := ui.probe.waitScreen("the turn's answer", shows("notes/tuiturn.md")); err != nil {
			return err
		}
		if err := ui.probe.waitScreen("the idle footer", idleScreen); err != nil {
			return err
		}
		x.Extra["turn_ms"] = ms(time.Since(start))
		updates, views := ui.probe.stats()
		bytes, writes := ui.term.stats()
		p50, p95, top := percentiles(views[len(views0):])
		x.Extra["view_p50_ms"], x.Extra["view_p95_ms"], x.Extra["view_max_ms"] = ms(p50), ms(p95), ms(top)
		x.Extra["updates"], x.Extra["views"] = float64(updates-updates0), float64(len(views)-len(views0))
		x.Extra["term_kb"], x.Extra["term_writes"] = float64(bytes-bytes0)/1024, float64(writes-writes0)
		requestStats(x, e.LLM)

		return nil
	}, nil)
	out := []Named{{Name: name, Fixture: &fx, Sample: turn}}
	if err != nil {
		return out, err
	}
	time.Sleep(500 * time.Millisecond) // the last batch and the renderer settle
	quiet := r.probe("idle/tui", e)
	quiet.Quiet = true
	idle, err := quiet.Measure(func(x *Sample) error {
		updates0, views0 := ui.probe.stats()
		bytes0, _ := ui.term.stats()
		time.Sleep(idleFor)
		updates, views := ui.probe.stats()
		bytes, _ := ui.term.stats()
		x.Extra["updates_per_s"] = float64(updates-updates0) / idleFor.Seconds()
		x.Extra["views_per_s"] = float64(len(views)-len(views0)) / idleFor.Seconds()
		x.Extra["term_bytes_per_s"] = float64(bytes-bytes0) / idleFor.Seconds()

		return nil
	}, nil)
	idle.Extra["cpu_ms_per_s"] = ms(idle.CPU) / idleFor.Seconds()
	idle.Extra["wakeups_per_s"] = float64(idle.Wakeups) / idleFor.Seconds()

	return append(out, Named{Name: "idle/tui", Fixture: &fx, Sample: idle}), err
}

// childWatch answers children: it notes when each one's first request came
// (once the fake model read it), and keeps the most goroutines and
// connections seen while children ran.
type childWatch struct {
	mu        sync.Mutex
	parentAt  time.Time
	first     map[string]time.Duration
	peakG     int
	peakConns int
	llm       *fakellm.Server
}

func (w *childWatch) parentReply(calls ...fakellm.Call) fakellm.Reply {
	return fakellm.Reply{From: func(fakellm.Request) fakellm.Reply {
		w.mu.Lock()
		w.parentAt = time.Now()
		w.mu.Unlock()

		return fakellm.Reply{Calls: calls}
	}}
}

func (w *childWatch) child(name string, reply fakellm.Reply) fakellm.Reply {
	return fakellm.Reply{From: func(fakellm.Request) fakellm.Reply {
		w.mu.Lock()
		if _, ok := w.first[name]; !ok {
			w.first[name] = time.Since(w.parentAt) // after the fake model read the request (server_ms)
		}
		w.peakG = max(w.peakG, runtime.NumGoroutine())
		w.peakConns = max(w.peakConns, w.llm.Conns())
		w.mu.Unlock()

		return reply
	}}
}

// waitAll is a wait_agent call for every agent ID in the request so far.
func waitAll() fakellm.Reply {
	return fakellm.Reply{From: func(req fakellm.Request) fakellm.Reply {
		return fakellm.Reply{Calls: []fakellm.Call{{Name: "wait_agent", Args: `{"targets":["` + strings.Join(agentIDs(req), `","`) + `"],"timeout_ms":60000}`}}}
	}}
}

var agentIDPattern = regexp.MustCompile(`"agent_id":"((?:subagent-)?[0-9a-f-]{36})"`)

// spawnAgent is the subagent tool that starts a child.
const spawnAgent = "spawn_agent"

// agentIDs are the agent IDs in the request's tool results, in order.
func agentIDs(req fakellm.Request) []string {
	var out []string
	for _, o := range req.ToolOutputs {
		for _, m := range agentIDPattern.FindAllStringSubmatch(o, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}

	return out
}

// agents runs a turn that spawns two children and forks one, waits for
// them, and finishes: spawn latency, goroutines and connections at the
// peak and after.
func (r *run) agents(t target) ([]Named, error) {
	name := "agents/" + t.name
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	s, _, err := e.Open(r.ctx, fx.SessionID, true)
	if err != nil {
		return nil, err
	}
	w := &childWatch{first: map[string]time.Duration{}, llm: e.LLM}
	e.LLM.Script(
		w.parentReply(
			fakellm.Call{Name: spawnAgent, Args: `{"message":"CHILD-A read the handler"}`},
			fakellm.Call{Name: spawnAgent, Args: `{"message":"CHILD-B read the routes"}`},
			fakellm.Call{Name: spawnAgent, Args: `{"message":"CHILD-F check the log","fork_context":true}`},
		),
		waitAll(), waitAll(),
		fakellm.Reply{Text: "The children are done."},
	)
	e.LLM.Route("CHILD-A", w.child("A", fakellm.Reply{Commands: []string{"cat internal/server/handler.go"}}), fakellm.Reply{Text: "Read it."})
	e.LLM.Route("CHILD-B", w.child("B", fakellm.Reply{Text: "The routes are fine."}))
	e.LLM.Route("CHILD-F", w.child("F", fakellm.Reply{Text: "The log is clean."}))
	sample, err := r.probe(name, e).Measure(func(x *Sample) error {
		if _, err := Turn(s, "Split the review between subagents."); err != nil {
			return err
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		x.Extra["spawn_a_ms"], x.Extra["spawn_b_ms"], x.Extra["fork_ms"] = ms(w.first["A"]), ms(w.first["B"]), ms(w.first["F"])
		x.Extra["peak_goroutines"], x.Extra["peak_conns"] = float64(w.peakG), float64(w.peakConns)
		requestStats(x, e.LLM)

		return nil
	}, func() { closeSession(s) })

	return []Named{{Name: name, Fixture: &fx, Sample: sample}}, err
}

// spawn resumes the session and spawns one child, fresh or forked from
// the session's whole history: the latency to the child's first request.
func (r *run) spawn(t target, fork bool) ([]Named, error) {
	name, args := "spawn/"+t.name, `{"message":"CHILD-S read the routes"}`
	if fork {
		name, args = "fork/"+t.name, `{"message":"CHILD-S read the routes","fork_context":true}`
	}
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	s, _, err := e.Open(r.ctx, fx.SessionID, true)
	if err != nil {
		return nil, err
	}
	w := &childWatch{first: map[string]time.Duration{}, llm: e.LLM}
	e.LLM.Script(w.parentReply(fakellm.Call{Name: spawnAgent, Args: args}), waitAll(), fakellm.Reply{Text: "Done."})
	e.LLM.Route("CHILD-S", w.child("S", fakellm.Reply{Text: "The routes are fine."}))
	sample, err := r.probe(name, e).Measure(func(x *Sample) error {
		if _, err := Turn(s, "Ask a subagent to read the routes."); err != nil {
			return err
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		x.Extra["child_first_request_ms"] = ms(w.first["S"])
		requestStats(x, e.LLM)

		return nil
	}, func() { closeSession(s) })

	return []Named{{Name: name, Fixture: &fx, Sample: sample}}, err
}

// leakRuns is how many sessions the leak scenario opens, runs, and closes.
const leakRuns = 5

// leak opens a session, runs a turn with one command, and closes it,
// leakRuns times: goroutines and connections left after.
func (r *run) leak(t target) ([]Named, error) {
	name := fmt.Sprintf("leak/%d-runs", leakRuns)
	e, err := r.env(name)
	if err != nil {
		return nil, err
	}
	defer e.Close()
	fx, err := t.build(e)
	if err != nil {
		return nil, err
	}
	e.LLM.Light = true
	var open *session.Session
	sample, err := r.probe(name, e).Measure(func(*Sample) error {
		for range leakRuns {
			e.LLM.Script(fakellm.Reply{Commands: []string{"cat cmd/app/main.go"}}, fakellm.Reply{Text: "ok"})
			s, _, err := e.Open(r.ctx, fx.SessionID, true)
			if err != nil {
				return err
			}
			open = s
			if _, err := Turn(s, "Read main.go."); err != nil {
				return err
			}
			if err := s.Close(); err != nil {
				return fmt.Errorf("failed to close the session: %w", err)
			}
			open = nil
		}

		return nil
	}, func() { closeSession(open) })

	return []Named{{Name: name, Fixture: &fx, Sample: sample}}, err
}
