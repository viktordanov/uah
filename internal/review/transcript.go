package review

import (
	"fmt"
	"strings"
)

// Kind is what a transcript entry is.
type Kind string

// The entry kinds. Only EntryUser is trusted.
const (
	// EntryUser is a user message, or the user's answers to the agent's
	// questions: the user's own words.
	EntryUser Kind = "user"
	// EntryCall is a tool call's arguments.
	EntryCall Kind = "call"
	// EntryResult is a tool call's short result, such as "exit 1", without
	// its output.
	EntryResult Kind = "result"
)

// Entry is one entry of a session's transcript.
type Entry struct {
	Kind Kind
	// Tool is the tool's name, for a call or a result.
	Tool string
	Text string
}

// Transcript is what a session's reviews see of it, oldest first, numbered
// from 0 across the whole session: Entries[i] is entry Start+i. The session
// keeps only its latest entries, and its first user message (usually the
// task) apart when that is no longer among them.
type Transcript struct {
	Entries []Entry
	Start   int
	// First is the session's first user message, entry FirstAt, when it
	// fell out of Entries (FirstAt < Start); else nil.
	First   *Entry
	FirstAt int
}

// End is the number of the entry after the last.
func (t Transcript) End() int { return t.Start + len(t.Entries) }

// numbered is an entry with its number in the session.
type numbered struct {
	Entry

	at int
}

// since returns the entries from number from on, and how many before
// Start the session no longer keeps; full adds First.
func (t Transcript) since(from int, full bool) (entries []numbered, dropped int) {
	if full && t.First != nil && t.FirstAt < t.Start {
		entries = append(entries, numbered{at: t.FirstAt, Entry: *t.First})
		dropped--
	}
	if from < t.Start {
		dropped += t.Start - from
		from = t.Start
	}
	for i := from - t.Start; i < len(t.Entries); i++ {
		entries = append(entries, numbered{at: t.Start + i, Entry: t.Entries[i]})
	}

	return entries, max(dropped, 0)
}

// render writes the entries in Codex's numbered form within the budget:
// the newest user messages that fit, after the first when task is set (in
// a full transcript it is usually the task), and the newest tool entries
// that fit. Each is cut in the middle when too long, and the left-out
// entries, with the dropped ones, are counted at the top.
func render(entries []numbered, dropped int, task bool, l Limits) string {
	lines := make([]string, len(entries))
	var users, tools []int // newest first
	for i := len(entries) - 1; i >= 0; i-- {
		lines[i] = line(entries[i], l)
		if entries[i].Kind == EntryUser {
			users = append(users, i)
		} else {
			tools = append(tools, i)
		}
	}
	keep := make([]bool, len(entries))
	if task && len(users) > 0 {
		users = append([]int{users[len(users)-1]}, users[:len(users)-1]...)
	}
	used := 0
	for _, i := range users {
		if used+len(lines[i]) <= l.UserBytes {
			keep[i] = true
			used += len(lines[i])
		}
	}
	used = 0
	for n, i := range tools {
		if n == l.Calls || used+len(lines[i]) > l.CallsBytes {
			break
		}
		keep[i] = true
		used += len(lines[i])
	}
	var b strings.Builder
	omitted := dropped
	for i := range entries {
		if !keep[i] {
			omitted++
		}
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "<omitted transcript_entries=\"%d\" reason=\"budget\" />\n", omitted)
	}
	for i, line := range lines {
		if keep[i] {
			b.WriteString(line)
		}
	}

	return b.String()
}

// line is an entry as the reviewer reads it.
func line(e numbered, l Limits) string {
	switch e.Kind {
	case EntryUser:
		return fmt.Sprintf("[%d] user: %s\n", e.at+1, truncate(strings.TrimSpace(e.Text), l.UserMessageBytes))
	case EntryResult:
		return fmt.Sprintf("[%d] tool %s result: %s\n", e.at+1, e.Tool, truncate(strings.TrimSpace(e.Text), l.CallBytes))
	default:
		return fmt.Sprintf("[%d] tool %s call: %s\n", e.at+1, e.Tool, truncate(strings.TrimSpace(e.Text), l.CallBytes))
	}
}
