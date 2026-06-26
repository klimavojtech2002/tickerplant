package market

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseScaled parses a non-negative decimal string into an integer scaled by
// 10^scale, with no floating point on the path: "1.5" at scale 4 is 15000.
//
// Reconstruction correctness depends on exact representation, so the parser is
// strict. A value with more fractional digits than the scale, a malformed number,
// a negative, or one that overflows int64 is a loud error (ErrMalformed), never a
// silent round or wrap (ADR-0003, docs/correctness.md §2).
func ParseScaled(s string, scale int) (int64, error) {
	if scale < 0 || scale > 18 {
		return 0, fmt.Errorf("scale %d out of range [0,18]: %w", scale, ErrMalformed)
	}
	if s == "" {
		return 0, fmt.Errorf("empty decimal: %w", ErrMalformed)
	}
	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if hasDot && fracPart == "" { // a bare "." or a trailing dot like "5." is not canonical
		return 0, fmt.Errorf("invalid decimal %q: %w", s, ErrMalformed)
	}
	if len(fracPart) > scale {
		return 0, fmt.Errorf("decimal %q has more than %d fractional digits: %w", s, scale, ErrMalformed)
	}
	// The integer digits followed by the fractional digits, padded out to the
	// scale, are the value in scaled units.
	digits := intPart + fracPart + strings.Repeat("0", scale-len(fracPart))
	var n int64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid decimal %q: %w", s, ErrMalformed)
		}
		d := int64(c - '0')
		if n > (math.MaxInt64-d)/10 { // reject before the multiply overflows; never wrap silently
			return 0, fmt.Errorf("decimal %q overflows int64: %w", s, ErrMalformed)
		}
		n = n*10 + d
	}
	return n, nil
}

// FormatScaled renders a scaled integer back to its decimal string with exactly
// scale fractional digits, the inverse of ParseScaled. 15000 at scale 4 is
// "1.5000"; trailing zeros are kept so the form is canonical. A scale <= 0 returns
// the plain integer. The sign is handled separately so the whole int64 range
// (including the most-negative value) is formatted without a panic — book prices
// and sizes are non-negative, but the function never produces garbage if they are not.
func FormatScaled(v int64, scale int) string {
	s := strconv.FormatInt(v, 10)
	if scale <= 0 {
		return s
	}
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	if len(s) <= scale {
		s = strings.Repeat("0", scale+1-len(s)) + s
	}
	return sign + s[:len(s)-scale] + "." + s[len(s)-scale:]
}
