package state

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/viktordanov/uah/internal/session"
)

// Command is one slash command.
type Command struct {
	Name    string
	Aliases []string
	Args    string
	Help    string
	// WhileBusy allows the command while a run is live.
	WhileBusy bool
	// Bare runs the command when enter picks it from the menu with no
	// argument typed, as /model does to open its picker; tab still adds
	// the space for one.
	Bare bool
	run  func(s *State, args string) []Effect
}

// Commands lists every slash command in the order /help shows them.
func Commands() []Command {
	return []Command{
		{Name: "model", Args: "[id] [effort]", Help: "choose the model, then its effort; /model <id> <effort> sets both at once", WhileBusy: true, Bare: true, run: cmdModel},
		{Name: "effort", Args: "<level>", Help: "set the thinking level: " + strings.Join(session.Efforts, ", "), WhileBusy: true, run: cmdEffort},
		{Name: "fast", Help: "priority processing (needs the embedded engine)", WhileBusy: true, run: cmdFast},
		{Name: "adaptive", Args: "[off|1-step|2-steps]", Help: "adaptive effort: think one or two effort levels less on follow-up turns after tool results; alone, the next value", WhileBusy: true, run: cmdAdaptive},
		{Name: "resume", Args: "[id]", Help: "open the session picker, or resume a session by ID prefix", run: cmdResume},
		{Name: "new", Help: "start a new session", run: func(*State, string) []Effect { return []Effect{EffOpenSession{}} }},
		{Name: "stop", Help: "interrupt the live run; queued messages stay", WhileBusy: true, run: func(s *State, _ string) []Effect { return s.interrupt() }},
		{Name: "clear", Help: "start the agent fresh in this session; the session keeps its history (embedded engine)", WhileBusy: true, run: cmdClear},
		{Name: "search", Args: "[text]", Help: "find text in your messages and the agent's answers; ↑↓ or j k move between matches, esc goes back to editing a message (/ while going back)", WhileBusy: true, Bare: true, run: cmdSearch},
		{Name: "rewind", Help: "go back to an earlier message and edit it; what followed leaves the context (esc esc; embedded engine)", run: cmdRewind},
		{Name: "goal", Args: "[objective|edit|pause|resume|clear|status]", Help: "set or view the goal of a long task: the agent keeps working, turn after turn, until it marks the goal complete or a budget stops it", WhileBusy: true, Bare: true, run: cmdGoal},
		{Name: "compact", Args: "[focus]", Help: "summarize the context to free it; your messages stay as written, and words after it steer the summary (embedded engine)", WhileBusy: true, run: cmdCompact},
		{Name: "diff", Help: "the workspace's git changes, staged, unstaged, and untracked; not sent to the agent", WhileBusy: true, run: cmdDiff},
		{Name: cmdReviewName, Args: "[target]", Help: "a read-only reviewer looks at your changes (uncommitted, branch <name>, commit <sha>, or instructions) and lists findings", run: cmdReview},
		{Name: "context", Help: "what fills the context window: prompt, instructions, skills, tools, messages", WhileBusy: true, run: cmdContext},
		{Name: "config", Help: "settings: auto-compact, compaction model, model, effort, fast mode, adaptive effort, permission mode, web search, details, mouse; saved to the user file", WhileBusy: true, run: cmdConfig},
		{Name: "status", Help: "session, settings, totals, prompt cache, and your plan's usage", WhileBusy: true, run: cmdStatus},
		{Name: "usage", Help: "your plan's usage: each limit, what is left, and when it resets (openai-codex); this session's prompt cache", WhileBusy: true, run: cmdUsage},
		{Name: "mcp", Args: "[verbose]", Help: "MCP servers: state, transport, and tool count; verbose adds auth and each tool", WhileBusy: true, run: cmdMCP},
		{Name: cmdAgentsName, Args: "[name]", Help: "subagents the agent started, and their state; a name shows that agent's transcript as it works", WhileBusy: true, run: cmdAgents},
		{Name: "sandbox", Help: "what commands may do: the permission mode and its sandbox (shift+tab changes it)", WhileBusy: true, run: cmdSandbox},
		{Name: "reasoning", Help: "show or hide reasoning summaries", WhileBusy: true, run: func(s *State, _ string) []Effect { s.ShowReasoning = !s.ShowReasoning; return nil }},
		{Name: "details", Help: "show or hide turns, run dividers, and token totals", WhileBusy: true, run: func(s *State, _ string) []Effect { s.Details = !s.Details; return nil }},
		{Name: "help", Help: "commands and keys", WhileBusy: true, run: cmdHelp},
		{Name: "quit", Aliases: []string{"exit"}, Help: "close the session and exit", WhileBusy: true, run: func(s *State, _ string) []Effect {
			s.Quitting, s.Status = true, "stopping…"
			return []Effect{EffQuit{}}
		}},
	}
}

// FindCommand returns the command named name or aliased to it.
func FindCommand(name string) (Command, bool) {
	for _, c := range Commands() {
		if c.Name == name || slices.Contains(c.Aliases, name) {
			return c, true
		}
	}

	return Command{}, false
}

// Complete returns the commands whose name starts with prefix (without "/").
func Complete(prefix string) []Command {
	var out []Command
	for _, c := range Commands() {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}

	return out
}

// commandLine splits a slash command into its name and arguments. ok is
// false when text is a message: it does not start with "/", or its first
// word holds more than letters, digits, "-" and "_", as a pasted path such
// as /Users/me/a.json does.
func commandLine(text string) (name, args string, ok bool) {
	rest, found := strings.CutPrefix(text, "/")
	name, args, _ = strings.Cut(rest, " ")
	if i := strings.IndexAny(name, "\n\t"); i >= 0 {
		name, args = name[:i], rest[i:]
	}
	ok = found && name != "" && strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == ""

	return name, strings.TrimSpace(args), ok
}

func isCommand(text string) bool { _, _, ok := commandLine(text); return ok }

func (s *State) command(text string) (State, []Effect) {
	name, args, _ := commandLine(text)
	cmd, ok := FindCommand(name)
	if p, isPrompt := s.findPrompt(name); !ok && isPrompt {
		return *s, s.runPrompt(p, args)
	}
	if !ok {
		s.notice(session.LevelError, fmt.Sprintf("unknown command /%s (see /help)", name))

		return *s, nil
	}
	if s.Busy && !cmd.WhileBusy {
		s.notice(session.LevelWarning, fmt.Sprintf("/%s waits until the agent is idle; press esc twice to interrupt it", cmd.Name))

		return *s, nil
	}

	return *s, cmd.run(s, args)
}

func cmdEffort(s *State, args string) []Effect {
	if !slices.Contains(session.Efforts, args) {
		s.notice(session.LevelInfo, fmt.Sprintf("effort: %s (set it with /effort %s)", s.Settings.Effort, strings.Join(session.Efforts, "|")))

		return nil
	}
	if err := s.checkEffort(args); err != nil {
		s.notice(session.LevelError, err.Error())

		return nil
	}
	next := s.Settings
	next.Effort = args

	return []Effect{EffSetSettings{Settings: next}}
}

func cmdFast(s *State, _ string) []Effect {
	if !s.Priority {
		s.notice(session.LevelWarning, "/fast needs the openai or openai-codex provider")

		return nil
	}
	next := s.Settings
	if next.ServiceTier == "" {
		next.ServiceTier = "priority"
	} else {
		next.ServiceTier = ""
	}

	return []Effect{EffSetSettings{Settings: next}}
}

// cmdAdaptive sets adaptive effort, or steps to the next value.
func cmdAdaptive(s *State, args string) []Effect {
	value := adaptiveValue(args)
	if value == "" {
		value = cycle(session.AdaptiveEfforts, adaptiveText(s.Settings), 1)
	}
	if !slices.Contains(session.AdaptiveEfforts, value) {
		s.notice(session.LevelInfo, fmt.Sprintf("adaptive effort: %s (set it with /adaptive %s)", adaptiveText(s.Settings), strings.Join(session.AdaptiveEfforts, "|")))

		return nil
	}
	next := s.Settings
	next.AdaptiveEffort = value

	return []Effect{EffSetSettings{Settings: next}}
}

// adaptiveValue reads /adaptive's argument: a value, or a short form of
// one ("1", "one", "2", "two", "0", "no"); anything else as typed.
func adaptiveValue(arg string) string {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "0", "no", "zero":
		return session.AdaptiveOff
	case "1", "one", "1step", "1 step", "1-steps":
		return session.AdaptiveOneStep
	case "2", "two", "2step", "2 steps", "2-step":
		return session.AdaptiveTwoSteps
	}

	return strings.TrimSpace(arg)
}

// adaptiveText is the session's adaptive effort, "off" when unset.
func adaptiveText(s session.Settings) string { return cmp.Or(s.AdaptiveEffort, session.AdaptiveOff) }

// AdaptiveLabel says the session's adaptive effort in words: "off", or
// "2 steps (follow-ups at low)".
func AdaptiveLabel(s session.Settings) string {
	if session.AdaptiveSteps(s.AdaptiveEffort) == 0 {
		return session.AdaptiveOff
	}

	return fmt.Sprintf("%s (follow-ups at %s)", strings.Replace(s.AdaptiveEffort, "-", " ", 1), session.FollowUpEffort(s.Effort, s.AdaptiveEffort))
}

func cmdResume(s *State, args string) []Effect {
	if args == "" {
		return []Effect{EffLoadSessions{}}
	}
	for _, info := range s.Picker.Sessions { // IDs are global, as in Codex
		if strings.HasPrefix(info.ID, args) {
			return []Effect{EffOpenSession{ID: info.ID}}
		}
	}

	return []Effect{EffOpenSession{ID: args}}
}

func cmdStatus(s *State, _ string) []Effect {
	t := s.Totals
	files := "none"
	if len(s.Files) > 0 {
		files = strings.Join(s.Files, ", ")
	}
	s.notice(session.LevelInfo, fmt.Sprintf("session %s · %s engine · %s/%s · effort %s · adaptive effort %s · %s mode · %s",
		s.SessionID, s.Engine, s.Settings.Provider, s.Settings.Model, s.Settings.Effort, adaptiveText(s.Settings), s.Settings.Mode.Label(), s.Settings.Workspace))
	if s.Settings.Mode.AsksNoOne() {
		s.notice(session.LevelWarning, "yolo mode (--yolo): no sandbox, and nothing asks before a command, a patch, or an MCP tool runs; only forbid rules refuse")
	}
	s.notice(session.LevelInfo, fmt.Sprintf("%d runs · %d turns · %d tool calls (max %d parallel) · %d in / %d out tokens · tools overlapped the model %s", t.Runs, t.Turns, t.ToolCalls, t.MaxParallel, t.Tokens.InputTokens, t.Tokens.OutputTokens, t.Overlap.Round(100_000_000)))
	s.notice(session.LevelInfo, "instructions: "+files)
	if line := s.goalStatusLine(); line != "" {
		s.notice(session.LevelInfo, line)
	}
	if w, ok := s.CurrentWait(); ok {
		s.notice(session.LevelInfo, joinDetail("now: "+w.Text(), s.Live.Progress.Phase))
	}

	return append([]Effect{EffLoadActivity{}, EffLoadUsage{Reason: UsageStatus}}, s.loadCache()...)
}

func cmdHelp(s *State, _ string) []Effect {
	var b strings.Builder
	for _, c := range Commands() {
		name := "/" + c.Name
		if c.Args != "" {
			name += " " + c.Args
		}
		fmt.Fprintf(&b, "%-18s %s\n", name, c.Help)
	}
	for _, p := range s.Menu.Prompts {
		name := "/" + p.Command
		if usage := p.Usage(); usage != "" {
			name += " " + usage
		}
		fmt.Fprintf(&b, "%-18s %s (MCP prompt)\n", name, p.Description)
	}
	b.WriteString("\n" + s.Keys.sendHelp() + "\n")
	b.WriteString("esc esc interrupt, or while idle on an empty prompt go back to an earlier message (↑/k earlier, ↓/j later, / search, enter edit, esc cancels) · ↑ edit the last queued message, else earlier prompts (↓ later) · ctrl+r search earlier prompts · shift+tab permission mode · alt+, alt+. effort · alt+e adaptive effort · ctrl+s sessions · ctrl+n new · ctrl+g edit the prompt in $VISUAL or $EDITOR · ctrl+t details · wheel, shift+↑↓, pgup/pgdn scroll (end: bottom) · drag, double or triple click select and copy · ctrl+c ctrl+c quit")
	s.notice(session.LevelInfo, b.String())

	return nil
}

func cmdSandbox(s *State, _ string) []Effect {
	var text string
	switch s.Settings.Sandbox {
	case "read-only":
		text = "sandbox read-only: commands can read files but write nothing, without network"
	case "workspace-write":
		text = "sandbox workspace-write: commands can read any file, write the workspace and temporary directories " +
			"(.git, .uah, .agents, and .codex stay read-only), without network"
	case "danger-full-access", "":
		text = "no sandbox: commands can do anything your user can, and nothing asks first"
	default:
		text = "sandbox " + s.Settings.Sandbox
	}
	cycle := "read only, workspace, and auto; yolo needs --yolo"
	if s.Yolo {
		cycle = "read only, workspace, auto, and yolo"
	}
	s.notice(session.LevelInfo, s.Settings.Mode.Label()+" mode, "+text+". shift+tab cycles "+cycle+".")

	return nil
}
