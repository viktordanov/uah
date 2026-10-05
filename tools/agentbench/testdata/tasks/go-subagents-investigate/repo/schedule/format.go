package schedule

import "time"

// Format prints a run time as the schedule's log does.
func Format(t time.Time) string {
	return t.Format("Mon 2006-01-02 15:04")
}
