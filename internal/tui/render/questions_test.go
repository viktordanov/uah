package render_test

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"

	"github.com/viktordanov/uagent/core"

	"github.com/viktordanov/uah/internal/engine"
	"github.com/viktordanov/uah/internal/session"
	"github.com/viktordanov/uah/internal/tui/render"
	"github.com/viktordanov/uah/internal/tui/state"
)

// migration are a request_user_input call's questions, as a model asks
// them.
var migration = []engine.Question{
	{ID: "strategy", Header: "Migration", Question: "The users table can change three ways. Which should the migration take?", Options: []engine.QuestionOption{
		{Label: "Expand and contract (Recommended)", Description: "Add the new column, backfill it, and drop the old one in a later release."},
		{Label: "Rename in place", Description: "One migration; the table is locked for about a minute."},
		{Label: "Copy and swap", Description: "Build a new table and swap it in; needs twice the disk for a while."},
	}},
	{ID: "rollout", Header: "Rollout", Question: "When should it run?", Options: []engine.QuestionOption{
		{Label: "Next deploy", Description: "Ships with the code."},
		{Label: "By hand", Description: "You run it in a quiet hour."},
	}},
}

const migrationArgs = `{"questions":[{"id":"strategy","header":"Migration","question":"Which?","options":[]},{"id":"rollout","header":"Rollout","question":"When?","options":[]}]}`

// asking is a live run whose agent asked the questions.
func asking() state.State {
	s := apply(base(),
		session.InputQueued{At: t0, Input: core.UserInput{ID: "m", Text: "Plan the users table migration"}},
		session.InputSent{At: t0, IDs: []string{"m"}},
		core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"},
		core.ToolCalled{At: t0, CallID: "c1", Name: engine.QuestionToolName, Arguments: migrationArgs},
		core.ToolStarted{At: t0, CallID: "c1"},
		session.QuestionsAsked{At: t0, ID: "q1", CallID: "c1", Questions: migration},
	)
	s.Now = t0.Add(12 * time.Second)

	return s
}

func TestScreen_Questions(t *testing.T) {
	s := asking()
	golden(t, "questions", screen(s, ""))

	s = apply(s, state.QuestionMove{Delta: 1})
	golden(t, "questions-moved", screen(s, ""))

	// n writes a note on the chosen option; enter keeps it under the row.
	s = apply(s, state.QuestionNote{})
	golden(t, "questions-noting", screen(s, "lock it after 6pm"))
	s = apply(s, state.QuestionAnswer{Draft: "lock it after 6pm"})
	assert.Contains(t, screen(s, ""), "2. Rename in place ✎")
	assert.Contains(t, screen(s, ""), "✎ lock it after 6pm")

	// The first answer shows the second question, its tab marked done, and
	// the last row takes the user's own words.
	s = apply(s, state.QuestionAnswer{}, state.QuestionMove{Delta: -1})
	golden(t, "questions-second", screen(s, "after the Friday backup"))
}

// handlers is a question whose options carry code previews.
var handlers = []engine.Question{{ID: "handler", Header: "Handler", Question: "Which shape should the users API handler take?", Options: []engine.QuestionOption{
	{Label: "net/http (Recommended)", Description: "No dependency; routing by hand.", Preview: "func (s *Server) users(w http.ResponseWriter, r *http.Request) {\n\tid := r.PathValue(\"id\")\n\tu, err := s.store.User(r.Context(), id)\n\tif err != nil {\n\t\thttp.Error(w, err.Error(), http.StatusNotFound)\n\t\treturn\n\t}\n\tjson.NewEncoder(w).Encode(u)\n}"},
	{Label: "chi router", Description: "Middleware and route groups.", Preview: "r := chi.NewRouter()\nr.Use(middleware.Logger)\nr.Get(\"/users/{id}\", s.user)"},
	{Label: "Generated (oapi)", Description: "From the OpenAPI spec.", Preview: strings.Repeat("// generated\n", 20)},
}}}

func previewing(t *testing.T) state.State {
	t.Helper()
	s := apply(base(),
		core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"},
		session.QuestionsAsked{At: t0, ID: "q1", CallID: "c1", Questions: handlers},
	)
	s.Now = t0.Add(5 * time.Second)

	return s
}

// TestScreen_QuestionPreviews: options with previews show the chosen one's
// beside the list on a wide screen, under it on a narrow one, cut to 14
// lines with "… N more lines", and following the choice.
func TestScreen_QuestionPreviews(t *testing.T) {
	s := previewing(t)
	golden(t, "questions-preview", screenAt(s, "", 120))
	golden(t, "questions-preview-narrow", screenAt(s, "", 70))

	// The reducer owns its state, so each step starts from a fresh one.
	more := screenAt(apply(previewing(t), state.QuestionMove{Delta: 2}), "", 120)
	assert.Contains(t, more, "… 6 more lines")
	assert.Contains(t, more, "┌ Generated (oapi)")
	own := screenAt(apply(previewing(t), state.QuestionMove{Delta: -1}), "", 120)
	assert.Contains(t, own, "Your own answer, typed below.")
	for _, w := range []int{120, 70, 40} {
		out, _ := render.Screen(previewing(t), render.NewCache(render.Amber), render.Frame{Width: w, Height: 40, Composer: "λ ", ComposerHeight: 1})
		for line := range strings.SplitSeq(out, "\n") {
			assert.LessOrEqual(t, ansi.StringWidth(line), w, "%q", ansi.Strip(line))
		}
	}
}

// TestScreen_QuestionsNarrow: at 40 columns every line fits, the labels
// and descriptions cut with "…".
func TestScreen_QuestionsNarrow(t *testing.T) {
	out, _ := render.Screen(asking(), render.NewCache(render.Amber), render.Frame{Width: 40, Height: 24, Composer: "λ ", ComposerHeight: 1})
	for line := range strings.SplitSeq(out, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 40, "%q", ansi.Strip(line))
	}
	golden(t, "questions-narrow", screenAt(asking(), "", 40))
}

// TestScreen_QuestionsAnswered: the call's line, with the answers under
// it, once the answers are recorded.
func TestScreen_QuestionsAnswered(t *testing.T) {
	s := apply(asking(),
		session.QuestionsAnswered{At: t0, ID: "q1"},
		engine.QuestionsAnswered{At: t0, CallID: "c1", Questions: migration, Answers: engine.Answers{
			"strategy": {Answers: []string{"Expand and contract (Recommended)"}},
			"rollout":  {Answers: []string{engine.OtherAnswer, engine.NotePrefix + "after the Friday backup"}},
		}},
		core.ToolFinished{At: t0.Add(9 * time.Second), CallID: "c1", Name: engine.QuestionToolName, OK: true, Detail: "completed", Duration: 9 * time.Second},
	)
	golden(t, "questions-answered", screen(s, ""))
}

func screenAt(s state.State, draft string, w int) string {
	out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: w, Height: 40, Composer: "λ " + draft, ComposerHeight: 1, Draft: draft})
	lines := strings.Split(ansi.Strip(out), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}

	return strings.Join(lines, "\n") + "\n"
}

// TestScreen_QuestionsLongText: the question's text and the chosen
// option's description show in full, wrapped, where a few rows cut them
// before; the other options' descriptions keep one row. A short screen
// cuts them so the whole panel shows.
func TestScreen_QuestionsLongText(t *testing.T) {
	long := strings.Repeat("Require verifiable excerpts and a checklist of correction criteria. ", 4) + "The end."
	ask := strings.Repeat("How should the stronger model decide a correction is clear enough? ", 6) + "Last words."
	s := apply(base(),
		core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"},
		session.QuestionsAsked{At: t0, ID: "q1", CallID: "c1", Questions: []engine.Question{{ID: "gate", Header: "Auto gate", Question: ask, Options: []engine.QuestionOption{
			{Label: "Evidence gate (Recommended)", Description: long},
			{Label: "Judge probability cutoff", Description: long},
		}}}},
	)
	for _, w := range []int{100, 40} {
		out := screenAt(s, "", w)
		flat := strings.Join(strings.Fields(strings.NewReplacer("│", " ", "›", " ").Replace(out)), " ")
		assert.Contains(t, flat, "Last words.", "the question, at %d columns", w)
		assert.Equal(t, 1, strings.Count(flat, "The end."), "the chosen option's description alone, at %d columns", w)
		for line := range strings.SplitSeq(out, "\n") {
			assert.LessOrEqual(t, ansi.StringWidth(line), w, "%q", line)
		}
	}

	// On a short screen the two give rows back before the title and the
	// question's start would scroll off the top.
	for _, w := range []int{100, 40} {
		out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: w, Height: 24, Composer: "λ ", ComposerHeight: 1})
		plain := ansi.Strip(out)
		assert.Contains(t, plain, "? Auto gate", "at %d columns", w)
		assert.Contains(t, plain, "How should the stronger", "at %d columns", w)
		assert.Contains(t, plain, "2. Judge", "at %d columns", w)
		assert.Contains(t, plain, "…", "something is cut at %d columns", w)
	}

	// Beside a taller preview, cutting the description gains nothing: the
	// question gives the rows.
	s = apply(base(),
		core.RunStarted{At: t0, RunID: "20260924-120000-3f2a1b2c"},
		session.QuestionsAsked{At: t0, ID: "q1", CallID: "c1", Questions: []engine.Question{{ID: "gate", Header: "Auto gate", Question: ask + ask, Options: []engine.QuestionOption{
			{Label: "Evidence gate", Description: long + long, Preview: strings.Repeat("line\n", 20)},
			{Label: "Judge", Description: long},
		}}}},
	)
	out, _ := render.Screen(s, render.NewCache(render.Amber), render.Frame{Width: 120, Height: 32, Composer: "λ ", ComposerHeight: 1})
	plain := ansi.Strip(out)
	assert.Contains(t, plain, "? Auto gate")
	assert.Contains(t, plain, "How should the stronger")
}
