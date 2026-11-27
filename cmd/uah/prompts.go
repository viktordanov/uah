package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/contextprep"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/instructions"
	"github.com/viktordanov/uah/internal/review"
)

// The `uah prompts` command's name, and the name of the subcommands that
// print one thing: `uah prompts show` and `uah sessions show`.
const (
	promptsName = "prompts"
	subShow     = "show"
)

// builtinPrompt is a prompt uah ships and the key that replaces it with a
// file.
type builtinPrompt struct {
	name  string // also the file's base name
	text  func() string
	table string // the key's table, "" for the top level
	key   string
	// alternative, when set, is a prompt uah does not use by default: its
	// key line is printed commented out, after the previous prompt's,
	// under this comment.
	alternative string
}

// keySystemPrompt is the key of the system prompts: uah's default and its
// alternatives.
const keySystemPrompt = "model_instructions_file"

// builtinPrompts are in the order their keys can be written: top-level
// keys before any table.
var builtinPrompts = []builtinPrompt{
	{name: "compact", text: func() string { return compaction.Prompt + "\n" }, key: "experimental_compact_prompt_file"},
	{name: "system", text: func() string { return instructions.DefaultPrompt }, key: keySystemPrompt},
	{
		name: "system-codex", text: func() string { return instructions.CodexPrompt }, key: keySystemPrompt,
		alternative: "Or Codex's own prompt, unmodified (gpt-6.1-sol's; it names Codex's tools, see docs/configuration.md):",
	},
	{
		name: "system-runner", text: func() string { return instructions.RunnerHostPrompt }, key: keySystemPrompt,
		alternative: "Or the runner's short host prompt, uah's default before the Codex-based one:",
	},
	{name: "review", text: review.DefaultPolicy, table: "review", key: "policy_file"},
}

// promptsCommand is `uah prompts`: it writes the built-in prompts into the
// configuration folder as a starting point, prints one, and lists and
// cleans the user's context overrides.
func promptsCommand() *cli.Command {
	configFlag := func() cli.Flag {
		return &cli.StringFlag{Name: flagConfig, Usage: usageConfig + "; the prompts are in its folder", Value: config.UserFile(), Sources: cli.NewValueSourceChain(nonEmptyEnv(home.EnvConfig)), TakesFile: true}
	}

	return &cli.Command{
		Name:  promptsName,
		Usage: "write the built-in prompts into the config folder to customize them, print one, or list and clean your overrides",
		Commands: []*cli.Command{
			{
				Name: "init", Usage: "write compact.md, system.md, system-codex.md, system-runner.md, and review.md into <config dir>/prompts, " +
					"and the context modules into <config dir>/prompts/context.defaults as a reference uah never reads",
				Flags:        []cli.Flag{configFlag(), &cli.BoolFlag{Name: "force", Usage: "overwrite prompt files that exist"}},
				OnUsageError: onUsageError, Action: promptsInit,
			},
			{Name: subShow, Usage: "print a built-in prompt, or a context module (context/<path>)", ArgsUsage: strings.Join(promptNames(), "|") + "|context/<path>", OnUsageError: onUsageError, Action: promptsShow},
			{
				Name: "status", Usage: "list the prompt files, the context overrides in use (and the ones identical to the built-in), and your extra modules",
				Flags: []cli.Flag{configFlag()}, OnUsageError: onUsageError, Action: promptsStatus,
			},
			{
				Name: "prune", Usage: "delete the context overrides that are identical to the built-in, so later versions' text reaches you",
				Flags:        []cli.Flag{configFlag(), &cli.BoolFlag{Name: flagDryRun, Usage: "print what would be deleted, and delete nothing"}},
				OnUsageError: onUsageError, Action: promptsPrune,
			},
		},
	}
}

// promptNames are the built-in prompts' names.
func promptNames() []string {
	names := make([]string, 0, len(builtinPrompts))
	for _, p := range builtinPrompts {
		names = append(names, p.name)
	}

	return names
}

func promptsShow(_ context.Context, cmd *cli.Command) error {
	name := cmd.Args().First()
	if rest, ok := strings.CutPrefix(name, contextprep.ContextDir+"/"); ok && cmd.Args().Len() == 1 {
		text, err := contextprep.BuiltinFile(rest)
		if err != nil {
			return cli.Exit(fmt.Sprintf("no context module %s; `uah context` lists them", rest), exitUsage)
		}
		fmt.Print(text)

		return nil
	}
	for _, p := range builtinPrompts {
		if cmd.Args().Len() == 1 && p.name == name {
			fmt.Print(p.text())

			return nil
		}
	}

	return cli.Exit("expected one of: "+strings.Join(promptNames(), ", ")+", or context/<module path>", exitUsage)
}

func promptsInit(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() > 0 {
		return cli.Exit("prompts init takes no arguments (see --help)", exitUsage)
	}
	configFile := cmd.String(flagConfig)
	dir := promptsDir(configFile)
	if !cmd.Bool("force") {
		for _, p := range builtinPrompts {
			path := filepath.Join(dir, p.name+".md")
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				return cli.Exit(path+" exists; use --force to overwrite it", exitUsage)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	var lines, names []string
	for _, p := range builtinPrompts {
		path := filepath.Join(dir, p.name+".md")
		if err := os.WriteFile(path, []byte(p.text()), 0o600); err != nil {
			return fmt.Errorf("failed to write %s: %w", path, err)
		}
		names = append(names, p.name+".md")
		line := fmt.Sprintf("%s = %q", p.key, homePath(path))
		switch {
		case p.alternative != "":
			lines[len(lines)-1] += "\n# " + p.alternative + "\n# " + line
		case p.table != "":
			lines = append(lines, "["+p.table+"]\n"+line)
		default:
			lines = append(lines, line)
		}
	}
	n, err := writeContextDefaults(dir)
	if err != nil {
		return err
	}
	defaults, live, extras := filepath.Join(dir, contextprep.DefaultsDir), filepath.Join(dir, contextprep.ContextDir), filepath.Join(dir, contextprep.ExtrasDir)
	fmt.Printf("Wrote the prompts to %s: %s.\n", homePath(dir), strings.Join(names, ", "))
	fmt.Printf("A prompt file takes effect only when the configuration names it. To use them, add to %s:\n\n%s\n\n", homePath(configFile), strings.Join(lines, "\n\n"))
	fmt.Printf("Wrote %d context modules to %s as a reference. uah never reads that folder:\n"+
		"the built-in modules stay in use, and later versions' text reaches you. To change a module,\n"+
		"copy its file to the same path under %s and edit the copy, for example:\n\n"+
		"  mkdir -p %s\n  cp %s %s\n\n",
		n, homePath(defaults), homePath(live),
		homePath(filepath.Join(live, "environment")),
		homePath(filepath.Join(defaults, "environment", "fish.md")), homePath(filepath.Join(live, "environment", "fish.md")))
	fmt.Printf("Warning: a file under %s replaces the built-in module of the same path for as long as it\n"+
		"exists, so later versions' changes to that module do not reach you. Copy only the modules you change.\n"+
		"Modules of your own go in %s.\n\n", homePath(live), homePath(extras))
	fmt.Printf("Context overrides in use: %s.\n\n", overridesSummary(contextprep.Overrides(dir)))
	fmt.Printf("To undo: delete the prompt files and the configuration lines above, and %s.\n"+
		"`uah prompts status` lists what is in use, `uah prompts prune` deletes overrides identical to the built-in,\n"+
		"and `uah context` shows which modules apply in a workspace.\n", homePath(defaults))

	return nil
}

// promptsDir is the prompts folder next to the configuration file.
func promptsDir(configFile string) string { return filepath.Join(filepath.Dir(configFile), "prompts") }

// overridesSummary counts the context overrides: the ones in use, and
// among them the copies identical to the built-in, and the ones not used.
func overridesSummary(overrides []contextprep.Override) string {
	if len(overrides) == 0 {
		return "none"
	}
	var pinned, broken int
	for _, o := range overrides {
		switch {
		case o.Err != nil:
			broken++
		case o.Pinned:
			pinned++
		}
	}
	out := strconv.Itoa(len(overrides) - broken)
	if pinned > 0 {
		out += fmt.Sprintf(", %d of them identical to the built-in (`uah prompts prune` deletes those)", pinned)
	}
	if broken > 0 {
		out += fmt.Sprintf("; %d more not used because of an error (`uah prompts status` says which)", broken)
	}

	return out
}

// contextDefaultFile is where `uah prompts init` writes a built-in
// module's reference copy.
func contextDefaultFile(dir, path string) string {
	return filepath.Join(dir, contextprep.DefaultsDir, filepath.FromSlash(path)+".md")
}

// writeContextDefaults writes the built-in context modules under
// dir/context.defaults, as uah ships them, over the copies there.
func writeContextDefaults(dir string) (int, error) {
	for _, m := range contextprep.Builtins() {
		path := contextDefaultFile(dir, m.Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return 0, fmt.Errorf("failed to create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(m.Raw), 0o600); err != nil {
			return 0, fmt.Errorf("failed to write %s: %w", path, err)
		}
	}

	return len(contextprep.Builtins()), nil
}

// promptsStatus is `uah prompts status`: the prompt files, the context
// overrides, and the user's extra modules.
func promptsStatus(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() > 0 {
		return cli.Exit("prompts status takes no arguments (see --help)", exitUsage)
	}
	dir := promptsDir(cmd.String(flagConfig))
	w := os.Stdout
	fmt.Fprintf(w, "Prompt files in %s (each used only when the configuration names it; `uah config` shows the keys):\n", homePath(dir))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, p := range builtinPrompts {
		fmt.Fprintf(tw, "  %s.md\t%s\n", p.name, promptState(filepath.Join(dir, p.name+".md"), p.text()))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	live := filepath.Join(dir, contextprep.ContextDir)
	overrides := contextprep.Overrides(dir)
	fmt.Fprintf(w, "\nContext overrides in %s (each replaces the built-in module of its path):", homePath(live))
	if len(overrides) == 0 {
		fmt.Fprintln(w, " none")
	} else {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, o := range overrides {
			fmt.Fprintf(tw, "  %s\t%s\n", o.Path, overrideState(o))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	extras := filepath.Join(dir, contextprep.ExtrasDir)
	fmt.Fprintf(w, "\nYour modules in %s:", homePath(extras))
	var mine []*contextprep.Module
	for _, m := range contextprep.Load(contextprep.Sources{UserDir: dir}).All() {
		if m.Source == contextprep.SourceUser && !m.Overrides {
			mine = append(mine, m)
		}
	}
	if len(mine) == 0 {
		fmt.Fprintln(w, " none")
	} else {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, m := range mine {
			fmt.Fprintf(tw, "  %s\t%s\n", strings.TrimPrefix(m.Path, contextprep.ExtrasDir+"/"), extraState(m))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "\nReference copies of the built-in modules: %s (never read; `uah prompts init` rewrites them).\n"+
		"`uah context` shows which modules apply in a workspace and why.\n", homePath(filepath.Join(dir, contextprep.DefaultsDir)))

	return nil
}

// promptState says whether a prompt file exists and differs from the
// built-in.
func promptState(path, builtin string) string {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "absent"
	case err != nil:
		return "unreadable: " + err.Error()
	case string(data) == builtin:
		return "identical to the built-in; a key that names it keeps this version's text"
	}

	return "edited"
}

// overrideState is a context override's state in `uah prompts status`.
func overrideState(o contextprep.Override) string {
	switch {
	case o.Err != nil:
		return "not used, the built-in stays: " + oneLine(o.Err.Error(), 120)
	case o.Pinned:
		return "pinned copy: identical to the built-in, so no effect today, but it stops later versions' text of this module; delete it (`uah prompts prune`)"
	}

	return "edited: in use"
}

// extraState is a user's extra module's state in `uah prompts status`.
func extraState(m *contextprep.Module) string {
	switch {
	case m.Err != nil:
		return "error: " + oneLine(m.Err.Error(), 120)
	case m.Meta.Enabled != nil && !*m.Meta.Enabled:
		return "enabled: false, used where [context] modules names " + m.Meta.ID
	}

	return "on"
}

// promptsPrune is `uah prompts prune`: it deletes the context overrides
// identical to the built-in, and the folders that leaves empty.
func promptsPrune(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() > 0 {
		return cli.Exit("prompts prune takes no arguments (see --help)", exitUsage)
	}
	dir := promptsDir(cmd.String(flagConfig))
	dry := cmd.Bool(flagDryRun)
	var deleted, kept int
	for _, o := range contextprep.Overrides(dir) {
		if !o.Pinned {
			kept++

			continue
		}
		deleted++
		if dry {
			fmt.Printf("would delete %s\n", homePath(o.File))

			continue
		}
		if err := os.Remove(o.File); err != nil {
			return fmt.Errorf("failed to delete %s: %w", o.File, err)
		}
		fmt.Printf("deleted %s\n", homePath(o.File))
		removeEmptyDirs(filepath.Dir(o.File), filepath.Join(dir, contextprep.ContextDir))
	}
	switch {
	case deleted == 0:
		fmt.Printf("No context override is identical to the built-in; %d kept.\n", kept)
	case dry:
		fmt.Printf("%d override(s) identical to the built-in would be deleted; %d kept.\n", deleted, kept)
	default:
		fmt.Printf("Deleted %d override(s) identical to the built-in; %d kept. The built-ins of those paths are used again.\n", deleted, kept)
	}

	return nil
}

// removeEmptyDirs removes dir and its parents while they are empty,
// stopping at root, which stays.
func removeEmptyDirs(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if os.Remove(dir) != nil { // not empty
			return
		}
		dir = filepath.Dir(dir)
	}
}

// homePath writes a path under the home directory as ~/..., which the
// prompt keys accept.
func homePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}

	return path
}
