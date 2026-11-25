package embedded

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	uahsession "github.com/viktordanov/uah/internal/session"
)

// advisorChoice is what /advisor set for a session and its subagents: off,
// or a model and effort in place of the configured backend.
type advisorChoice struct {
	set    bool
	off    bool
	model  string
	effort string
}

// advisorSetup is a session's advisor: the model backend's model and
// effort ("": the session's), or the command backend's command.
type advisorSetup struct {
	model, effort string
	command       []string
}

// treeRoot is the session a subagent's tree starts from, which its cache
// key names (internal/agents keys every agent of a tree by its root); a
// main session is its own.
func (e *Engine) treeRoot(sessionID string) string { return cmp.Or(e.cacheKey(sessionID), sessionID) }

// promptCacheKey is the session's prompt cache key, as its turn requests
// send it.
func (e *Engine) promptCacheKey(sessionID string) string {
	return cmp.Or(e.cacheKey(sessionID), sessionID)
}

func (e *Engine) advisorChoice(sessionID string) advisorChoice {
	v, _ := e.advisors.Load(e.treeRoot(sessionID))
	c, _ := v.(advisorChoice)

	return c
}

// advisorFor is the session's advisor now: the configured one, with
// /advisor's choice for its tree, whose model replaces a configured
// command. ok is false when it is off: none is set, the model backend's
// provider does not run it, UAH_ADVISOR=off, or /advisor turned it off.
// The command backend runs on any provider.
func (e *Engine) advisorFor(provider, sessionID string) (advisorSetup, bool) {
	setup, ok := e.advisorWanted(sessionID)
	if !ok {
		return advisorSetup{}, false
	}
	if setup.model != "" && !e.advisorProvider(provider) {
		return advisorSetup{}, false
	}

	return setup, true
}

// advisorWanted is the session's advisor before the provider check: ok
// is false when none is set, UAH_ADVISOR=off, or /advisor turned it off.
func (e *Engine) advisorWanted(sessionID string) (advisorSetup, bool) {
	cfg := e.cfg.Advisor
	if cfg.Off {
		return advisorSetup{}, false
	}
	setup := advisorSetup{model: cfg.Model, effort: cfg.Effort, command: cfg.Command}
	if c := e.advisorChoice(sessionID); c.set {
		if c.off {
			return advisorSetup{}, false
		}
		if c.model != "" {
			setup = advisorSetup{model: c.model, effort: cmp.Or(c.effort, cfg.Effort)}
		}
	}

	return setup, setup.model != "" || len(setup.command) > 0
}

// advisorOffered reports whether a run of the session offers the advisor,
// before its scope and the request leave it out: it is on, and the tool
// policy allows it.
func (e *Engine) advisorOffered(provider, sessionID string) bool {
	_, on := e.advisorFor(provider, sessionID)

	return on && e.cfg.Tools.Allows(toolAdvisor)
}

// advisorProvider reports whether the provider runs the model backend.
func (e *Engine) advisorProvider(name string) bool {
	p, err := e.provider(name)

	return err == nil && p.Advisor
}

// Advisor is /advisor (engine.Advisor) for a session on provider: ""
// shows its advisor, "off" turns it off for the session and its
// subagents, "on" back on, "command" goes back to the configured command
// after "<model> [effort]", which picks the model backend's model (on a
// provider that runs it, openai-codex) in place of the configured
// backend. A change reaches the next call; a run offers the tool or not
// as it starts.
func (e *Engine) Advisor(sessionID, provider, arg string) (string, error) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return e.advisorStatus(provider, sessionID), nil
	}
	c := e.advisorChoice(sessionID)
	keyword := fields[0] == "off" || fields[0] == "on" || fields[0] == "command"
	switch {
	case len(fields) > 2 || len(fields) == 2 && keyword:
		return "", errors.New("usage: /advisor [off|on|command|<model> [effort]]")
	case fields[0] == "off":
		c.set, c.off = true, true
		e.advisors.Store(e.treeRoot(sessionID), c)

		return e.advisorStatus(provider, sessionID), nil
	case e.cfg.Advisor.Off:
		return "", errors.New("the advisor is off for this process (UAH_ADVISOR=off)")
	case !e.cfg.Tools.Allows(toolAdvisor):
		return "", errors.New("the tool policy does not allow the advisor")
	case fields[0] == "on":
		if err := e.advisorCanTurnOn(c, provider); err != nil {
			return "", err
		}
		c.set, c.off = true, false
	case fields[0] == "command":
		if len(e.cfg.Advisor.Command) == 0 {
			return "", errors.New(cmp.Or(e.cfg.Advisor.Refused, "no advisor command is set ([advisor] command)"))
		}
		c = advisorChoice{set: true}
	default:
		var err error
		if c, err = e.advisorModelChoice(provider, fields); err != nil {
			return "", err
		}
	}
	e.advisors.Store(e.treeRoot(sessionID), c)

	return e.advisorStatus(provider, sessionID) + "; a run keeps the tools it started with", nil
}

// advisorCanTurnOn says why /advisor on cannot turn on the advisor the
// choice c and the configuration leave, on provider (nil: it can).
func (e *Engine) advisorCanTurnOn(c advisorChoice, provider string) error {
	cfg := e.cfg.Advisor
	switch {
	case c.model == "" && cfg.Model == "" && len(cfg.Command) == 0 && cfg.Refused != "":
		return errors.New("the advisor command is off: " + cfg.Refused)
	case c.model == "" && cfg.Model == "" && len(cfg.Command) == 0:
		return errors.New("no advisor is set: [advisor] model or command sets one, and /advisor <model> picks a model")
	case (c.model != "" || len(cfg.Command) == 0) && !e.advisorProvider(provider):
		return errModelBackendProvider(provider)
	}

	return nil
}

// advisorModelChoice is /advisor <model> [effort]'s choice, on a
// provider that runs the model backend.
func (e *Engine) advisorModelChoice(provider string, fields []string) (advisorChoice, error) {
	if !e.advisorProvider(provider) {
		return advisorChoice{}, errModelBackendProvider(provider)
	}
	c := advisorChoice{set: true, model: fields[0]}
	if len(fields) == 2 {
		if !slices.Contains(uahsession.Efforts, fields[1]) {
			return advisorChoice{}, fmt.Errorf("unknown effort %q (want one of %s)", fields[1], strings.Join(uahsession.Efforts, ", "))
		}
		c.effort = fields[1]
	}

	return c, nil
}

func errModelBackendProvider(provider string) error {
	return fmt.Errorf("the advisor's model backend needs the openai-codex provider; this session's is %s", provider)
}

// advisorStatus says what the session's advisor is, and when it is off,
// the reason that keeps it off.
func (e *Engine) advisorStatus(provider, sessionID string) string {
	cfg := e.cfg.Advisor
	setup, on := e.advisorFor(provider, sessionID)
	wanted, _ := e.advisorWanted(sessionID)
	c := e.advisorChoice(sessionID)
	limits := fmt.Sprintf("a call waits at most %s, and a run makes at most %d", shortDuration(cfg.timeout()), cfg.maxUses())
	switch {
	case cfg.Off:
		return "advisor: off (UAH_ADVISOR=off)"
	case !e.cfg.Tools.Allows(toolAdvisor):
		return "advisor: off (the tool policy does not allow it)"
	case on && len(setup.command) > 0:
		return fmt.Sprintf("advisor: the command %s, which reads the transcript as %s; %s", setup.command[0], cfg.input(), limits)
	case on:
		return fmt.Sprintf("advisor: %s at %s; %s", setup.model, cmp.Or(setup.effort, "the session's effort"), limits)
	case c.off && (c.model != "" || cfg.Model != "" || len(cfg.Command) > 0):
		return "advisor: off for this session (/advisor on turns it back on)"
	case c.off:
		return "advisor: off for this session (/advisor <model> picks one)"
	case wanted.model != "" && len(cfg.Command) > 0:
		return "advisor: off (its model backend needs the openai-codex provider; /advisor command goes back to the configured command)"
	case wanted.model != "":
		return "advisor: off (its model backend needs the openai-codex provider)"
	case cfg.Refused != "":
		return "advisor: off (" + cfg.Refused + ")"
	case !e.advisorProvider(provider):
		return "advisor: off (set [advisor] command in the config)"
	}

	return "advisor: off (set [advisor] model or command in the config, or pick a model with /advisor <model>)"
}
