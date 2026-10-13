package state

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/viktordanov/uah/internal/cmdparse"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
)

// Background shells (the [design](../../../docs/design/background-shells.md)):
// the agent starts one with Bash's background argument, and the session's
// engine.ShellUpdated events keep State.shells. /ps lists them and stops
// them; ctrl+c's quit stops them all, so it asks twice while one runs.

// EffStopShells stops the session's background shells by ID.
type EffStopShells struct{ IDs []string }

func (EffStopShells) effect() {}

const (
	cmdPsName = "ps"
	psStop    = "stop"
)

// The background shell tools, and the label of a Bash call that starts a
// shell.
const (
	toolWaitShell  = "wait_shell"
	toolKillShell  = "kill_shell"
	verbBackground = "BG"
)

// background reports whether a Bash call's arguments start a background
// shell.
func background(arguments string) bool {
	var args struct {
		Background bool `json:"background"`
	}

	return json.Unmarshal([]byte(arguments), &args) == nil && args.Background
}

// shellCallParts are a wait_shell's or kill_shell's line: WAIT shell-1,
// shell-2 or KILL shell-1, not its JSON.
func shellCallParts(name, arguments string) (string, []cmdparse.Part) {
	var args struct {
		IDs []string `json:"shell_ids"`
		ID  string   `json:"shell_id"`
	}
	_ = json.Unmarshal([]byte(arguments), &args)
	if name == toolKillShell {
		return "KILL", []cmdparse.Part{{Text: args.ID}}
	}

	return "WAIT", []cmdparse.Part{{Text: strings.Join(args.IDs, ", ")}}
}

// onShellUpdated keeps the shell's latest update, puts its ID on the line
// of the Bash call that started it, and says in the transcript when one
// ends.
func (s *State) onShellUpdated(e engine.ShellUpdated) {
	i := slices.IndexFunc(s.shells, func(u engine.ShellUpdated) bool { return u.ID == e.ID })
	s.shells = slices.Clone(s.shells)
	if i < 0 {
		s.shells = append(s.shells, e)
		if e.CallID != "" {
			s.update("call:"+e.CallID, func(it *Item) {
				it.Parts = append([]cmdparse.Part{{Text: e.ID + " ", Style: cmdparse.Dim}}, it.Parts...)
			})
		}

		return
	}
	was := s.shells[i].State
	if was != engine.ShellRunning && e.State == engine.ShellRunning {
		return // an end is final; a running update that arrives late is stale
	}
	s.shells[i] = e
	if was == engine.ShellRunning && e.State != engine.ShellRunning {
		s.notice(session.LevelInfo, shellEnded(e))
	}
}

// shellEnded says how a shell ended: "background shell shell-1 exited 0
// after 3m12s: npm run build".
func shellEnded(e engine.ShellUpdated) string {
	how := fmt.Sprintf("exited %d", e.ExitCode)
	if e.State == engine.ShellKilled {
		how = "was stopped"
	}

	return fmt.Sprintf("background shell %s %s after %s: %s", e.ID, how, e.Ended.Sub(e.Started).Round(time.Second), shortCommand(e.Command))
}

// RunningShells counts the session's background shells that run.
func (s State) RunningShells() int {
	n := 0
	for _, u := range s.shells {
		if u.State == engine.ShellRunning {
			n++
		}
	}

	return n
}

// cmdPs lists the session's background shells, or stops them: /ps stop
// <id>, or /ps stop all.
func cmdPs(s *State, args string) []Effect {
	word, rest, _ := strings.Cut(strings.TrimSpace(args), " ")
	rest = strings.TrimSpace(rest)
	switch {
	case word == "":
		listShells(s)

		return nil
	case word == psStop && rest == "all":
		// Every shell: one that ended may have left children running in
		// its process group, which the engine stops too.
		ids := make([]string, 0, len(s.shells))
		for _, u := range s.shells {
			ids = append(ids, u.ID)
		}
		if len(ids) == 0 {
			s.notice(session.LevelInfo, "no background shells in this session")

			return nil
		}

		return []Effect{EffStopShells{IDs: ids}}
	case word == psStop && rest != "":
		return []Effect{EffStopShells{IDs: strings.Fields(rest)}}
	}
	s.notice(session.LevelWarning, "usage: /ps, /ps stop <id>, or /ps stop all")

	return nil
}

// listShells lists the session's background shells, running ones first.
func listShells(s *State) {
	if len(s.shells) == 0 {
		s.notice(session.LevelInfo, "no background shells in this session; the agent starts one with Bash's background argument")

		return
	}
	var b strings.Builder
	for _, u := range s.shells {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch u.State {
		case engine.ShellRunning:
			fmt.Fprintf(&b, "%s · running %s · process group %d · %s", u.ID, s.Now.Sub(u.Started).Round(time.Second), u.Group, shortCommand(u.Command))
		case engine.ShellKilled:
			fmt.Fprintf(&b, "%s · stopped · %s", u.ID, shortCommand(u.Command))
		default:
			fmt.Fprintf(&b, "%s · exited %d · %s", u.ID, u.ExitCode, shortCommand(u.Command))
		}
		fmt.Fprintf(&b, "\n  log: %s", u.Log)
	}
	b.WriteString("\n/ps stop <id> or /ps stop all stops them; quitting stops them all")
	s.notice(session.LevelInfo, b.String())
}

// shellNote reads a <background_shell_notification>, which a shell's end
// sends the agent, as a line of the transcript.
func shellNote(text string) (string, bool) {
	end, ok := engine.ParseShellNotification(text)
	if !ok {
		return "", false
	}

	return fmt.Sprintf("the agent was told that background shell %s %s", end.ID, end.State), true
}

// shortCommand is a command on one line, cut to fit a list.
func shortCommand(command string) string {
	text := oneLine(command)
	if r := []rune(text); len(r) > 80 {
		text = string(r[:79]) + "…"
	}

	return text
}

// quitHint asks for a second ctrl+c, naming what quitting stops.
func quitHint(running bool, shells int) string {
	var what []string
	if running {
		what = append(what, "the run")
	}
	switch shells {
	case 0:
	case 1:
		what = append(what, "1 background shell")
	default:
		what = append(what, fmt.Sprintf("%d background shells", shells))
	}

	return "press ctrl+c again to stop " + strings.Join(what, " and ") + " and quit"
}

// onBridgeCalled shows a call a command made with `uah tool call` as an
// MCP call's line, marked as a script's: "MCP docs · search  q retry ·
// uah tool".
func (s *State) onBridgeCalled(e engine.ToolBridgeCalled) {
	state := ToolOK
	if !e.OK {
		state = ToolFailed
	}
	parts := append(mcpParts(e.Name, e.Arguments), cmdparse.Part{Text: " · uah tool", Style: cmdparse.Dim})
	s.put(Item{
		Kind: KindTool, Key: s.nextKey("bridge"), Name: e.Name, Verb: "MCP", Parts: parts,
		Tool: state, Started: e.At.Add(-e.Duration), Duration: e.Duration, ErrorLine: oneLine(e.Error),
	})
}
