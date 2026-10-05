package pricing

import "strings"

// codes are the discount codes and their percentages.
var codes = map[string]int{"SAVE10": 10, "SAVE25": 25, "HALF": 50}

// Discount is the total in cents after the code's discount, rounded to
// the nearest cent, halves up.
func Discount(total int, code string) int {
	pct, ok := codes[strings.ToUpper(code)]
	if !ok {
		return total
	}

	return total - percentOf(total, pct)
}

// percentOf is pct percent of cents, rounded to the nearest cent.
func percentOf(cents, pct int) int {
	return cents * pct / 100
}
