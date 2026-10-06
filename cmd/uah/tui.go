package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/history"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/images"
	"github.com/viktordanov/uah/internal/images/clipboard"
	"github.com/viktordanov/uah/internal/models"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/store"
	"github.com/viktordanov/uah/internal/tui/bubble"
	"github.com/viktordanov/uah/internal/tui/term"
	"github.com/viktordanov/uah/internal/usage/cachestats"
)

// tuiAction is the default action: open the terminal UI, optionally with a
// first prompt.
func tuiAction(ctx context.Context, cmd *cli.Command) error {
	return openTUI(ctx, cmd, tuiLaunch{
		sessionRef: cmd.String("session"),
		prompt:     strings.TrimSpace(strings.Join(cmd.Args().Slice(), " ")),
	})
}

// tuiLaunch says what the TUI shows first.
type tuiLaunch struct {
	sessionRef string // a session to resume, by ID or unique prefix
	prompt     string
	picker     bool // start in the session picker
	all        bool // the picker shows every directory
}

func openTUI(ctx context.Context, cmd *cli.Command, launch tuiLaunch) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return cli.Exit("the TUI needs a terminal; for scripts and pipes use uah exec", exitUsage)
	}
	st, err := setupFor(ctx, cmd, os.Stderr, launch.sessionRef) // validates flags before the screen takes over
	if err != nil {
		return err
	}
	logFile, err := openTUILog(st.StateDir)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cwd, err := currentDir(cmd)
	if err != nil {
		return err
	}
	prompts, err := history.New(home.Dir(), st.Config.History.Persistence, st.Config.History.MaxBytes)
	if err != nil {
		return err
	}
	var once sync.Once
	startupNotes := func() (notes []string) {
		once.Do(func() { notes, _ = ctx.Value(startupKey{}).([]string) })

		return notes
	}
	first := "" // a session to resume first; --session-id names a new one
	if st.Options.Resumed {
		first = st.Options.ID
	}
	newID := &takeOnce{value: cmd.String(flagSessionID)}
	// From here the TUI handles SIGINT and SIGTERM itself (term): it
	// restores the terminal, and drops the SIGINT that ctrl+c in ctrl+g's
	// editor also sends uah, which would cancel main's signal context for
	// good. The TUI and every dependency below live on this context.
	ctx = tuiContext(ctx)

	deps := bubble.Deps{
		SessionID:   first,
		Prompt:      launch.prompt,
		Cwd:         cwd,
		Picker:      launch.picker,
		AllSessions: launch.all,
		Details:     st.Config.TUI.Details,
		Mouse:       st.Config.TUI.MouseOn(),
		Title:       st.Config.TUI.TitleOn(),
		FileLinks:   st.Config.TUI.FileLinksMode(),
		Terminal: term.Options{
			SyncInMultiplexer: st.Config.TUI.SyncInMultiplexer,
			KeepBlanks:        st.Config.TUI.KeepBlanks,
		},
		History:    &prompts,
		Version:    buildVersion(),
		Config:     tuiConfig(cmd),
		SaveConfig: tuiSaveConfig(ctx, cmd),
		Images:     &images.Store{Dir: images.DirIn(st.StateDir)},
		Clipboard:  clipboard.System(),
		PasteText:  clipboard.SystemTextReader().ReadText,
		CopyText:   clipboard.SystemWriter().WriteText,
		// ctrl+g refuses a draft directory these roots expose.
		WritableRoots: st.Sandbox.WritableRoots,
		Open: func(ctx context.Context, id string) (*session.Session, []session.LoadedRun, error) {
			in := inputs(cmd)
			in.SessionRef, in.NewSessionID, in.Interactive = id, "", true
			if id == "" {
				in.NewSessionID = newID.take() // the first new session only; /new gets a fresh ID
			}
			setup, err := setupWith(ctx, in, logFile)
			if err != nil {
				return nil, nil, err
			}
			setup.Options.Source, setup.Options.Interactive, setup.Options.Stream = session.SourceTUI, true, true
			setup.Options.Notices = append(setup.Options.Notices, startupNotes()...) // the first session shows them
			s, err := session.Open(context.WithoutCancel(ctx), setup.Engine, setup.Options)
			if err != nil {
				return nil, nil, err
			}
			if !setup.Options.Resumed {
				return s, nil, nil
			}
			history, err := session.Load(setup.StateDir, s.ID())
			if err != nil {
				_ = s.Close()

				return nil, nil, err
			}

			return s, history, nil
		},
		// The session's catalog caches for five minutes and bounds a
		// refresh to five seconds.
		Models: func(ctx context.Context, provider string) models.Catalog {
			return st.Models.Catalog(ctx, provider, models.OnlineIfUncached)
		},
		Windows:  st.Models.Window,
		Usage:    st.Usage, // read after each run and on /status, never on a timer
		Activity: func() (map[string]int, error) { return store.ActivityIn(ctx, st.StateDir, time.Now(), 7*12) },
		Cache:    func(id string) ([]cachestats.Attributed, error) { return session.CacheStats(st.StateDir, id) },
		Sessions: func() ([]session.Info, error) {
			infos, err := store.List(ctx, st.StateDir)

			return session.Interactive(infos), err
		},
	}
	exit, err := bubble.Run(ctx, deps)
	printExit(os.Stdout, exit)
	if err != nil {
		return fmt.Errorf("the TUI stopped: %w", err)
	}

	return nil
}

// currentDir is the directory sessions are scoped to: --workspace when given,
// otherwise the working directory.
func currentDir(cmd *cli.Command) (string, error) {
	dir := "."
	if w := cmd.String("workspace"); w != "" {
		dir = w
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve the current directory: %w", err)
	}

	return abs, nil
}

// openTUILog opens the diagnostic log; the screen belongs to the TUI.
func openTUILog(stateDir string) (*os.File, error) {
	dir := filepath.Join(stateDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "uah-tui.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open log: %w", err)
	}

	return f, nil
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// takeOnce hands out its value once, then "".
type takeOnce struct {
	mu    sync.Mutex
	value string
}

func (t *takeOnce) take() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := t.value
	t.value = ""

	return v
}

// tuiContext is the context the TUI and its dependencies live on: ctx's
// values without its cancellation, since term handles the signals that
// cancel main's context.
func tuiContext(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }
