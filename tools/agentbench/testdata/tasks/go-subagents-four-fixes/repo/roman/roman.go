// Package roman converts Roman numerals.
package roman

import "strings"

var values = map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}

// FromRoman parses a Roman numeral such as "MCMXCIV".
func FromRoman(s string) (int, error) {
	s = strings.ToUpper(s)
	n := 0
	for i := 0; i < len(s); i++ {
		n += values[s[i]]
	}

	return n, nil
}

// ToRoman writes n (1 to 3999) as a Roman numeral.
func ToRoman(n int) string {
	syms := []struct {
		v int
		s string
	}{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}
	var b strings.Builder
	for _, s := range syms {
		for n >= s.v {
			b.WriteString(s.s)
			n -= s.v
		}
	}

	return b.String()
}
