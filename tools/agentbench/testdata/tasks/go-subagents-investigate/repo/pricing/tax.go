package pricing

// Tax is the sales tax in cents at rate basis points, rounded down.
func Tax(cents, basisPoints int) int {
	return cents * basisPoints / 10000
}
