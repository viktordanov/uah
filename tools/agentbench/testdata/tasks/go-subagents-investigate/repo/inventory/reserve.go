package inventory

// Reserve holds n units of sku, or fails with ErrOutOfStock.
func (s *Stock) Reserve(sku string, n int) error {
	if !canReserve(s.Available(sku), n) {
		return ErrOutOfStock
	}
	s.reserved[sku] += n

	return nil
}

// canReserve says whether n units fit in what is available.
func canReserve(available, n int) bool {
	return n > 0 && n < available
}
