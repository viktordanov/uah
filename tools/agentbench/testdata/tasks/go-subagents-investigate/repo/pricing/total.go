package pricing

// Line is one order line: a price in cents and a quantity.
type Line struct {
	Cents int
	Qty   int
}

// Subtotal is the sum of the lines in cents.
func Subtotal(lines []Line) int {
	sum := 0
	for _, l := range lines {
		sum += l.Cents * l.Qty
	}

	return sum
}
