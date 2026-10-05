package inventory

import "errors"

// ErrOutOfStock means a reservation asks for more than is available.
var ErrOutOfStock = errors.New("out of stock")

// Stock is the units on hand and reserved per SKU.
type Stock struct {
	onHand   map[string]int
	reserved map[string]int
}

// New returns stock with the units on hand.
func New(onHand map[string]int) *Stock {
	return &Stock{onHand: onHand, reserved: map[string]int{}}
}

// Available is the units of sku that can still be reserved.
func (s *Stock) Available(sku string) int {
	return s.onHand[sku] - s.reserved[sku]
}
