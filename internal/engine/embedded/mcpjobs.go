package embedded

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/viktordanov/uah-core/harness/operation"

	"github.com/viktordanov/uah/internal/mcp"
)

var errInterrupted = errors.New("interrupted: the run stopped while this MCP call was running, and it was not repeated")

// mcpJobs is the runner's RemoteJobHandler for MCP calls. Each call runs on
// its own goroutine, so the coordinator never waits on a server; it reports
// awaiting when the call starts and its result when it ends.
type mcpJobs struct {
	ctx     context.Context
	manager *mcp.Manager // nil when no servers are configured
	updates chan operation.Operation

	mu      sync.Mutex
	cancels map[operation.ID]context.CancelFunc
}

func newMCPJobs(ctx context.Context, manager *mcp.Manager) *mcpJobs {
	return &mcpJobs{ctx: ctx, manager: manager, updates: make(chan operation.Operation), cancels: map[operation.ID]context.CancelFunc{}}
}

func (*mcpJobs) RemoteJobPlanType() operation.RemoteJobPlanType       { return mcpPlanType }
func (*mcpJobs) RemoteJobPlanVersion() operation.RemoteJobPlanVersion { return mcpPlanVersion }
func (j *mcpJobs) RemoteJobUpdates() <-chan operation.Operation       { return j.updates }

// AddRemoteJob starts a call. A call that had already started before a
// restart fails instead of running twice.
func (j *mcpJobs) AddRemoteJob(op operation.Operation) error {
	state, err := operation.DecodeRemoteJobState(op)
	if err != nil {
		return err // the runner's own error
	}
	var plan mcpPlan
	if err := json.Unmarshal(state.Plan.Data, &plan); err != nil {
		return err // the operation manager reports it
	}
	if op.Status != operation.StatusReady {
		go j.finish(operation.FailRemoteJob(op, errInterrupted))

		return nil
	}
	ctx, cancel := context.WithCancel(j.ctx)
	j.mu.Lock()
	j.cancels[op.ID] = cancel
	j.mu.Unlock()
	go j.run(ctx, op, state, plan)

	return nil
}

// CancelRemoteJob cancels a running call; it then reports canceled.
func (j *mcpJobs) CancelRemoteJob(id operation.ID, _ string) error {
	j.mu.Lock()
	cancel := j.cancels[id]
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	return nil
}

func (j *mcpJobs) run(ctx context.Context, op operation.Operation, state operation.RemoteJobState, plan mcpPlan) {
	defer func() {
		j.mu.Lock()
		j.cancels[op.ID]()
		delete(j.cancels, op.ID)
		j.mu.Unlock()
	}()
	step, err := operation.UpdateRemoteJob(op, state, operation.StatusAwaiting)
	if err != nil || !j.send(*step.Operation) {
		return
	}
	op = *step.Operation
	if j.manager == nil {
		j.finish(operation.FailRemoteJob(op, errors.New("no MCP servers are configured")))

		return
	}
	if plan.Op != "" {
		text, err := resourceJob(ctx, j.manager, plan)
		switch {
		case ctx.Err() != nil && j.ctx.Err() == nil: // canceled by the coordinator
			j.finish(operation.CancelRemoteJob(op))
		case err != nil:
			j.finish(operation.FailRemoteJob(op, err))
		default:
			state.TerminalResult = text
			j.finish(operation.UpdateRemoteJob(op, state, operation.StatusCompleted))
		}

		return
	}
	r, err := j.manager.Call(ctx, plan.Server, plan.Tool, plan.Arguments)
	switch {
	case ctx.Err() != nil && j.ctx.Err() == nil: // canceled by the coordinator
		j.finish(operation.CancelRemoteJob(op))
	case err != nil:
		j.finish(operation.FailRemoteJob(op, err))
	case r.IsError:
		state.TerminalError = r.Text
		j.finish(operation.UpdateRemoteJob(op, state, operation.StatusFailed))
	default:
		state.TerminalResult = r.Text
		if len(r.Images) > 0 {
			state.Handle, _ = json.Marshal(mcpOutputs{Images: r.Images}) //nolint:errchkjson // strings always encode
		}
		j.finish(operation.UpdateRemoteJob(op, state, operation.StatusCompleted))
	}
}

// finish sends a step's operation; a step that cannot be built was already
// validated when the job was added, so it is dropped.
func (j *mcpJobs) finish(step operation.Step, err error) {
	if err == nil && step.Operation != nil {
		j.send(*step.Operation)
	}
}

func (j *mcpJobs) send(op operation.Operation) bool {
	select {
	case j.updates <- op:
		return true
	case <-j.ctx.Done():
		return false
	}
}
