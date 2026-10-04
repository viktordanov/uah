package bubble

import (
	"context"
	"errors"
	"fmt"

	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/state"
	"github.com/viktordanov/uah/internal/tui/term"
)

var (
	errNoSession   = errors.New("no session is open")
	errNoConfig    = errors.New("settings are not available here")
	errEmptyPrompt = errors.New("the MCP prompt returned no text")
)

// run turns an effect into a command that does its I/O off the update loop.
func (m Model) run(e state.Effect) term.Cmd { //nolint:gocyclo // a dispatch switch over a closed set; see docs/documentation/architecture.md
	sess := m.sess
	fail := func(err error) term.Msg { return state.Failed{Err: err} }
	withSession := func(fn func(*session.Session) error) term.Cmd {
		return m.calls.next(func() term.Msg {
			if sess == nil {
				return fail(errNoSession)
			}
			if err := fn(sess); err != nil {
				return fail(err)
			}

			return nil
		})
	}
	if cmd, ok := m.runImage(e); ok {
		return cmd
	}
	if cmd, ok := m.runReview(e); ok {
		return cmd
	}
	if cmd, ok := m.runHistory(e); ok {
		return cmd
	}
	if cmd, ok := m.runMCP(e); ok {
		return cmd
	}
	ctx, store := m.ctx, m.deps.Images
	switch e := e.(type) {
	case state.EffSubmit:
		return withSession(func(s *session.Session) error {
			_, err := s.Submit(withResources(ctx, s.MCP(), store, e.Text))
			return err
		})
	case state.EffSteer:
		return withSession(func(s *session.Session) error {
			_, err := s.Send(withResources(ctx, s.MCP(), store, e.Text), e.When)
			return err
		})
	case state.EffSteerQueued:
		return withSession(func(s *session.Session) error { _, err := s.SteerQueued(); return err })
	case state.EffShell:
		ctx := m.ctx

		return func() term.Msg { // not in m.calls: it runs until the command ends
			if sess == nil {
				return fail(errNoSession)
			}
			if _, err := sess.RunShell(ctx, e.Command); err != nil {
				return fail(err)
			}

			return nil
		}
	case state.EffInterrupt:
		return withSession(func(s *session.Session) error { return s.Interrupt() })
	case state.EffClear:
		return withSession(func(s *session.Session) error { return s.Clear() })
	case state.EffRewind:
		return withSession(func(s *session.Session) error { return s.Rewind(e.ID) })
	case state.EffCompact:
		return withSession(func(s *session.Session) error { return s.CompactWith(e.Focus) })
	case state.EffGoal:
		return withSession(func(s *session.Session) error { return runGoal(s, e) })
	case state.EffResolve:
		return withSession(func(s *session.Session) error { return s.Resolve(e.ID, e.Answer) })
	case state.EffAnswerQuestions:
		return withSession(func(s *session.Session) error { return s.AnswerQuestions(e.ID, e.Answers) })
	case state.EffSetSettings:
		return withSession(func(s *session.Session) error { _, err := s.SetSettings(e.Settings); return err })
	case state.EffWithdraw:
		return m.calls.next(func() term.Msg {
			if sess == nil {
				return fail(errNoSession)
			}
			ok, err := sess.Withdraw(e.ID)
			if err != nil {
				return fail(err)
			}
			if !ok {
				return nil // already sent
			}

			return withdrawnMsg{text: e.Text}
		})
	case state.EffLoadSessions:
		return func() term.Msg {
			infos, err := m.deps.Sessions()
			if err != nil {
				return fail(err)
			}
			local := infos
			if m.deps.Cwd != "" {
				local = session.InDir(infos, m.deps.Cwd)
			}

			return state.SessionsLoaded{Sessions: infos, Local: local, All: m.deps.AllSessions || m.deps.Cwd == ""}
		}
	case state.EffLoadActivity:
		if m.deps.Activity == nil {
			return nil
		}

		return func() term.Msg {
			counts, err := m.deps.Activity()
			if err != nil {
				return fail(err)
			}

			return state.ActivityLoaded{Counts: counts}
		}
	case state.EffLoadUsage:
		return m.loadUsage(e)
	case state.EffLoadCache:
		return m.loadCache(e)
	case state.EffLoadModels:
		if m.deps.Models == nil { // no list, so /model says so instead of loading forever
			return func() term.Msg {
				return state.ModelsLoaded{Catalog: models.Catalog{Provider: e.Provider, Origin: models.OriginNone}}
			}
		}

		return func() term.Msg { return state.ModelsLoaded{Catalog: m.deps.Models(m.ctx, e.Provider)} }
	case state.EffLoadFiles:
		dir := m.deps.Cwd

		return func() term.Msg { return state.FilesLoaded{Paths: workspaceFiles(m.ctx, dir)} }
	case state.EffLoadConfig:
		if m.deps.Config == nil {
			return func() term.Msg { return state.ConfigLoaded{Err: errNoConfig} }
		}

		return func() term.Msg { return m.deps.Config(m.ctx) }
	case state.EffSaveConfig:
		if m.deps.SaveConfig == nil {
			return func() term.Msg { return state.ConfigSaved{Key: e.Key, Value: e.Value, Err: errNoConfig} }
		}

		return func() term.Msg {
			return state.ConfigSaved{Key: e.Key, Value: e.Value, Err: m.deps.SaveConfig(e.Key, e.Value)}
		}
	case state.EffListMCP:
		return func() term.Msg {
			if sess == nil {
				return fail(errNoSession)
			}
			servers, ok := sess.MCPServers()
			if mg := sess.MCP(); e.Verbose && mg != nil {
				ctx, cancel := context.WithTimeout(ctx, resourceListTimeout)
				defer cancel()
				refs := mg.Resources(ctx)
				for i := range servers {
					for _, r := range refs {
						if r.Server == servers[i].Name {
							servers[i].Resources = append(servers[i].Resources, r)
						}
					}
				}
			}

			return state.MCPListed{Servers: servers, Supported: ok, Verbose: e.Verbose}
		}
	case state.EffContext:
		return func() term.Msg {
			if sess == nil {
				return fail(errNoSession)
			}
			u, ok := sess.ContextUsage()

			return state.ContextShown{Usage: u, OK: ok}
		}
	case state.EffOpenSession:
		return m.switchTo(e.ID)
	case state.EffViewAgent:
		return m.watchAgent(e.ID)
	case state.EffAgentSend:
		return m.sendToAgent(e.Text, e.When)
	case state.EffAgentSteerQueued:
		return m.steerAgentQueue()
	case state.EffAgentInterrupt:
		if w := m.watch; w != nil && w.Interrupt != nil {
			w.Interrupt()
		}

		return nil
	case state.EffCopySelection:
		return m.copySelection()
	case state.EffEditDraft:
		return m.editDraft(e.Text)
	case state.EffQuit:
		return m.calls.next(func() term.Msg {
			if sess != nil {
				_ = sess.Close()
			}

			return quitMsg{}
		})
	}

	return nil
}

// open opens a session and reports it with its history.
func (m Model) open(id string) term.Cmd {
	return func() term.Msg {
		s, history, err := m.deps.Open(m.ctx, id)
		if err != nil {
			return state.Failed{Err: fmt.Errorf("failed to open session: %w", err)}
		}

		return openedMsg{sess: s, history: history}
	}
}

// switchTo closes the current session and opens another.
func (m Model) switchTo(id string) term.Cmd {
	sess := m.sess
	next := m.open(id)

	return m.calls.next(func() term.Msg {
		if sess != nil {
			_ = sess.Close()
		}

		return next()
	})
}

// runGoal changes the session's goal; the session reports the change.
func runGoal(s *session.Session, e state.EffGoal) error {
	var err error
	switch e.Op {
	case state.GoalOpSet:
		_, err = s.SetGoal(e.Text)
	case state.GoalOpEdit:
		_, err = s.EditGoal(e.Text)
	case state.GoalOpPause:
		_, err = s.PauseGoal()
	case state.GoalOpResume:
		_, err = s.ResumeGoal()
	case state.GoalOpClear:
		var had bool
		if had, err = s.ClearGoal(); err == nil && !had {
			err = errors.New("no goal to clear")
		}
	}

	return err
}
