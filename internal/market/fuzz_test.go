package market

import "testing"

// FuzzParseFormatRoundTrip asserts two properties over arbitrary input: ParseScaled
// never panics, and any value it accepts formats to a canonical string that re-parses
// to the same value (format/parse is an idempotent round-trip on the accepted set).
func FuzzParseFormatRoundTrip(f *testing.F) {
	f.Add("123.45", 2)
	f.Add("0.0005", 4)
	f.Add("1.5", 4)
	f.Add(".5", 2)
	f.Add("9223372036854775807", 0)
	f.Add("5.", 2)
	f.Add("", 0)
	f.Fuzz(func(t *testing.T, s string, scale int) {
		if scale < 0 || scale > 18 {
			return // out-of-range scale is rejected by contract; nothing to round-trip
		}
		v, err := ParseScaled(s, scale)
		if err != nil {
			return // malformed input is allowed — it just must not panic
		}
		out := FormatScaled(v, scale)
		v2, err2 := ParseScaled(out, scale)
		if err2 != nil {
			t.Fatalf("formatted %q (from %q, scale %d) failed to re-parse: %v", out, s, scale, err2)
		}
		if v2 != v {
			t.Fatalf("round-trip mismatch: %q -> %d -> %q -> %d (scale %d)", s, v, out, v2, scale)
		}
	})
}

// FuzzFormatScaledNoPanic asserts FormatScaled never panics across the full int64
// range and a wide scale range — the property the negative-value/scale fix relies on.
func FuzzFormatScaledNoPanic(f *testing.F) {
	f.Add(int64(0), 2)
	f.Add(int64(-9223372036854775808), 4) // most-negative int64
	f.Add(int64(9223372036854775807), 8)
	f.Fuzz(func(t *testing.T, v int64, scale int) {
		if scale < 0 || scale > 64 {
			return
		}
		_ = FormatScaled(v, scale)
	})
}
