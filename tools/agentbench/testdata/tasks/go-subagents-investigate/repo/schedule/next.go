package schedule

import "time"

// NextRun is the next time at hour:00 on one of the weekdays, after from.
func NextRun(from time.Time, hour int, days []time.Weekday) time.Time {
	for i := 0; i < 8; i++ {
		day := from.AddDate(0, 0, i)
		at := time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, from.Location())
		if at.After(from) && onDay(at.Weekday(), days) {
			return at
		}
	}

	return time.Time{}
}
