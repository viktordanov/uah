package state

import (
	"fmt"
	"strings"
	"time"
)

// heatmapWeeks is how far back /status shows activity.
const heatmapWeeks = 12

// heatShades go from no runs to the busiest day.
var heatShades = []string{"·", "░", "▒", "▓", "█"}

// Heatmap draws runs per day as a GitHub-style grid: a row per weekday
// (Monday first), a column per week, ending with the week of now.
func Heatmap(counts map[string]int, now time.Time, weeks int) string {
	values := make(map[string]int64, len(counts))
	total := 0
	for day, n := range counts {
		values[day] = int64(n)
		total += n
	}
	rows, _ := HeatGrid(values, now, weeks, nil)
	var b strings.Builder
	fmt.Fprintf(&b, "activity · last %d weeks · %d runs\n", weeks, total)
	for i, label := range []string{"Mon", "", "Wed", "", "Fri", "", "Sun"} {
		fmt.Fprintf(&b, "%-4s%s", label, rows[i])
		if i < 6 {
			b.WriteByte('\n')
		}
	}

	return b.String()
}

// HeatGrid is the grid of a heatmap of values per day (keyed YYYY-MM-DD):
// seven rows, Monday first, with a cell per week from start, the Monday
// weeks-1 weeks before the week of now, to now. A cell is a shade of its
// value relative to the largest shown, and paint, when set, styles it by its
// level (0: none).
func HeatGrid(values map[string]int64, now time.Time, weeks int, paint func(level int, cell string) string) (rows [7]string, start time.Time) {
	offset := (int(now.Weekday()) + 6) % 7
	start = now.AddDate(0, 0, -offset-7*(weeks-1))
	var peak int64 // of the days shown: a busier day outside would pale them all
	for d := start; !d.After(now); d = d.AddDate(0, 0, 1) {
		peak = max(peak, values[d.Format(time.DateOnly)])
	}
	for day := range 7 {
		var b strings.Builder
		for week := range weeks {
			d := start.AddDate(0, 0, 7*week+day)
			if d.After(now) {
				break
			}
			level := shade(values[d.Format(time.DateOnly)], peak)
			cell := heatShades[level]
			if paint != nil {
				cell = paint(level, cell)
			}
			b.WriteString(cell)
		}
		rows[day] = b.String()
	}

	return rows, start
}

// shade picks the level of a shade for v relative to the busiest day.
func shade(v, peak int64) int {
	if v <= 0 || peak <= 0 {
		return 0
	}
	level := 1 + (v-1)*int64(len(heatShades)-2)/max(peak-1, 1)

	return min(int(level), len(heatShades)-1)
}
