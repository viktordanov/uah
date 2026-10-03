package goal

import (
	"encoding/json"
	"fmt"
)

// Codex's goal tools (codex-rs ext/goal/src/spec.rs, b741e48): their names,
// descriptions, and schemas word for word, with the keys in the order
// Codex's BTreeMap sends them.
const (
	GetToolName    = "get_goal"
	CreateToolName = "create_goal"
	UpdateToolName = "update_goal"
)

// ToolNames are the goal tools in the order they are offered.
var ToolNames = []string{GetToolName, CreateToolName, UpdateToolName}

// Tool is one tool's definition.
type Tool struct {
	Name        string
	Description string
	// Schema is the parameters' JSON schema.
	Schema string
}

// Tools are the goal tools' definitions.
func Tools() []Tool {
	return []Tool{
		{
			Name:        GetToolName,
			Description: "Get the current goal for this thread, including status, budgets, token and elapsed-time usage, and remaining token budget.",
			Schema:      `{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		},
		{
			Name: CreateToolName,
			Description: "Create a goal only when explicitly requested by the user or system/developer instructions; do not infer goals from ordinary tasks.\n" +
				"Set token_budget only when an explicit token budget is requested. Fails if an unfinished goal exists; use " + UpdateToolName + " only for status.",
			Schema: `{"type":"object","properties":{` +
				`"objective":{"type":"string","description":"Required. The concrete objective to start pursuing. This starts a new active goal when no goal exists or replaces the current goal when it is complete."},` +
				`"token_budget":{"type":"integer","description":"Positive token budget for the new goal. Omit unless explicitly requested."}},` +
				`"required":["objective"],"additionalProperties":false}`,
		},
		{
			Name:        UpdateToolName,
			Description: updateDescription,
			Schema: `{"type":"object","properties":{"status":{"type":"string","enum":["complete","blocked","paused"],` +
				`"description":"Required. ` + "`paused`" + ` requires an explicit user request. Set to ` + "`complete`" + ` only when the objective is achieved and no required work remains. ` +
				`Set to ` + "`blocked`" + ` only after the same blocking condition has recurred for at least three consecutive goal turns and the agent is at an impasse. ` +
				`After a previously blocked goal is resumed, the resumed run starts a fresh blocked audit."}},` +
				`"required":["status"],"additionalProperties":false}`,
		},
	}
}

const updateDescription = "Update the existing goal.\n" +
	"Set status to `paused` only at the user's explicit request to pause this goal, never on your own initiative. Ask if unclear; a later resume revokes that request. Report the returned status and stop goal work. Budget limits take precedence over pausing.\n" +
	"Set status to `complete` only when the objective has actually been achieved and no required work remains.\n" +
	"Set status to `blocked` only when the same blocking condition has repeated for at least three consecutive goal turns, counting the original/user-triggered turn and any automatic continuations, and the agent cannot make meaningful progress without user input or an external-state change.\n" +
	"If the user resumes a goal that was previously marked `blocked`, treat the resumed run as a fresh blocked audit. If the same blocking condition then repeats for at least three consecutive resumed goal turns, set status to `blocked` again.\n" +
	"Once the blocked threshold is satisfied, do not keep reporting that you are still blocked while leaving the goal active; set status to `blocked`.\n" +
	"Do not use `blocked` merely because the work is hard, slow, uncertain, incomplete, or would benefit from clarification.\n" +
	"Do not mark a goal complete merely because its budget is nearly exhausted or because you are stopping work.\n" +
	"You cannot use this tool to resume, budget-limit, or usage-limit a goal; those status changes are controlled by the user or system.\n" +
	"When marking a budgeted goal achieved with status `complete`, report the final token usage from the tool result to the user."

// Codex's refusals.
const (
	ErrUnfinished   = "cannot create a new goal because this thread has an unfinished goal; complete the existing goal first"
	ErrNoGoal       = "cannot update goal because this thread has no goal"
	ErrUpdateStatus = "update_goal can only mark the existing goal complete, blocked, or paused at the user's explicit request; resume, budget-limited, and usage-limited status changes are controlled by the user or system"
)

// CreateArgs are create_goal's arguments.
type CreateArgs struct {
	Objective   string `json:"objective"`
	TokenBudget *int64 `json:"token_budget"`
}

// UpdateArgs are update_goal's arguments.
type UpdateArgs struct {
	Status Status `json:"status"`
}

// ParseArgs reads a call's arguments as Codex's serde would.
func ParseArgs[T any](arguments string) (T, error) {
	var v T
	if arguments == "" {
		arguments = "{}"
	}
	if err := json.Unmarshal([]byte(arguments), &v); err != nil {
		return v, fmt.Errorf("failed to parse function arguments: %w", err)
	}

	return v, nil
}

// completionReport is Codex's note to the model when a goal it marked
// complete had a budget or used time.
const completionReport = "Goal achieved. Report final usage from this tool result's structured goal fields. If `goal.tokenBudget` is present, include token usage from `goal.tokensUsed` and `goal.tokenBudget`. If `goal.timeUsedSeconds` is greater than 0, summarize elapsed time in a concise, human-friendly form appropriate to the response language."

// Result is a goal tool's result for the model: Codex's GoalToolResponse,
// {"goal":{...},"remainingTokens":N,"completionBudgetReport":"..."}, with
// null for what is missing. sessionID stands for Codex's thread ID;
// completed asks for the completion report.
func Result(g *Goal, sessionID string, completed bool) string {
	type wire struct {
		ThreadID        string `json:"threadId"`
		Objective       string `json:"objective"`
		Status          Status `json:"status"`
		TokenBudget     *int64 `json:"tokenBudget,omitempty"`
		TokensUsed      int64  `json:"tokensUsed"`
		TimeUsedSeconds int64  `json:"timeUsedSeconds"`
		CreatedAt       int64  `json:"createdAt"`
		UpdatedAt       int64  `json:"updatedAt"`
	}
	out := struct {
		Goal                   *wire   `json:"goal"`
		RemainingTokens        *int64  `json:"remainingTokens"`
		CompletionBudgetReport *string `json:"completionBudgetReport"`
	}{}
	if g != nil {
		w := wire{
			ThreadID: sessionID, Objective: g.Objective, Status: g.Status, TokensUsed: g.TokensUsed,
			TimeUsedSeconds: g.TimeUsedSeconds, CreatedAt: g.CreatedAt.Unix(), UpdatedAt: g.UpdatedAt.Unix(),
		}
		if g.TokenBudget > 0 {
			budget, remaining := g.TokenBudget, g.RemainingTokens()
			w.TokenBudget, out.RemainingTokens = &budget, &remaining
		}
		out.Goal = &w
		if completed && g.Status == StatusComplete && (g.TokenBudget > 0 || g.TimeUsedSeconds > 0) {
			report := completionReport
			out.CompletionBudgetReport = &report
		}
	}
	data, _ := json.Marshal(out) //nolint:errchkjson // plain values

	return string(data)
}
