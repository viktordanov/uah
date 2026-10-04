package perf

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"time"
)

// Options configure a run of the harness.
type Options struct {
	// Sizes are the fixture sizes the size-dependent scenarios run on.
	Sizes []Size
	// Match, when set, keeps the scenarios whose name it matches.
	Match *regexp.Regexp
	// Count runs each scenario this many times and keeps the medians.
	Count int
	// ProfileDir, with CPUProfile or MemProfile, gets each scenario's
	// profiles (pprof).
	ProfileDir             string
	CPUProfile, MemProfile bool
	// Goroutines writes the stacks of the goroutines a scenario left
	// behind to ProfileDir.
	Goroutines bool
	// Scratch holds the environments; it is removed after the run unless
	// Keep.
	Scratch string
	Keep    bool
	// Real, when set, is a uah home whose largest RealSessions sessions
	// are copied (read-only) into the scratch directory and loaded too.
	Real         string
	RealSessions int
	// Log gets progress lines (nil: none).
	Log io.Writer
}

// scenario is one entry of the harness.
type scenario struct {
	name string
	// sized scenarios run once per size (and real session); the others
	// run on the small fixture.
	sized bool
	run   func(r *run, t target) ([]Named, error)
}

var scenarios = []scenario{
	{name: "load", sized: true, run: (*run).load},
	{name: "tui", sized: true, run: (*run).tui},
	{name: "turn", sized: true, run: (*run).turn},
	{name: "spawn", sized: true, run: func(r *run, t target) ([]Named, error) { return r.spawn(t, false) }},
	{name: "fork", sized: true, run: func(r *run, t target) ([]Named, error) { return r.spawn(t, true) }},
	{name: "memory", sized: true, run: (*run).memory},
	{name: "tui-turn", run: (*run).tuiTurn},
	{name: "agents", run: (*run).agents},
	{name: "leak", run: (*run).leak},
}

// realScenarios are the scenarios a copied real session runs: no tool
// calls, no subagents.
var realScenarios = []string{"load", "tui", "turn"}

// Run records the workload, then runs every selected scenario Count
// times, and reports the medians.
func Run(ctx context.Context, opts Options) (*Report, error) {
	opts.Count = max(opts.Count, 1)
	if opts.Scratch == "" {
		dir, err := os.MkdirTemp("", "uah-perf-")
		if err != nil {
			return nil, fmt.Errorf("failed to make a scratch directory: %w", err)
		}
		opts.Scratch = dir
	}
	if !opts.Keep {
		defer os.RemoveAll(opts.Scratch)
	}
	logf(opts.Log, "recording the workload in %s", opts.Scratch)
	tmpl, err := Record(ctx, filepath.Join(opts.Scratch, "record"))
	if err != nil {
		return nil, err
	}
	targets := make([]target, 0, len(opts.Sizes))
	for _, size := range opts.Sizes {
		targets = append(targets, target{name: size.Name, build: func(e *Env) (Fixture, error) { return tmpl.Build(e, size) }})
	}
	if opts.Real != "" {
		reals, err := realTargets(opts.Real, max(opts.RealSessions, 1))
		if err != nil {
			return nil, err
		}
		targets = append(targets, reals...)
	}
	small := target{name: Small.Name, build: func(e *Env) (Fixture, error) { return tmpl.Build(e, Small) }}
	r := &run{ctx: ctx, opts: opts, scratch: opts.Scratch}
	rep := newReport(ctx, opts)
	if !Sandboxed(opts.Scratch) { //nolint:contextcheck,nolintlint // on Linux, the sandbox probes bwrap once per process, with its own timeout; not on darwin
		rep.Sandbox = "none"
	}
	for _, sc := range scenarios {
		on := []target{small}
		if sc.sized {
			on = targets
		}
		for _, t := range on {
			if t.real && !slices.Contains(realScenarios, sc.name) {
				continue
			}
			name := sc.name + "/" + t.name
			if opts.Match != nil && !opts.Match.MatchString(name) {
				continue
			}
			if err := rep.collect(r, sc, t); err != nil {
				return rep, fmt.Errorf("%s: %w", name, err)
			}
		}
	}

	return rep, nil
}

// collect runs the scenario Count times and adds the medians.
func (rep *Report) collect(r *run, sc scenario, t target) error {
	var runs [][]Named
	for i := range r.opts.Count {
		logf(r.opts.Log, "%s/%s (%d of %d)", sc.name, t.name, i+1, r.opts.Count)
		named, err := sc.run(r, t)
		if err != nil {
			return err
		}
		runs = append(runs, named)
		runtime.GC()
	}
	for i, n := range runs[0] {
		samples := make([]Sample, 0, len(runs))
		for _, run := range runs {
			samples = append(samples, run[i].Sample)
		}
		rep.add(n.Name, n.Fixture, samples)
	}

	return nil
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
	}
}
