package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
)

// Environment variables behind flags.
const (
	EnvProvider = "UAH_LLM_PROVIDER"
	EnvModel    = "UAH_LLM_MODEL"
	EnvSandbox  = "UAH_SANDBOX"
	EnvAsk      = "UAH_ASK"
	// EnvAdaptiveEffort is --adaptive-effort's variable.
	EnvAdaptiveEffort = "UAH_ADAPTIVE_EFFORT"
	// EnvContextPreparation is on or off, as --no-context-preparation
	// turns it off.
	EnvContextPreparation = "UAH_CONTEXT_PREPARATION"
	// EnvEffortUpdates is on or off: off turns effort updates off, for
	// tests and A/B runs; it has no configuration key.
	EnvEffortUpdates = "UAH_EFFORT_UPDATES"
	// EnvRequestUserInput is on or off: off stops offering the agent's
	// question tool, as [tools.experimental_request_user_input] enabled.
	EnvRequestUserInput = "UAH_REQUEST_USER_INPUT"
	// EnvModelVerbosity is --model-verbosity's variable.
	EnvModelVerbosity = "UAH_MODEL_VERBOSITY"
	// EnvMaxAttempts is the runner's variable for the attempt limit.
	EnvMaxAttempts = "UAH_LLM_MAX_ATTEMPTS"
)

// Source is where an effective setting came from.
type Source string

// Sources, from the strongest to the weakest. A configuration layer's
// source, between the project file and the user file, is its name:
// UAH_EXTRA_CONFIG, or config.d/<file>.
const (
	FromFlag    Source = "flag"
	FromEnv     Source = "env"
	FromSession Source = "session"
	FromProject Source = "project file"
	FromUser    Source = "user file"
	FromDefault Source = "default"
)

// Setting is one effective value and where it came from: one source, or
// several for keys whose files add up.
type Setting struct {
	Key     string   `json:"key"`
	Value   any      `json:"value"`
	Sources []Source `json:"sources"`
}

// File is a configuration file and what became of it: read, not found, or
// not trusted.
type File struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

// fileRead is the state of a file that was read.
const fileRead = "read"

// Report is the effective configuration for a workspace.
type Report struct {
	// Home is uah's home, set by Inspect.
	Home      string `json:"home,omitempty"`
	Workspace string `json:"workspace"`
	// WorkspaceSource is the workspace's source: a flag, the resumed
	// session, or the default (the current directory).
	WorkspaceSource Source `json:"workspace_source"`
	UserFile        File   `json:"user_file"`
	// Layers are the configuration layers read, in merge order.
	Layers      []File    `json:"layers"`
	ProjectFile File      `json:"project_file"`
	Settings    []Setting `json:"settings"`
}

// Origins are what Explain weighs besides the inputs.
type Origins struct {
	// Env holds the environment variables behind flags, by name. A flag
	// value equal to its variable's counts as from the environment.
	Env     map[string]string
	Resumed session.Info
	Layers  config.Layers
	// Dir is the current directory, the default workspace.
	Dir string
	// Catalog is the provider's model list the default model is settled
	// from (nil: none, so the default is FallbackCodexModel).
	Catalog func(provider string) models.Catalog
}

// Inspect loads what Setup loads, the resumed session and the configuration
// files, and explains the effective configuration. It builds no engine.
func Inspect(ctx context.Context, in Inputs) (Report, error) {
	o := Origins{Env: map[string]string{}}
	for _, name := range []string{EnvProvider, EnvModel, EnvSandbox, EnvAsk, EnvMaxAttempts, EnvAdaptiveEffort, EnvContextPreparation, EnvRequestUserInput, EnvModelVerbosity} {
		o.Env[name] = os.Getenv(name)
	}
	var err error
	if o.Dir, err = os.Getwd(); err != nil {
		return Report{}, fmt.Errorf("failed to find the current directory: %w", err)
	}
	if in.SessionRef != "" {
		stateDir, err := filepath.Abs(in.StateDir)
		if err != nil {
			return Report{}, fmt.Errorf("failed to resolve state dir: %w", err)
		}
		if o.Resumed, err = FindSession(ctx, stateDir, in.SessionRef); err != nil {
			return Report{}, err
		}
	}
	if o.Layers, err = config.LoadLayers(in.ConfigPath, workspaceIn(in, o)); err != nil {
		return Report{}, usage(err)
	}
	if stateDir, err := filepath.Abs(in.StateDir); err == nil {
		// The cached list at any age: `uah config` makes no request.
		o.Catalog = models.New(models.Options{Dir: models.CacheDir(stateDir)}).Cached
	}
	rep, err := Explain(in, o)
	rep.Home = home.Dir()

	return rep, err
}

// Explain reports the effective configuration, as Resolve decides it, with
// each value's source. It does no I/O.
func Explain(in Inputs, o Origins) (Report, error) {
	wsSource := pick(input(in.Workspace, "", nil), sessionValue(o.Resumed.Workspace), FromDefault)
	in.Workspace = workspaceIn(in, o)
	cfg := o.Layers.Merged()
	r, err := Resolve(in, o.Resumed, cfg)
	if err != nil {
		return Report{}, err
	}
	if o.Catalog != nil {
		r.SettleModel(o.Catalog(r.Settings.Provider))
	}
	rep := Report{Workspace: in.Workspace, WorkspaceSource: wsSource, UserFile: File{Path: in.ConfigPath, State: fileRead}, ProjectFile: File{Path: config.ProjectFile(in.Workspace), State: fileRead}}
	if o.Layers.UserFile == "" {
		rep.UserFile.State = "not found"
	}
	rep.Layers = []File{}
	for _, x := range o.Layers.Extra {
		rep.Layers = append(rep.Layers, File{Path: x.Path, State: fileRead})
	}
	switch {
	case !o.Layers.Trusted:
		rep.ProjectFile.State = "not trusted"
	case o.Layers.ProjectFile == "":
		rep.ProjectFile.State = "not found"
	}
	rep.Settings = slices.Concat(sessionSettings(in, o, r, cfg), fileSettings(in.Workspace, o.Layers, r, cfg))

	return rep, nil
}

// workspaceIn is the absolute workspace: the flag, the resumed session's,
// or the current directory.
func workspaceIn(in Inputs, o Origins) string {
	ws := first(in.Workspace, o.Resumed.Workspace, o.Dir)
	if !filepath.IsAbs(ws) {
		ws = filepath.Join(o.Dir, ws)
	}

	return filepath.Clean(ws)
}

// sessionSettings are the settings a flag, the environment, or the resumed
// session can set, besides the workspace.
func sessionSettings(in Inputs, o Origins, r Resolved, cfg config.Config) []Setting {
	l, env, resumed := o.Layers, o.Env, o.Resumed
	s := r.Settings
	modelSource := pick(input(in.Model, EnvModel, env), sessionValue(resumed.Model), overrides(l, func(c config.Config) any { return c.Model }), FromDefault)
	if providerChanged(in, resumed, cfg) {
		modelSource = pick(input(in.Model, EnvModel, env), FromDefault)
	}
	maxDisk := in.MaxDisk
	if !in.MaxDiskSet && cfg.MaxDisk != "" {
		maxDisk = cfg.MaxDisk
	}

	return []Setting{
		one("provider", s.Provider, pick(input(in.Provider, EnvProvider, env), sessionValue(resumed.Provider), overrides(l, func(c config.Config) any { return c.Provider }), FromDefault)),
		one("model", s.Model, modelSource),
		one("effort", s.Effort, pick(input(in.Effort, "", nil), sessionValue(resumed.Effort), overrides(l, func(c config.Config) any { return c.Effort }), FromDefault)),
		one("request_max_attempts", s.MaxAttempts, pick(attemptsInput(in, env), overrides(l, func(c config.Config) any { return c.RequestMaxAttempts }), FromDefault)),
		one("max_disk", maxDisk, pick(given(in.MaxDiskSet), overrides(l, func(c config.Config) any { return c.MaxDisk }), FromDefault)),
		{Key: "fast", Value: s.ServiceTier != "", Sources: fastSources(in, o, cfg)},
		one("adaptive_effort", s.AdaptiveEffort, pick(input(in.AdaptiveEffort, EnvAdaptiveEffort, env), sessionValue(resumed.AdaptiveEffort),
			overrides(l, func(c config.Config) any { return c.AdaptiveEffort }), FromDefault)),
		one("context_preparation", r.ContextPreparation, pick(input(in.ContextPreparation, EnvContextPreparation, env),
			overrides(l, func(c config.Config) any { return c.ContextPreparation }), FromDefault)),
		one("tools.experimental_request_user_input.enabled", r.RequestUserInput, pick(input(in.RequestUserInput, EnvRequestUserInput, env),
			overrides(l, func(c config.Config) any { return c.Tools.ExperimentalRequestUserInput.Enabled }), FromDefault)),
		one("model_verbosity", modelVerbosity(o, r), pick(input(in.ModelVerbosity, EnvModelVerbosity, env),
			overrides(l, func(c config.Config) any { return c.ModelVerbosity }), FromDefault)),
		one("permission_mode", string(s.Mode), modeSource(in, o)),
		one("sandbox_mode", string(r.Sandbox.Mode), modeSource(in, o)),
		one("approval_policy", string(r.Approval), pick(input(in.Ask, EnvAsk, env), overrides(l, func(c config.Config) any { return c.ApprovalPolicy }), FromDefault)),
		one("model_context_window", compaction.ContextWindow(s.Model, s.ContextWindow, models.BundledWindow), pick(overrides(l, func(c config.Config) any { return c.ModelContextWindow }), FromDefault)),
		{Key: "instructions.enabled", Value: r.Instructions, Sources: orSources(given(in.NoInstructions), []Source{overrides(l, func(c config.Config) any { return c.Instructions.Enabled })})},
		{Key: "skills.enabled", Value: !r.NoSkills, Sources: orSources(given(in.NoSkills), []Source{overrides(l, func(c config.Config) any { return c.Skills.Enabled })})},
		{Key: "tools.allow", Value: allowValue(r.Tools.Allow), Sources: policySources(l, resumed, in.Tools != nil, func(c config.Config) any { return c.Tools.Allow })},
		{Key: "tools.deny", Value: list(r.Tools.Deny), Sources: policySources(l, resumed, len(in.DenyTools) > 0, func(c config.Config) any { return c.Tools.Deny })},
	}
}

// allowValue is the allowlist, or "every tool" when there is none.
func allowValue(allow []string) any {
	if allow == nil {
		return "every tool"
	}

	return list(allow)
}

// policySources are the sources a tool policy list is narrowed by: every
// file that sets it, the resumed session's policy, and the flag, else the
// default.
func policySources(l config.Layers, resumed session.Info, flag bool, get func(config.Config) any) []Source {
	out := fileSources(l, get)
	if resumed.Tools != nil && resumed.Tools.Restricted() {
		out = append(out, FromSession)
	}
	if flag {
		out = append(out, FromFlag)
	}
	if len(out) == 0 {
		return []Source{FromDefault}
	}

	return out
}

// modelVerbosity is the text.verbosity the session's model gets: the
// configured one or the catalog's default, for a model that supports it
// ("": none).
func modelVerbosity(o Origins, r Resolved) string {
	c := models.Bundled(r.Settings.Provider)
	if o.Catalog != nil {
		c = o.Catalog(r.Settings.Provider)
	}
	v, _ := c.Verbosity(r.Settings.Model, r.Verbosity)

	return v
}

// attemptsInput is the source of --max-attempts: its environment variable
// when the value is the variable's ("" when unset).
func attemptsInput(in Inputs, env map[string]string) Source {
	if in.MaxAttempts == 0 {
		return ""
	}

	return input(strconv.Itoa(in.MaxAttempts), EnvMaxAttempts, env)
}

// fastSources are the fast mode's: the flag, the resumed session, or the
// files, as pickFast decides.
func fastSources(in Inputs, o Origins, cfg config.Config) []Source {
	if !in.FastSet && sessionFast(in, o.Resumed, cfg) {
		return []Source{FromSession}
	}

	return orSources(given(in.FastSet), adds(o.Layers, func(c config.Config) any { return c.Fast }))
}

// modeSource is the permission mode's source, and the sandbox mode's that
// follows from it: --yolo, --sandbox, the resumed session, permission_mode,
// or sandbox_mode, as pickMode decides.
func modeSource(in Inputs, o Origins) Source {
	l := o.Layers
	resumed := o.Resumed.Mode
	if resumed.AsksNoOne() {
		resumed = "" // only --yolo gives yolo
	}

	return pick(given(in.Yolo), input(in.Sandbox, EnvSandbox, o.Env), sessionValue(string(resumed)),
		overrides(l, func(c config.Config) any { return c.PermissionMode }),
		overrides(l, func(c config.Config) any { return c.SandboxMode }), FromDefault)
}

// Text is the value as one line of text.
func (s Setting) Text() string {
	switch v := s.Value.(type) {
	case string:
		if v == "" {
			return `""`
		}

		return v
	case []string:
		return "[" + strings.Join(v, ", ") + "]"
	case map[string]string:
		keys := slices.Sorted(maps.Keys(v))
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+strconv.Quote(v[k]))
		}

		return "{" + strings.Join(parts, ", ") + "}"
	}

	return fmt.Sprint(s.Value)
}

// SourceText is the sources joined with " + ".
func (s Setting) SourceText() string {
	parts := make([]string, 0, len(s.Sources))
	for _, src := range s.Sources {
		parts = append(parts, string(src))
	}

	return strings.Join(parts, " + ")
}

func one(key string, value any, src Source) Setting {
	return Setting{Key: key, Value: value, Sources: []Source{src}}
}

// input is the source of an input value: the environment when the value is
// its variable's, else a flag ("" when unset).
func input(value, envName string, env map[string]string) Source {
	switch {
	case value == "":
		return ""
	case envName != "" && env[envName] == value:
		return FromEnv
	}

	return FromFlag
}

func given(set bool) Source {
	if set {
		return FromFlag
	}

	return ""
}

func sessionValue(v string) Source {
	if v != "" {
		return FromSession
	}

	return ""
}

// pick is the first non-empty source.
func pick(sources ...Source) Source {
	for _, s := range sources {
		if s != "" {
			return s
		}
	}

	return ""
}

// orSources is the flag when given, else the files, else the default.
func orSources(flag Source, files []Source) []Source {
	files = slices.DeleteFunc(files, func(s Source) bool { return s == "" })
	switch {
	case flag != "":
		return []Source{flag}
	case len(files) > 0:
		return files
	}

	return []Source{FromDefault}
}

// overrides is the file whose value wins for a key a later file overrides:
// the last of the user file, the layers, and the project file that sets
// it, else "".
func overrides(l config.Layers, get func(config.Config) any) Source {
	files := fileSources(l, get)
	if len(files) == 0 {
		return ""
	}

	return files[len(files)-1]
}

// adds is every file that sets a key whose files add up, in merge order, or
// the default when none does.
func adds(l config.Layers, get func(config.Config) any) []Source {
	if out := fileSources(l, get); len(out) > 0 {
		return out
	}

	return []Source{FromDefault}
}

// fileSources are the files that set get's value, in merge order.
func fileSources(l config.Layers, get func(config.Config) any) []Source {
	var out []Source
	add := func(c config.Config, src Source) {
		if s := setIn(c, get, src); s != "" {
			out = append(out, s)
		}
	}
	add(l.User, FromUser)
	for _, x := range l.Extra {
		add(x.Config, Source(x.Name))
	}
	add(l.Project, FromProject)

	return out
}

// setIn is src when get's value in c is not the zero value.
func setIn(c config.Config, get func(config.Config) any, src Source) Source {
	v := reflect.ValueOf(get(c))
	if !v.IsValid() || v.IsZero() {
		return ""
	}

	return src
}
