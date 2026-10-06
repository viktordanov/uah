package state

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Searching the transcript: / while going back, or /search, opens a query
// in the status line. Your messages and the agent's answers that contain
// it, ignoring case, are found off the update loop (EffFindInTranscript)
// and the window shows the newest. While typing, ↑ and ↓ move between
// them; enter ends the query, and then ↑ or k go to an earlier match and
// ↓ or j to a later one. Enter then leaves the search and going back with
// the window scrolled back to the match, as if scrolled there (FindStay):
// no run is stopped and nothing is cut. Esc goes back to choosing a
// message to edit, on your message at or above the match, or leaves when
// going back is not possible. The window holds the match a third of the way down, whatever
// arrives below, and every line but the matches is drawn dim.

// TranscriptSearch is an open search of the transcript.
type TranscriptSearch struct {
	Query string
	// Browsing is after enter: the query is set and j and k move.
	Browsing bool
	// Keys are the items that contain the query, top to bottom; At is the
	// one shown, or -1 for none.
	Keys []string
	At   int
	// Searching is true while a search for the query is under way.
	Searching bool
	gen       int
}

// Key is the item the search shows, or "" for none.
func (t *TranscriptSearch) Key() string {
	if t == nil || t.At < 0 || t.At >= len(t.Keys) {
		return ""
	}

	return t.Keys[t.At]
}

type (
	// FindOpen opens the search with a query, which may be empty.
	FindOpen struct{ Query string }
	// FindType edits the query: "\b" deletes its last character, and any
	// other text is appended.
	FindType struct{ Text string }
	// FindMove goes to an earlier (-1) or a later (+1) match.
	FindMove struct{ Delta int }
	// FindEnter ends the query; while browsing without a match it leaves
	// as esc does.
	FindEnter struct{}
	// FindStay leaves the search and going back with the window where the
	// last frame drew it: At is the line on its bottom row, Scroll the
	// lines below it, as Anchored reports them.
	FindStay struct {
		At     TextPos
		Scroll int
		Width  int
	}
	// FindEsc leaves the search for choosing a message to edit.
	FindEsc struct{}
	// FindCancel leaves the search and going back altogether (ctrl+c).
	FindCancel struct{}
	// Found are the items that contain a search's query.
	Found struct {
		Gen  int
		Keys []string
	}

	// EffFindInTranscript looks for Query in Docs off the update loop and
	// returns Found with Gen; a later search makes it stale.
	EffFindInTranscript struct {
		Gen   int
		Query string
		Docs  []SearchDoc
	}
)

func (EffFindInTranscript) effect() {}

// SearchDoc is an item's text, as a search sees it.
type SearchDoc struct {
	Key, Text string
}

const findNoMatch = "no match"

// onFind handles the search's intents; ok is false for any other event.
func (s *State) onFind(ev any) ([]Effect, bool) {
	if open, ok := ev.(FindOpen); ok {
		s.Find, s.Backtrack, s.escArmed = &TranscriptSearch{Query: open.Query, At: -1}, nil, time.Time{}

		return s.find(), true
	}
	f := s.Find
	if f == nil {
		if _, ok := ev.(Found); ok {
			return nil, true // a search that was closed
		}

		return nil, false
	}
	switch e := ev.(type) {
	case FindType:
		if e.Text == "\b" {
			r := []rune(f.Query)
			f.Query = string(r[:max(len(r)-1, 0)])
		} else {
			f.Query += e.Text
		}

		return s.find(), true
	case FindMove:
		if len(f.Keys) > 0 {
			f.At = min(max(f.At+e.Delta, 0), len(f.Keys)-1)
		}
	case FindEnter:
		if f.Browsing {
			s.leaveFind()

			return nil, true
		}
		f.Browsing = true
	case FindEsc:
		s.leaveFind()

		return nil, true
	case FindStay:
		s.Find, s.Backtrack, s.Status = nil, nil, ""
		s.Anchor, s.Scroll, s.NewBelow = e.At, e.Scroll, false
		s.anchorLayout = layout{width: e.Width, details: s.Details, reasoning: s.ShowReasoning}
		s.follow()

		return nil, true
	case FindCancel:
		s.Find, s.Backtrack, s.Status = nil, nil, ""

		return nil, true
	case Found:
		if e.Gen != f.gen {
			return nil, true
		}
		f.Keys, f.Searching, f.At = e.Keys, false, len(e.Keys)-1
	case Esc:
		s.leaveFind()

		return nil, true
	default:
		return nil, false
	}
	s.Status = s.findHint()

	return nil, true
}

// find starts a search for the query, which makes any earlier one stale.
func (s *State) find() []Effect {
	f := s.Find
	f.gen++
	f.Keys, f.At, f.Searching = nil, -1, f.Query != ""
	s.Status = s.findHint()
	if f.Query == "" {
		return nil
	}
	var docs []SearchDoc
	for _, it := range s.Items {
		if (it.Kind == KindUser || it.Kind == KindAssistant) && it.Text != "" {
			docs = append(docs, SearchDoc{Key: it.Key, Text: it.Text})
		}
	}

	return []Effect{EffFindInTranscript{Gen: f.gen, Query: f.Query, Docs: docs}}
}

// leaveFind closes the search and goes back to choosing a message to
// edit, on your message at or above the match, when going back is
// possible; otherwise the window returns to the bottom.
func (s *State) leaveFind() {
	key := s.Find.Key()
	s.Find, s.Status = nil, ""
	if !s.canBacktrack() || len(s.rewindTargets()) == 0 {
		s.Backtrack = nil

		return
	}
	s.startBacktrack()
	if key == "" {
		return
	}
	at := s.Order(key)
	for _, i := range slices.Backward(s.rewindTargets()) {
		if i <= at {
			s.Backtrack.Key = s.Items[i].Key

			return
		}
	}
}

// findHint is the status line while searching.
func (s *State) findHint() string {
	f := s.Find
	var b strings.Builder
	b.WriteString("/" + f.Query)
	switch {
	case f.Query == "":
	case f.Searching:
		b.WriteString(" · searching…")
	case len(f.Keys) == 0:
		b.WriteString(" · " + findNoMatch)
	default:
		fmt.Fprintf(&b, " · %d of %d", f.At+1, len(f.Keys))
	}
	if f.Browsing {
		b.WriteString(" · ↑/k earlier · ↓/j later · enter stay here · esc back")
	} else {
		b.WriteString(" · ↑↓ move · enter done · esc back")
	}

	return b.String()
}

// FindIn returns the keys of the docs that contain query, ignoring case,
// in order. It stops early, returning nil, when stale reports true.
func FindIn(docs []SearchDoc, query string, stale func() bool) []string {
	query = strings.ToLower(query)
	var keys []string
	for i, d := range docs {
		if i%256 == 0 && stale != nil && stale() {
			return nil
		}
		if strings.Contains(strings.ToLower(d.Text), query) {
			keys = append(keys, d.Key)
		}
	}

	return keys
}

// cmdSearch opens the search, with the words after /search as the query.
func cmdSearch(s *State, args string) []Effect {
	effects, _ := s.onFind(FindOpen{Query: strings.TrimSpace(args)})

	return effects
}
