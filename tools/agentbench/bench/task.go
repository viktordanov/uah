package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// A task lives in its own directory under the tasks directory:
//
//	task.json   the prompt, the check, and what the task exercises
//	repo/       the files of the repository the agent starts from
//	solution/   files laid over repo/ that solve the task (the reference solution)
//	check/      files laid over the agent's result before the check runs (hidden tests)
//
// The agent never sees solution/ or check/.
const (
	taskFile    = "task.json"
	repoDir     = "repo"
	solutionDir = "solution"
	// fixtureReadme is a README.md in repo/ or solution/ as stored here,
	// so that Memoria does not track each one as a document of uah's;
	// copyFixture names it README.md again in the workspace.
	fixtureReadme = "README.fixture.md"
	checkDir      = "check"
)

// Task is one benchmark task.
type Task struct {
	Name string `json:"-"`
	Dir  string `json:"-"`
	// Prompt is the message both harnesses get.
	Prompt string `json:"prompt"`
	// FollowUps are more messages, each sent when the agent is done with
	// the one before, in the same session: one line each, as uah exec
	// --stdin reads them. Codex cannot take one without saving its
	// session, so a task with follow-ups is tagged uah-only.
	FollowUps []string `json:"follow_ups,omitempty"`
	// Check is a shell command run in the result, after check/ is laid
	// over it; exit status 0 passes.
	Check string `json:"check"`
	// CheckTimeout bounds the check (default 3m).
	CheckTimeout Duration `json:"check_timeout,omitzero"`
	// Timeout overrides the run's wall-clock limit.
	Timeout Duration `json:"timeout,omitzero"`
	// Exercises says what the task measures.
	Exercises string `json:"exercises"`
	// Tags group tasks: a language, "slow", "subagents".
	Tags []string `json:"tags,omitempty"`
	// Delete lists files the reference solution removes.
	Delete []string `json:"solution_delete,omitempty"`
	// Setup is a shell script run in the workspace after its first
	// commit, with TASK_DIR set: to make git history, for example.
	Setup string `json:"setup,omitempty"`
	// SolutionScript runs in the workspace after solution/ is laid over
	// it, for a solution that is not only files.
	SolutionScript string `json:"solution_script,omitempty"`
	// Service is a shell command run in the task's directory for the
	// whole run, with PORT set; {{URL}} in the prompt and the check, and
	// SERVICE_URL, are its address.
	Service string `json:"service,omitempty"`
	// FakeHome gives the run its own HOME, filled from home/, so a task
	// may edit files under ~ without touching the user's.
	FakeHome bool `json:"fake_home,omitempty"`
	// MCPServers are MCP servers the uah run gets, by name, written into
	// that run's configuration: a stdio server's command and arguments,
	// where {{TASK_DIR}} is the task's directory. Codex's runs would need
	// them too, so a task with them is tagged uah-only.
	MCPServers map[string]MCPServer `json:"mcp_servers,omitempty"`
	// ReviewBase is the base branch of the task's review run (-review):
	// the harness's review command (`uah review`, `codex review`) reviews
	// the checked-out branch against it, in place of the prompt, and its
	// review becomes REVIEW.md for the check.
	ReviewBase string `json:"review_base,omitempty"`
}

// MCPServer is a task's stdio MCP server.
type MCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// ReviewFile is where a review run's harness writes its review, in the
// workspace, for the check.
const ReviewFile = "REVIEW.md"

// TagUAHOnly marks a task only uah runs: the plan leaves Codex out.
const TagUAHOnly = "uah-only"

// UAHOnly reports whether only uah runs the task.
func (t Task) UAHOnly() bool { return slices.Contains(t.Tags, TagUAHOnly) }

// mcpProblems are what is wrong with the task's MCP servers.
func (t Task) mcpProblems() []string {
	var problems []string
	if len(t.MCPServers) > 0 && !t.UAHOnly() {
		problems = append(problems, "MCP servers need the tag "+TagUAHOnly)
	}
	for _, name := range slices.Sorted(maps.Keys(t.MCPServers)) {
		if t.MCPServers[name].Command == "" || !mcpName.MatchString(name) {
			problems = append(problems, "MCP server "+name+": a name of letters, digits, _ and -, and a command")
		}
	}

	return problems
}

// mcpName is an MCP server's name, which becomes a TOML key and part of
// its tools' names.
var mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Duration is a time.Duration written as "90s" in JSON.
type Duration time.Duration

// UnmarshalJSON reads a duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)

	return err
}

// MarshalJSON writes a duration string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

const defaultCheckTimeout = 3 * time.Minute

// LoadTasks reads every task under dir whose name matches re (nil: all),
// sorted by name.
func LoadTasks(dir string, re *regexp.Regexp) ([]Task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for _, e := range entries {
		if !e.IsDir() || re != nil && !re.MatchString(e.Name()) {
			continue
		}
		t, err := LoadTask(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	slices.SortFunc(tasks, func(a, b Task) int { return strings.Compare(a.Name, b.Name) })

	return tasks, nil
}

// LoadTask reads and checks one task directory.
func LoadTask(dir string) (Task, error) {
	t := Task{Name: filepath.Base(dir), Dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, taskFile))
	if err != nil {
		return t, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return t, fmt.Errorf("%s: %w", t.Name, err)
	}
	var problems []string
	if strings.TrimSpace(t.Prompt) == "" {
		problems = append(problems, "no prompt")
	}
	if strings.TrimSpace(t.Check) == "" {
		problems = append(problems, "no check")
	}
	if t.Exercises == "" {
		problems = append(problems, "no exercises")
	}
	for _, f := range t.FollowUps {
		if strings.TrimSpace(f) == "" || strings.Contains(f, "\n") {
			problems = append(problems, "a follow-up must be one non-empty line")
		}
	}
	if len(t.FollowUps) > 0 && !t.UAHOnly() {
		problems = append(problems, "follow-ups need the tag "+TagUAHOnly)
	}
	problems = append(problems, t.mcpProblems()...)
	for _, d := range []string{repoDir, solutionDir} {
		if d == solutionDir && t.SolutionScript != "" {
			continue // the script is the solution; git keeps no empty folder
		}
		if st, err := os.Stat(filepath.Join(dir, d)); err != nil || !st.IsDir() {
			problems = append(problems, "no "+d+"/")
		}
	}
	if len(problems) > 0 {
		return t, fmt.Errorf("task %s: %s", t.Name, strings.Join(problems, ", "))
	}

	return t, nil
}

// Prepare copies the task's repository to ws and commits it, so the agent
// starts in a clean git repository and its changes show in git, then runs
// the task's setup.
func (t Task) Prepare(ctx context.Context, ws string, env []string) error {
	if err := copyFixture(filepath.Join(t.Dir, repoDir), ws); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=agentbench", "-c", "user.email=agentbench@localhost", "commit", "-q", "-m", "task: " + t.Name},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir, cmd.Env = ws, slices.Concat(os.Environ(), gitIdentity)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, out)
		}
	}
	if t.Setup != "" {
		if err := runScript(ctx, t.Setup, ws, env); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
	}

	return nil
}

// ApplySolution lays the reference solution over ws and runs its script.
func (t Task) ApplySolution(ctx context.Context, ws string, env []string) error {
	for _, d := range t.Delete {
		if err := os.Remove(filepath.Join(ws, d)); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(t.Dir, solutionDir)); err == nil {
		if err := copyFixture(filepath.Join(t.Dir, solutionDir), ws); err != nil {
			return err
		}
	}
	if t.SolutionScript != "" {
		return runScript(ctx, t.SolutionScript, ws, env)
	}

	return nil
}

// CheckResult is the outcome of a task's check.
type CheckResult struct {
	Passed     bool   `json:"passed"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"` // the tail
}

const checkOutputTail = 4000

// RunCheck copies ws to scratch, lays check/ over the copy, and runs the
// check there, so hidden tests replace whatever the agent wrote and ws
// stays as the agent left it. env includes the fixture's variables.
func (t Task) RunCheck(ctx context.Context, ws, scratch string, env []string, fx *Fixture) (CheckResult, error) {
	if err := os.RemoveAll(scratch); err != nil {
		return CheckResult{}, err
	}
	if err := copyTree(ws, scratch); err != nil {
		return CheckResult{}, err
	}
	if _, err := os.Stat(filepath.Join(t.Dir, checkDir)); err == nil {
		if err := copyTree(filepath.Join(t.Dir, checkDir), scratch); err != nil {
			return CheckResult{}, err
		}
	}
	limit := cmpDur(time.Duration(t.CheckTimeout), defaultCheckTimeout)
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", fx.Expand(t.Check))
	cmd.Dir = scratch
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	began := time.Now()
	out, err := cmd.CombinedOutput()
	res := CheckResult{Passed: err == nil, DurationMS: time.Since(began).Milliseconds(), Output: tail(string(out), checkOutputTail)}
	if _, isExit := errors.AsType[*exec.ExitError](err); err != nil && !isExit && ctx.Err() == nil {
		return res, fmt.Errorf("check: %w", err)
	}
	if ctx.Err() != nil {
		res.Output += "\n[check timed out after " + limit.String() + "]"
	}

	return res, nil
}

func cmpDur(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}

	return def
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return "…" + s[len(s)-n:]
}

// copyTree copies the regular files and directories under src into dst,
// keeping file modes; it follows no links.
func copyTree(src, dst string) error { return copyFiles(src, dst, false) }

// copyFixture copies a task's repo/ or solution/ as copyTree does, with
// each README.fixture.md as README.md.
func copyFixture(src, dst string) error { return copyFiles(src, dst, true) }

// A file that disappears during the walk, such as a lock file of git
// running in the background, is skipped.
func copyFiles(src, dst string, fixture bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if fixture && !d.IsDir() && d.Name() == fixtureReadme {
			target = filepath.Join(filepath.Dir(target), "README.md")
		}
		info, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(p)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}

			return os.WriteFile(target, b, info.Mode().Perm())
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(target)

			return os.Symlink(link, target)
		}

		return nil
	})
}
