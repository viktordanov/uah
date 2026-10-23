package state

import (
	"testing"
	"time"

	"github.com/sahilm/fuzzy"
	"github.com/stretchr/testify/assert"
)

// TestMenu_FilesAreSearchedOncePerDraft: the file list is searched when the
// draft or the list changes, not again for the menu's keys or each frame.
func TestMenu_FilesAreSearchedOncePerDraft(t *testing.T) {
	searches := 0
	findFiles = func(q string, files []string) fuzzy.Matches { searches++; return fuzzy.Find(q, files) }
	t.Cleanup(func() { findFiles = fuzzy.Find })
	labels := func(items []Suggestion) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Label)
		}

		return out
	}

	s := New(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	s, _ = Reduce(s, DraftChanged{Draft: "see @sess"})
	assert.Zero(t, searches, "nothing to search before the list loads")
	s, _ = Reduce(s, FilesLoaded{Paths: []string{"README.md", "internal/session/session.go", "cmd/uah/main.go"}})
	assert.Equal(t, 1, searches, "the loaded list is searched for the draft")
	draft := "see @sess"
	for _, key := range []string{"g", "o"} {
		assert.True(t, s.MenuOpen(draft), "the shell asks before it gives the key to the menu or the composer")
		draft += key
		s, _ = Reduce(s, DraftChanged{Draft: draft})
		for range 3 {
			assert.Equal(t, []string{"internal/session/session.go"}, labels(s.Suggestions(draft)), "a frame")
		}
	}
	assert.Equal(t, 3, searches, "one search per draft")
	s, _ = Reduce(s, MenuMove{Draft: draft, Delta: 1})
	_, effects := Reduce(s, MenuAccept{Draft: draft})
	assert.Equal(t, []Effect{EffSetDraft{Text: "see internal/session/session.go "}}, effects)
	assert.Equal(t, 3, searches, "the menu's keys do not search")

	s, _ = Reduce(s, FilesLoaded{Paths: []string{"session.go"}})
	assert.Equal(t, []string{"session.go"}, labels(s.Suggestions(draft)), "a new list is searched again")
	assert.Equal(t, []string{"session.go"}, labels(s.Suggestions("pasted @ses")), "a draft DraftChanged did not report is searched as it shows")
	assert.Equal(t, 5, searches)
}
