package goal

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The texts that tell the model about the goal are Codex's, word for word
// (codex-rs ext/goal/templates/goals, b741e48), in Codex's hidden-context
// wrapper (core/src/context/internal_model_context.rs, user_goal.rs). Each
// goes to the model as a user message: the session appends it, so the
// system prompt and every earlier item stay the same and the prompt cache
// holds.
var (
	//go:embed prompts/continuation.md
	continuationTemplate string
	//go:embed prompts/budget_limit.md
	budgetLimitTemplate string
	//go:embed prompts/objective_updated.md
	objectiveUpdatedTemplate string
)

// Codex's wrapper and its two sources: "goal" for the runtime's steering
// (a continuation, the budget limit, an edited objective) and "user_goal"
// for what the user did to the goal.
const (
	contextStart     = "<codex_internal_context source=\""
	contextEnd       = "</codex_internal_context>"
	sourceGoal       = "goal"
	sourceUserGoal   = "user_goal"
	maxEvidenceBytes = 700 // user_goal.rs MAX_OBJECTIVE_BYTES
)

// Kind is what a goal context message is.
type Kind int

const (
	KindNone Kind = iota
	// KindContinuation starts a run uah began on its own for the goal.
	KindContinuation
	// KindBudgetLimit tells the model the budget ran out mid-run.
	KindBudgetLimit
	// KindObjectiveUpdated tells the model the user edited the objective.
	KindObjectiveUpdated
	// KindUser records what the user did: set, edit, pause, resume, clear.
	KindUser
)

// Continuation is the message of an automatic continuation run: Codex's
// continuation.md without the update_plan paragraph (uah has no plan
// tool), plus, when the goal has a cap, a line with its continuations.
func Continuation(g Goal) string {
	text := withoutPlanParagraph(continuationTemplate)
	if g.MaxContinuations > 0 {
		line := fmt.Sprintf("- Automatic continuations: %d of %d\n", g.Continuations, g.MaxContinuations)
		text = strings.Replace(text, "- Tokens remaining: {{ remaining_tokens }}\n", "- Tokens remaining: {{ remaining_tokens }}\n"+line, 1)
	}

	return wrap(sourceGoal, render(text, g, "unbounded"))
}

// BudgetLimit tells the model mid-run that the goal reached its budget and
// to wrap up.
func BudgetLimit(g Goal) string { return wrap(sourceGoal, render(budgetLimitTemplate, g, "")) }

// ObjectiveUpdated tells the model mid-run that the user edited the
// objective.
func ObjectiveUpdated(g Goal) string {
	return wrap(sourceGoal, render(objectiveUpdatedTemplate, g, "unknown"))
}

// UserSet records that the user set or edited the objective, and the
// status when the user set that too.
func UserSet(objective string, status Status) string {
	body := "\n"
	if objective != "" {
		quoted := jsonString(objective)
		if len(quoted) <= maxEvidenceBytes {
			body += "User set the goal: " + quoted + "\n"
		} else {
			body += "User set the goal: [objective omitted; exceeds the evidence limit].\n"
		}
	}
	if status != "" {
		body += "User set goal status: " + jsonString(string(status)) + ".\n"
	}

	return contextStart + sourceUserGoal + "\">" + body + contextEnd
}

// UserCleared records that the user cleared the goal.
func UserCleared() string {
	return contextStart + sourceUserGoal + "\">\nUser cleared the goal.\n" + contextEnd
}

// Parse reports what goal message text is, and its body without the
// wrapper; KindNone for any other text.
func Parse(text string) (Kind, string) {
	t := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(t, contextStart)
	if !ok || !strings.HasSuffix(rest, contextEnd) {
		return KindNone, ""
	}
	source, body, ok := strings.Cut(strings.TrimSuffix(rest, contextEnd), "\">")
	if !ok {
		return KindNone, ""
	}
	body = strings.TrimSpace(body)
	switch {
	case source == sourceUserGoal:
		return KindUser, body
	case source != sourceGoal:
		return KindNone, ""
	case strings.HasPrefix(body, "The active thread goal has reached its token budget."):
		return KindBudgetLimit, body
	case strings.HasPrefix(body, "The active thread goal objective was edited by the user."):
		return KindObjectiveUpdated, body
	default:
		return KindContinuation, body
	}
}

// IsContext reports whether text is one of the goal's messages, which are
// uah's rather than the user's.
func IsContext(text string) bool { kind, _ := Parse(text); return kind != KindNone }

func wrap(source, body string) string {
	return contextStart + source + "\">\n" + body + "\n" + contextEnd
}

// render fills a template's placeholders; unknownRemaining is what
// remaining_tokens says without a budget.
func render(template string, g Goal, unknownRemaining string) string {
	budget, remaining := "none", unknownRemaining
	if g.TokenBudget > 0 {
		budget, remaining = strconv.FormatInt(g.TokenBudget, 10), strconv.FormatInt(g.RemainingTokens(), 10)
	}

	return strings.NewReplacer(
		"{{ objective }}", escapeXML(g.Objective),
		"{{ tokens_used }}", strconv.FormatInt(g.TokensUsed, 10),
		"{{ token_budget }}", budget,
		"{{ remaining_tokens }}", remaining,
		"{{ time_used_seconds }}", strconv.FormatInt(g.TimeUsedSeconds, 10),
	).Replace(template)
}

// withoutPlanParagraph drops the "Progress visibility" paragraph, as
// Codex's without_update_plan_instructions does when update_plan is off.
func withoutPlanParagraph(text string) string {
	lines := strings.SplitAfter(text, "\n")
	var b strings.Builder
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "Progress visibility:" && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "If update_plan is available") {
			i++
			if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "" {
				i++
			}

			continue
		}
		b.WriteString(lines[i])
	}

	return b.String()
}

func escapeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// jsonString quotes s as serde_json does, so the objective stays data.
func jsonString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) //nolint:errchkjson // a string always encodes

	return strings.TrimSuffix(b.String(), "\n")
}
