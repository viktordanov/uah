package term

import (
	"encoding/base64"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// Status is one record of the program-status protocol (OSC 7501, revision
// 0.4): what the program is doing, for a terminal or a tool around it to
// show in a tab, a session list, or a notification. Each report replaces
// its record whole, so a Status is the record as it should be now.
type Status struct {
	// ID addresses a record beneath the program's own, such as
	// "agent/scout" ("": the program's own). StatusID builds one.
	ID string
	// State is StateIdle, StateWorking, StateBlocked, StateDone, or
	// StateError.
	State string
	// Kind is what a blocked record waits on: KindPermission,
	// KindQuestion, or KindAuth.
	Kind string
	// Title labels a record beneath the program's own; Msg says what it
	// does or needs, in one line.
	Title, Msg string
}

// The states a Status reports.
const (
	// StateIdle is at rest, waiting for the user's next instruction.
	StateIdle = "idle"
	// StateWorking is busy, with nothing the user must do.
	StateWorking = "working"
	// StateBlocked waits on the user (Kind says for what).
	StateBlocked = "blocked"
	// StateDone finished a piece of work the user has not seen yet.
	StateDone = "done"
	// StateError stopped on an error.
	StateError = "error"
)

// What a blocked Status waits on.
const (
	KindPermission = "permission" // an approval
	KindQuestion   = "question"   // an answer the user must type
	KindAuth       = "auth"       // a login, a token, or a credential
)

// The protocol's limits: a report that breaks one is dropped whole, so the
// encoder cuts Msg and Title to them.
const (
	maxStatusMsg     = 2048 // bytes of Msg, before base64
	maxStatusTitle   = 192  // bytes of Title, before base64
	maxStatusSegment = 32   // bytes of an ID's segment
	maxStatusID      = 128  // bytes of an ID
	maxStatusDepth   = 8    // segments of an ID
)

// StatusID joins segments into a record's ID, such as StatusID("agent",
// "Scout #2") = "agent/Scout-2": each segment keeps letters, digits, and
// "_.+-", the rest become "-", and a segment is cut to 32 bytes; an empty
// one is "_". IDs past 8 segments or 128 bytes are cut to fit.
func StatusID(segments ...string) string {
	segments = segments[:min(len(segments), maxStatusDepth)]
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		var b strings.Builder
		dash := false
		for _, r := range seg {
			if !idChar(r) {
				if !dash && b.Len() > 0 {
					b.WriteByte('-')
				}
				dash = true

				continue
			}
			b.WriteRune(r)
			dash = false
		}
		part := b.String()
		part = strings.TrimRight(part[:min(len(part), maxStatusSegment)], "-")
		if part == "" {
			part = "_"
		}
		parts = append(parts, part)
	}
	id := strings.Join(parts, "/")
	for len(id) > maxStatusID {
		id = id[:strings.LastIndexByte(id, '/')]
	}

	return id
}

func idChar(r rune) bool {
	return r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.+-", r))
}

// validStatusID reports whether id is one the protocol's grammar takes:
// segments of 1 to 32 of "A-Za-z0-9_.+-", at most 8 of them and 128
// bytes. A report with any other is dropped by the terminal.
func validStatusID(id string) bool {
	segments := strings.Split(id, "/")
	if len(id) > maxStatusID || len(segments) > maxStatusDepth {
		return false
	}
	for _, seg := range segments {
		if seg == "" || len(seg) > maxStatusSegment || strings.IndexFunc(seg, func(r rune) bool { return !idChar(r) }) >= 0 {
			return false
		}
	}

	return true
}

// oscStatus is s's report for app: "ESC ] 7501 ; state=…:app=… ESC \".
// Msg and Title are base64 of their text with control characters as
// spaces, cut on a character's boundary to the protocol's limits.
func oscStatus(app string, s Status) string {
	var b strings.Builder
	b.WriteString("\x1b]7501;state=" + s.State)
	if s.ID != "" {
		b.WriteString(":id=" + s.ID)
	}
	if s.State == StateBlocked && s.Kind != "" {
		b.WriteString(":kind=" + s.Kind)
	}
	b.WriteString(":app=" + app)
	if s.Title != "" {
		b.WriteString(":title=" + statusText(s.Title, maxStatusTitle))
	}
	if s.Msg != "" {
		b.WriteString(":msg=" + statusText(s.Msg, maxStatusMsg))
	}
	b.WriteString("\x1b\\")

	return b.String()
}

// oscStatusClear removes the record id and the records beneath it. The
// program's own ("") is never cleared: with no ID, a clear removes every
// record on the terminal, other programs' too.
func oscStatusClear(id string) string { return "\x1b]7501;state=clear:id=" + id + "\x1b\\" }

// statusText is text as one line of at most limit bytes, in base64.
func statusText(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}

		return r
	}, strings.ToValidUTF8(text, "�"))
	for len(text) > limit {
		_, n := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-n]
	}

	return base64.StdEncoding.EncodeToString([]byte(text))
}

// writeStatus adds to pre the reports of the records that changed since
// the last frame written, and clears the ones left out, and returns what
// the terminal is told once pre is written: until then p.status stays, so
// a frame that fails to write sends its reports and clears again. A
// record with an ID the terminal would refuse is skipped.
func (p *Program) writeStatus(pre *strings.Builder, records []Status) map[string]string {
	next := make(map[string]string, len(records))
	var kept []Status
	for _, s := range records {
		if _, dup := next[s.ID]; dup || (s.ID != "" && !validStatusID(s.ID)) {
			continue
		}
		next[s.ID] = oscStatus(p.opts.StatusApp, s)
		kept = append(kept, s)
	}
	// The clears go first: a clear removes the records beneath it too, so
	// a kept one beneath a cleared one is reported again after it.
	var cleared []string
	for _, id := range slices.Sorted(maps.Keys(p.status)) {
		if _, shown := next[id]; shown {
			continue
		}
		if id == "" { // the program's own stays, at rest
			pre.WriteString(oscStatus(p.opts.StatusApp, Status{State: StateIdle}))
		} else {
			pre.WriteString(oscStatusClear(id))
			cleared = append(cleared, id)
		}
	}
	for _, s := range kept {
		if p.status[s.ID] != next[s.ID] || beneath(s.ID, cleared) {
			pre.WriteString(next[s.ID])
		}
	}

	return next
}

// beneath reports whether id is a record beneath one of ids.
func beneath(id string, ids []string) bool {
	return slices.ContainsFunc(ids, func(parent string) bool { return strings.HasPrefix(id, parent+"/") })
}

// mayHaveSent is what the terminal may have been told after a frame that
// failed to write, part of which may have reached it: the records told
// before and the frame's, none of them known, so each is sent again, or
// cleared, with the next frame.
func mayHaveSent(told, frame map[string]string) map[string]string {
	out := make(map[string]string, len(told)+len(frame))
	for id := range told {
		out[id] = ""
	}
	for id := range frame {
		out[id] = ""
	}

	return out
}

// sendLeaveStatus writes leaveStatus on its own after the terminal is left:
// leaving writes nothing when the terminal was already released, as it is
// while ctrl+g's editor runs, and the reports outlive the alt screen.
func (p *Program) sendLeaveStatus(failure string) {
	if seq := p.leaveStatus(failure); seq != "" {
		_ = p.term.write(seq)
	}
}

// leaveStatus is what leaving the terminal reports: the records beneath
// the program's own cleared, and its own idle, or an error when failure
// says why the program stopped. A working or blocked record would go when
// the program exits, but a done one would stay, though the user has seen
// it, and an error must stay.
func (p *Program) leaveStatus(failure string) string {
	if p.opts.StatusApp == "" || (len(p.status) == 0 && failure == "") {
		return ""
	}
	var b strings.Builder
	for _, id := range slices.Sorted(maps.Keys(p.status)) {
		if id != "" {
			b.WriteString(oscStatusClear(id))
		}
	}
	own := Status{State: StateIdle}
	if failure != "" {
		own = Status{State: StateError, Msg: failure}
	}
	b.WriteString(oscStatus(p.opts.StatusApp, own))
	p.status = nil

	return b.String()
}
