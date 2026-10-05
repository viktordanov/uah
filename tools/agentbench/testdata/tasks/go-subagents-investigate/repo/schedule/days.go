package schedule

import "time"

// onDay says whether d is one of days, with Sunday given as 0 or 7.
func onDay(d time.Weekday, days []time.Weekday) bool {
	for _, x := range days {
		if x%7 == d && d != time.Sunday {
			return true
		}
	}

	return false
}
