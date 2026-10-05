package state

// Peek is a file shown in an overlay over the session, the default for a
// click on a file link ([tui] file_links = "peek"): centered, scrolled so
// the link's line is near the top third, with that line or range on the
// band. The session goes on behind it, and keys go to it until it closes.
// The shell reads the file (EffPeekFile) and answers with PeekLoaded.
type Peek struct {
	Link FileLink
	// Loading is set until the file is read.
	Loading bool
	// Lines are the file's lines as the shell read them, highlighted.
	Lines []string
	// Note says why the file, or the rest of it, is not shown: binary, too
	// large, or not a readable regular file.
	Note string
	// Top is the first line shown, from 0.
	Top int
}

type (
	// EffPeekFile reads the file for the overlay; the answer is PeekLoaded.
	EffPeekFile struct{ Link FileLink }
	// PeekLoaded is the file read for the overlay: its lines, and a note
	// when not all of it shows. Rows is how many lines the overlay shows,
	// so the link's line can start near its top third.
	PeekLoaded struct {
		Link  FileLink
		Lines []string
		Note  string
		Rows  int
	}
	// PeekScroll moves the overlay's text by Delta lines (positive is
	// down), in an overlay that shows Rows lines.
	PeekScroll struct{ Delta, Rows int }
	// PeekClose closes the overlay (esc, q, ctrl+c).
	PeekClose struct{}
	// PeekEdit (e) and PeekOpen (o) close the overlay and open its file in
	// the editor or with the system's default app.
	PeekEdit struct{}
	PeekOpen struct{}
)

func (EffPeekFile) effect() {}

// onPeek handles the overlay's intents; ok is false for any other event.
func (s *State) onPeek(ev any) (effects []Effect, ok bool) {
	switch e := ev.(type) {
	case PeekLoaded:
		if p := s.Peek; p != nil && p.Link == e.Link {
			p.Loading, p.Lines, p.Note = false, e.Lines, e.Note
			p.Top = 0
			if l := e.Link.Line; l > 0 {
				p.Top = clampTop(l-1-e.Rows/3, len(p.Lines), e.Rows)
			}
		}
	case PeekScroll:
		if p := s.Peek; p != nil {
			p.Top = clampTop(p.Top+e.Delta, len(p.Lines), e.Rows)
		}
	case PeekClose:
		s.Peek = nil
	case PeekEdit, PeekOpen:
		if s.Peek == nil {
			return nil, true
		}
		l := s.Peek.Link
		s.Peek = nil
		if _, edit := e.(PeekEdit); edit {
			return []Effect{EffEditFile{Link: l}}, true
		}

		return []Effect{EffOpenFile{Link: l}}, true
	default:
		return nil, false
	}

	return nil, true
}

// clampTop keeps the first line shown within the text: never past the
// line that leaves the last one at the bottom.
func clampTop(top, lines, rows int) int {
	return max(min(top, lines-max(rows, 1)), 0)
}
