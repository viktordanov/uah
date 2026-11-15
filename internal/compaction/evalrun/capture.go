package evalrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/compaction"
	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/engine/embedded"
	"github.com/viktordanov/uah/internal/session"
)

// probeMessage is the message a capture sends to start a run. It follows
// the covered history as new input, so no strategy covers it.
const probeMessage = "(compaction evaluation)"

// captureTimeout bounds one capture.
const captureTimeout = time.Minute

// Capture opens the session id of sessionsDir, cut after the item seq, on
// the embedded engine in scratch (a directory evalrun may fill), and
// returns the first model request the context builder produced: the
// history up to the cut and the probe message. The session's rewinds made
// by then apply; its compactions do not, so the request is the full
// history.
func Capture(ctx context.Context, scratch, sessionsDir, id string, p Point) ([]llm.Item, error) {
	state, err := os.MkdirTemp(scratch, "case-")
	if err != nil {
		return nil, fmt.Errorf("failed to make a scratch home: %w", err)
	}
	defer os.RemoveAll(state)
	dir := filepath.Join(state, "sessions")
	workspace := filepath.Join(state, "workspace")
	for _, d := range []string{dir, workspace} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("failed to make %s: %w", d, err)
		}
	}
	if err := Cut(filepath.Join(sessionsDir, id+".session.jsonl"), filepath.Join(dir, id+".session.jsonl"), p.Seq); err != nil {
		return nil, err
	}
	if err := copyRewinds(sessionsDir, dir, id, p.At); err != nil {
		return nil, err
	}
	if err := stubSkill(workspace); err != nil {
		return nil, err
	}

	return capture(ctx, state, workspace, id)
}

// copyRewinds copies the rewinds made by at.
func copyRewinds(from, to, id string, at time.Time) error {
	cuts, err := compaction.OpenRewinds(from, id).Records()
	if err != nil {
		return err
	}
	for _, r := range cuts {
		if !r.At.After(at) {
			if err := compaction.OpenRewinds(to, id).Append(r); err != nil {
				return err
			}
		}
	}

	return nil
}

func capture(ctx context.Context, state, workspace, id string) ([]llm.Item, error) {
	ctx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()
	client := &captureClient{}
	eng := embedded.New(embedded.Config{
		StateDir: state, Provider: captureProvider,
		Providers: []embedded.Provider{{Name: captureProvider, DefaultModel: captureModel, NewClient: func(embedded.ClientConfig) (embedded.Client, error) { return client, nil }}},
		Getenv:    func(k string) string { return map[string]string{"SHELL": "/bin/sh", "HOME": state}[k] },
		Logger:    slog.New(slog.DiscardHandler),
		Subagents: noSubagents{},
	})
	s, err := session.Open(ctx, eng, session.Options{
		ID: id, Resumed: true,
		Settings: session.Settings{Provider: captureProvider, Model: captureModel, Workspace: workspace, MaxAttempts: 1},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open the cut session: %w", err)
	}
	defer s.Close()
	if _, err := s.Submit(probeMessage); err != nil {
		return nil, fmt.Errorf("failed to start the capture run: %w", err)
	}
	why, err := waitIdle(ctx, s)
	if err != nil {
		return nil, err
	}
	input := client.first()
	if input == nil {
		return nil, fmt.Errorf("the capture run sent no model request (%s)", why)
	}

	return input, nil
}

// waitIdle waits for the run to end; why is its status and errors.
func waitIdle(ctx context.Context, s *session.Session) (why string, err error) {
	finished := false
	var errs []string
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				return "", errors.New("the session closed during the capture")
			}
			switch v := e.(type) {
			case core.RunnerError:
				errs = append(errs, v.Message)
			case core.RunFinished:
				finished = true
				errs = append(errs, "status "+string(v.Result.Status))
			case session.Idle:
				if finished {
					return strings.Join(errs, "; "), nil
				}
			}
		case <-ctx.Done():
			return "", fmt.Errorf("the capture timed out: %w", ctx.Err())
		}
	}
}

// captureProvider is the provider whose client the capture replaces: one
// the session accepts, with no hosted tools and no credentials.
const captureProvider = "ollama"

// captureModel names the model a capture asks for.
const captureModel = "evaluation"

// captureClient keeps the first request and answers each with a final
// message, so the run ends.
type captureClient struct {
	mu    sync.Mutex
	input []llm.Item
}

func (c *captureClient) Respond(_ context.Context, req llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	c.mu.Lock()
	if c.input == nil {
		c.input = req.Input
	}
	c.mu.Unlock()

	return llm.Response{ID: captureModel, Stop: llm.StopComplete, Output: []llm.Item{{
		Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "ok", Phase: "final_answer"},
	}}}, nil
}

func (c *captureClient) first() []llm.Item {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.input
}

func (c *captureClient) Close() error { return nil }

var _ io.Closer = (*captureClient)(nil)

// noSubagents knows the subagent tools' names, so a session with past
// calls to them loads, and offers and runs none.
type noSubagents struct{}

func (noSubagents) Attach(engine.AgentParent) []engine.AgentTool { return nil }
func (noSubagents) ToolNames() []string                          { return agents.ToolNames() }
func (noSubagents) Interrupt(string)                             {}

func (noSubagents) Call(context.Context, engine.AgentCall) (string, error) {
	return "", errors.New("the evaluation runs no subagents")
}

func (noSubagents) WaitTimeout(string, json.RawMessage) (time.Duration, bool) { return 0, false }

// stubSkill puts one skill in the scratch workspace, so the engine always
// offers SkillUse: a recorded SkillUse call must find its tool to be
// restored, whatever skills the machine running the evaluation has.
func stubSkill(workspace string) error {
	dir := filepath.Join(workspace, ".agents", "skills", "evalrun-stub")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to make the stub skill: %w", err)
	}
	body := "---\nname: evalrun-stub\ndescription: Keeps SkillUse available while a session is replayed.\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		return fmt.Errorf("failed to write the stub skill: %w", err)
	}

	return nil
}
