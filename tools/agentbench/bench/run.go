package bench

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Harness names.
const (
	HarnessUAH   = "uah"
	HarnessCodex = "codex"
)

// CommandReview is Key.Command of a review run.
const CommandReview = "review"

// Config is one invocation of the benchmark.
type Config struct {
	Tasks     []Task
	Harnesses []string
	Repeat    int
	Model     string
	Effort    string
	Parallel  int
	// Timeout is a run's wall-clock limit unless its task sets one.
	Timeout time.Duration
	// MaxRuns refuses a plan with more runs than this.
	MaxRuns int
	// Work is the scratch root: workspaces, the shared temporary
	// directory and Go build cache, and uah's home.
	Work string
	// Out is the results file (JSON lines); run artifacts go in the
	// directory of the same name without the extension.
	Out   string
	UAH   string // the uah binary
	Codex string // the codex binary
	Price Price
	// Mode is the permission mode of both: ModeAuto or ModeWorkspace.
	Mode string
	// UAHEnv is KEY=VALUE pairs added to uah's environment, such as
	// UAH_ADAPTIVE_EFFORT, and UAHConfig lines added to its configuration
	// file; Variant labels the uah runs they make, so they and the control
	// runs (no variant) share a results file.
	UAHEnv    []string
	UAHConfig []string
	Variant   string
	// Review runs each task's review command against its ReviewBase
	// instead of sending its prompt: `uah review` and `codex review`, the
	// harnesses' /review without a TUI.
	Review bool
	// OwnerEnv gives the harness the user's own environment (OwnerEnv)
	// with Shell as SHELL, instead of the bench's isolated one; the
	// fixtures and the checks keep the isolated one.
	OwnerEnv bool
	Shell    string
	Keep     bool
	Log      io.Writer
}

// Key names a run; a results file holds each key once.
type Key struct {
	Task    string `json:"task"`
	Harness string `json:"harness"`
	// Variant is a uah run's -variant label; "" is the control.
	Variant string `json:"variant,omitempty"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
	Repeat  int    `json:"repeat"`
	// Command is "review" for a review run (-review), "" for the prompt.
	Command string `json:"command,omitempty"`
}

func (k Key) String() string {
	return fmt.Sprintf("%s/%s/%s-%s/%d", k.Task, k.Label(), k.Model, k.Effort, k.Repeat)
}

// Label is the harness with its variant and command: "uah",
// "uah+prompt-runner", "codex@review".
func (k Key) Label() string {
	l := k.Harness
	if k.Variant != "" {
		l += "+" + k.Variant
	}
	if k.Command != "" {
		l += "@" + k.Command
	}

	return l
}

// Run statuses.
const (
	StatusDone    = "done"    // the harness exited 0
	StatusFailed  = "failed"  // the harness exited non-zero
	StatusTimeout = "timeout" // the wall-clock limit stopped it
	StatusError   = "error"   // the benchmark could not run it; resume retries it
)

// Result is one run's line in the results file.
type Result struct {
	Key

	Status   string `json:"status"`
	Passed   bool   `json:"passed"`
	ExitCode int    `json:"exit_code"`
	// StartedAt is when the harness process started: the timeline's zero.
	StartedAt time.Time   `json:"started_at"`
	Check     CheckResult `json:"check"`
	Metrics   Metrics     `json:"metrics"`
	DiffStat  string      `json:"diff_stat,omitempty"`
	Error     string      `json:"error,omitempty"`
	// Env is what -uah-env added to the harness's environment, and
	// UAHConfig what -uah-config added to its configuration file.
	Env       []string `json:"env,omitempty"`
	UAHConfig []string `json:"uah_config,omitempty"`
	// Shell is the harness's SHELL when it ran with the owner's
	// environment (-owner-env), else "".
	Shell string `json:"owner_env_shell,omitempty"`
	// Failures counts a uah run's failed and wasted calls (failures.go).
	Failures *Failures `json:"failures,omitempty"`
	// AgentUse is how a uah run's main agent treated its subagents, when
	// it spawned any.
	AgentUse *AgentUse `json:"agent_use,omitempty"`
	// Artifacts is the run's directory: the stamped event stream, stderr,
	// the timeline, the diff, and uah's state.
	Artifacts string `json:"artifacts"`
}

// Plan lists the runs of cfg in the order they start: repeats outermost,
// and the harnesses' order alternating by repeat so neither always goes
// first. A uah-only task has no other harness's runs.
func Plan(cfg Config) []Key {
	var keys []Key
	for r := 1; r <= cfg.Repeat; r++ {
		for i, t := range cfg.Tasks {
			hs := cfg.Harnesses
			if (r+i)%2 == 0 && len(hs) == 2 {
				hs = []string{hs[1], hs[0]}
			}
			for _, h := range hs {
				if t.UAHOnly() && h != HarnessUAH {
					continue
				}
				k := Key{Task: t.Name, Harness: h, Model: cfg.Model, Effort: cfg.Effort, Repeat: r}
				if cfg.Review {
					k.Command = CommandReview
				}
				if h == HarnessUAH {
					k.Variant = cfg.Variant
				}
				keys = append(keys, k)
			}
		}
	}

	return keys
}

// LoadResults reads a results file; a missing file has none.
func LoadResults(path string) ([]Result, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Result
	err = eachLine(f, func(_ time.Time, line []byte) error {
		var r Result
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, r)

		return nil
	})

	return out, err
}

// Execute runs every planned run that the results file does not already
// hold (a run that ended in StatusError runs again), appending each result
// as it finishes.
func Execute(ctx context.Context, cfg Config) error {
	done, err := LoadResults(cfg.Out)
	if err != nil {
		return err
	}
	have := map[Key]bool{}
	for _, r := range done {
		if r.Status != StatusError {
			have[r.Key] = true
		}
	}
	var todo []Key
	for _, k := range Plan(cfg) {
		if !have[k] {
			todo = append(todo, k)
		}
	}
	if len(todo) > cfg.MaxRuns {
		return fmt.Errorf("%d runs planned, more than -max-runs %d", len(todo), cfg.MaxRuns)
	}
	fmt.Fprintf(cfg.Log, "%d runs to do (%d already in %s)\n", len(todo), len(Plan(cfg))-len(todo), cfg.Out)
	if len(todo) == 0 {
		return nil
	}
	env, err := newEnv(ctx, cfg)
	if err != nil {
		return err
	}
	tasks := map[string]Task{}
	for _, t := range cfg.Tasks {
		tasks[t.Name] = t
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Out), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(cfg.Out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	var mu sync.Mutex
	jobs := make(chan Key)
	var wg sync.WaitGroup
	for range max(1, cfg.Parallel) {
		wg.Go(func() {
			for k := range jobs {
				res := runOne(ctx, cfg, env, tasks[k.Task], k)
				b, _ := json.Marshal(res)
				mu.Lock()
				_, _ = out.Write(append(b, '\n'))
				fmt.Fprintf(cfg.Log, "%-48s %-7s pass=%-5v wall=%5.0fs model=%5.0fs tools=%5.0fs overlap=%4.0fs calls=%3d tokens=%d/%d/%d %s\n",
					k, res.Status, res.Passed, sec(res.Metrics.WallMS), sec(res.Metrics.ModelMS), sec(res.Metrics.ToolMS), sec(res.Metrics.OverlapMS),
					res.Metrics.ToolCalls, res.Metrics.Tokens.Input, res.Metrics.Tokens.Cached, res.Metrics.Tokens.Output, res.Error)
				mu.Unlock()
			}
		})
	}
	for _, k := range todo {
		select {
		case jobs <- k:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()

	return ctx.Err()
}

func sec(ms int64) float64 { return float64(ms) / 1000 }

// runEnv is what every run shares.
type runEnv struct {
	work      string
	tmp       string
	uahHome   string
	uahConfig string // uah's user configuration: the permission mode and -uah-config's lines
	codexHome string // the user's, for a run with a fake home
	// base is the environment of the fixtures and the checks, and harness
	// the harness's: base, or the owner's with -owner-env.
	base    []string
	harness []string
}

// newEnv makes the shared directories and the base environment: the
// user's, without variables that would steer either harness away from its
// defaults, with a temporary directory both sandboxes let commands write
// (and the Go build cache in it), and no network for Go. With
// cfg.OwnerEnv the harness gets the owner's environment instead.
func newEnv(ctx context.Context, cfg Config) (*runEnv, error) {
	work, mode, variant, extra := cfg.Work, cfg.Mode, cfg.Variant, cfg.UAHConfig
	e := &runEnv{work: work, tmp: filepath.Join(work, "tmp"), uahHome: filepath.Join(work, "uah-home"), codexHome: os.Getenv("CODEX_HOME")}
	if e.codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		e.codexHome = filepath.Join(home, ".codex")
	}
	for _, d := range []string{e.tmp, e.uahHome, filepath.Join(work, "runs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	name := "uah-" + cmp.Or(mode, ModeWorkspace)
	if len(extra) > 0 {
		name += "-" + variant // another variant's lines must not reach these runs
	}
	e.uahConfig = filepath.Join(work, name+".toml")
	conf, err := UAHConfigFile(mode, extra)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(e.uahConfig, []byte(conf), 0o644); err != nil {
		return nil, err
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if dropEnv(name) {
			continue
		}
		e.base = append(e.base, kv)
	}
	e.base = append(e.base,
		"TMPDIR="+e.tmp,
		"GOCACHE="+filepath.Join(e.tmp, "gocache"),
		"GOPROXY=off", "GOTOOLCHAIN=local",
		// A slow suite is slow every time: no test results from the shared cache.
		"GOFLAGS=-count=1",
		"PYTHONDONTWRITEBYTECODE=1",
		"NO_COLOR=1",
	)
	e.harness = e.base
	if cfg.OwnerEnv {
		if err := checkShell(cfg.Shell); err != nil {
			return nil, err
		}
		e.harness = OwnerEnv(os.Environ(), cfg.Shell, UserTempDir(ctx))
	}

	return e, nil
}

// UAHConfigFile is the configuration file a uah run gets: the permission
// mode, and the extra lines. Each extra line is a top-level key: a table
// would take the keys after it, so one is refused.
func UAHConfigFile(mode string, extra []string) (string, error) {
	var b strings.Builder
	for _, line := range extra {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			return "", fmt.Errorf("-uah-config %q: only top-level keys, not tables", line)
		}
		b.WriteString(line + "\n")
	}
	if mode == ModeAuto {
		b.WriteString("permission_mode = \"auto\"\n")
	}
	var check map[string]any
	if _, err := toml.Decode(b.String(), &check); err != nil {
		return "", fmt.Errorf("-uah-config: %w", err)
	}

	return b.String(), nil
}

func dropEnv(name string) bool {
	for _, p := range []string{"UAH_", "WEBTTY_", "OPENAI_", "CLAUDE", "GO", "TMPDIR", "PYTHON"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}

	return name == "NO_COLOR"
}

// runOne runs one key end to end and never returns without a result.
func runOne(ctx context.Context, cfg Config, env *runEnv, t Task, k Key) (res Result) {
	res = Result{Key: k}
	art := filepath.Join(strings.TrimSuffix(cfg.Out, filepath.Ext(cfg.Out)), k.Task, fmt.Sprintf("%s-%s-%s-%d", k.Label(), k.Model, k.Effort, k.Repeat))
	res.Artifacts = art
	defer func() {
		if res.Error != "" && res.Status == "" {
			res.Status = StatusError
		}
	}()
	if err := os.RemoveAll(art); err != nil {
		res.Error = err.Error()

		return res
	}
	if err := os.MkdirAll(art, 0o755); err != nil {
		res.Error = err.Error()

		return res
	}
	scratch, err := os.MkdirTemp(filepath.Join(env.work, "runs"), k.Task+"-"+k.Harness+"-")
	if err != nil {
		res.Error = err.Error()

		return res
	}
	if !cfg.Keep {
		defer os.RemoveAll(scratch)
	}
	fx, err := t.StartFixture(ctx, scratch, env.base)
	if err != nil {
		res.Error = err.Error()

		return res
	}
	defer fx.Stop()
	runEnv := append(slicesClone(env.base), fx.Env...)
	ws := filepath.Join(scratch, "ws")
	if err := t.Prepare(ctx, ws, runEnv); err != nil {
		res.Error = err.Error()

		return res
	}
	limit := cmpDur(time.Duration(t.Timeout), cfg.Timeout)
	inv := invocation(cfg, env, fx, t, k, ws, art)
	if k.Harness == HarnessUAH {
		res.Env, res.UAHConfig = cfg.UAHEnv, cfg.UAHConfig
	}
	if cfg.OwnerEnv {
		res.Shell = cfg.Shell
	}
	out, err := launch(ctx, inv, limit, art)
	res.ExitCode, res.Status = out.exit, out.status
	if err != nil {
		res.Error = err.Error()
	}
	if !out.start.IsZero() {
		res.StartedAt = out.start.UTC()
		res.Metrics.WallMS = out.wall.Milliseconds()
		if perr := measure(&res, cfg.Price); perr != nil {
			res.Error = strings.TrimSpace(res.Error + "; parse: " + perr.Error())
		}
	}
	if res.Status == StatusFailed && res.Metrics.Turns == 0 {
		// The harness never took the prompt: a bad flag, a login, a
		// crash. Resume runs it again.
		res.Status = StatusError
		b, _ := os.ReadFile(filepath.Join(art, "stderr.txt"))
		res.Error = strings.TrimSpace(res.Error + "; harness: " + oneLine(string(b), 200))
	}
	res.DiffStat = saveDiff(ctx, ws, filepath.Join(art, "diff.patch"))
	chk, err := t.RunCheck(ctx, ws, filepath.Join(scratch, "check"), runEnv, fx)
	res.Check = chk
	if err != nil {
		res.Error = strings.TrimSpace(res.Error + "; " + err.Error())
		res.Status = StatusError
	}
	_ = os.WriteFile(filepath.Join(art, "check.txt"), []byte(chk.Output), 0o644)
	res.Passed = chk.Passed && res.Status != StatusTimeout

	return res
}

// measure parses the run's artifacts into its timeline (saved as
// timeline.json) and computes its metrics, keeping the wall time.
func measure(res *Result, price Price) error {
	f, err := os.Open(filepath.Join(res.Artifacts, "stream.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	var tl *Timeline
	if res.Harness == HarnessCodex {
		tl, err = ParseCodex(f, res.StartedAt)
	} else {
		tl, err = ParseUAH(f, res.StartedAt, filepath.Join(res.Artifacts, "uah-state"))
	}
	if err != nil {
		return err
	}
	if res.Harness == HarnessCodex {
		for i := range tl.Requests {
			tl.Requests[i].Effort = res.Effort // set once for the session
		}
	}
	wall := time.Duration(res.Metrics.WallMS) * time.Millisecond
	tl.End = res.StartedAt.Add(wall)
	res.Metrics = tl.Compute(wall, price)
	writeJSON(filepath.Join(res.Artifacts, "timeline.json"), tl)
	if res.Harness == HarnessUAH {
		if res.Failures, err = RunFailures(*res); err != nil {
			return err
		}
		state := filepath.Join(res.Artifacts, "uah-state")
		if res.AgentUse, err = CountAgentUse(state, mainSession(filepath.Join(res.Artifacts, "stream.jsonl"))); err != nil {
			return err
		}
	}

	return nil
}

// RunFailures counts a uah run's failures from its artifacts.
func RunFailures(res Result) (*Failures, error) {
	return CountFailures(filepath.Join(res.Artifacts, "uah-state"), mainSession(filepath.Join(res.Artifacts, "stream.jsonl")))
}

// mainSession is the session a uah stream opened first, or "".
func mainSession(stream string) string {
	f, err := os.Open(stream)
	if err != nil {
		return ""
	}
	defer f.Close()
	id := ""
	_ = eachLine(f, func(_ time.Time, line []byte) error {
		var e uahEvent
		if id == "" && json.Unmarshal(line, &e) == nil && e.Type == "session_opened" {
			id = e.ID
		}

		return nil
	})

	return id
}

// Remeasure parses every run in the results file again, as after a change
// to a parser or the metrics or the prices, and rewrites the file.
func Remeasure(path string, price Price) error {
	results, err := LoadResults(path)
	if err != nil {
		return err
	}
	var b []byte
	for i := range results {
		if results[i].StartedAt.IsZero() {
			continue
		}
		if err := measure(&results[i], price); err != nil {
			return fmt.Errorf("%s: %w", results[i].Key, err)
		}
		line, err := json.Marshal(results[i])
		if err != nil {
			return err
		}
		b = append(append(b, line...), '\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}
