package codereview

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Output is the reviewer's answer, Codex's ReviewOutputEvent: the
// findings, a verdict ("patch is correct" or "patch is incorrect"), its
// explanation, and the reviewer's confidence.
type Output struct {
	Findings               []Finding `json:"findings"`
	OverallCorrectness     string    `json:"overall_correctness"`
	OverallExplanation     string    `json:"overall_explanation"`
	OverallConfidenceScore float64   `json:"overall_confidence_score"`

	// scored is whether the answer had overall_confidence_score, so a 0
	// it wrote counts and one left out does not.
	scored bool
}

// Finding is one issue: a title (which the rubric starts with "[P1]"), a
// Markdown body, the reviewer's confidence, a priority from 0 (P0) to 3,
// and where it is.
type Finding struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	ConfidenceScore float64  `json:"confidence_score"`
	Priority        *int     `json:"priority"`
	CodeLocation    Location `json:"code_location"`

	// scored is whether the finding had confidence_score.
	scored bool
}

// UnmarshalJSON reads a finding and notes whether it had a confidence.
func (f *Finding) UnmarshalJSON(data []byte) error {
	type plain Finding
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err //nolint:wrapcheck // the caller's own decoding error
	}
	*f = Finding(p)
	f.scored = has(data, "confidence_score")

	return nil
}

// UnmarshalJSON reads an answer and notes whether it had an overall
// confidence.
func (o *Output) UnmarshalJSON(data []byte) error {
	type plain Output
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err //nolint:wrapcheck // the caller's own decoding error
	}
	*o = Output(p)
	o.scored = has(data, "overall_confidence_score")

	return nil
}

// has reports whether a JSON object has key with a value other than null.
func has(data []byte, key string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	v, ok := fields[key]

	return ok && string(v) != "null"
}

// Location is a finding's file and its lines, inclusive.
type Location struct {
	AbsoluteFilePath string    `json:"absolute_file_path"`
	LineRange        LineRange `json:"line_range"`
}

// LineRange is a finding's first and last line.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// FallbackMessage is Codex's text for a review with nothing to show.
const FallbackMessage = "Reviewer failed to output a response."

// Parse reads the reviewer's last message as Codex does: the whole text as
// JSON, else the text from its first "{" to its last "}" (a fenced or
// explained answer), else the whole text as the explanation, without
// findings. Unlike Codex, a finding without a priority still parses: the
// rubric allows leaving it out.
func Parse(text string) Output {
	var out Output
	if json.Unmarshal([]byte(text), &out) == nil {
		return out
	}
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		if json.Unmarshal([]byte(text[i:j+1]), &out) == nil {
			return out
		}
	}

	return Output{OverallExplanation: text}
}

// Location is "path:start-end", Codex's form.
func (f Finding) Location() string {
	l := f.CodeLocation

	return fmt.Sprintf("%s:%d-%d", l.AbsoluteFilePath, l.LineRange.Start, l.LineRange.End)
}

// Text is the review as Codex shows it and hands it to the main agent
// (render_review_output_text): the explanation, then the findings block,
// or FallbackMessage when both are empty.
func (o Output) Text() string {
	var sections []string
	if e := strings.TrimSpace(o.OverallExplanation); e != "" {
		sections = append(sections, e)
	}
	if len(o.Findings) > 0 {
		sections = append(sections, strings.TrimSpace(findingsBlock(o.Findings)))
	}
	if len(sections) == 0 {
		return FallbackMessage
	}

	return strings.Join(sections, "\n\n")
}

// findingsBlock is Codex's format_review_findings_block without a
// selection: a header, then "- title — path:start-end" and the body
// indented under it, for each finding.
func findingsBlock(findings []Finding) string {
	lines := []string{""}
	if len(findings) > 1 {
		lines = append(lines, "Full review comments:")
	} else {
		lines = append(lines, "Review comment:")
	}
	for _, f := range findings {
		lines = append(lines, "", fmt.Sprintf("- %s — %s", f.Title, f.Location()))
		for line := range strings.Lines(f.Body) {
			lines = append(lines, "  "+strings.TrimRight(line, "\r\n"))
		}
	}

	return strings.Join(lines, "\n")
}

// ExitMessage is what the main agent gets after a review, as Codex records
// it in the thread: the review's results in Codex's <user_action> message,
// or the interrupted form. It goes with the user's next message.
func ExitMessage(o Output, interrupted bool) string {
	if interrupted {
		return exitInterrupted
	}
	results := strings.TrimSpace(o.OverallExplanation)
	if len(o.Findings) > 0 {
		results += "\n" + findingsBlock(o.Findings)
	}

	return strings.Replace(exitSuccess, "{{results}}", results, 1)
}

// IsExitMessage reports whether a user message is ExitMessage's, so the
// transcript shows it as a note instead of a message the user wrote.
func IsExitMessage(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<user_action>") && strings.Contains(text, "<action>review</action>")
}
