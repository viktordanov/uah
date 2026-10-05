package inventory

// Release gives back n reserved units of sku, at most what is reserved.
func (s *Stock) Release(sku string, n int) {
	s.reserved[sku] = max(0, s.reserved[sku]-n)
}
