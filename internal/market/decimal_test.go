package market

import (
	"errors"
	"testing"
)

func TestParseScaled(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		scale int
		want  int64
		err   bool
	}{
		{"exact", "123.45", 2, 12345, false},
		{"trailing zeros kept", "1.5000", 4, 15000, false},
		{"fewer frac digits padded", "1.5", 4, 15000, false},
		{"integer only, scale 0", "123", 0, 123, false},
		{"integer with scale", "123", 2, 12300, false},
		{"leading-zero fraction", "0.0005", 4, 5, false},
		{"missing leading integer", ".5", 4, 5000, false},
		{"zero", "0", 2, 0, false},
		{"max precision at scale", "9.99999999", 8, 999999999, false},
		{"int64 max accepted", "9223372036854775807", 0, 9223372036854775807, false},
		{"trailing dot rejected", "5.", 2, 0, true},
		{"over precision rejected", "1.50001", 4, 0, true},
		{"double dot rejected", "1.2.3", 4, 0, true},
		{"empty rejected", "", 2, 0, true},
		{"dot only rejected", ".", 2, 0, true},
		{"letters rejected", "abc", 2, 0, true},
		{"negative rejected", "-1", 2, 0, true},
		{"scale below range", "1.0", -1, 0, true},
		{"scale above range", "1", 19, 0, true},
		{"overflow rejected", "92233720368547758070", 0, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseScaled(tc.in, tc.scale)
			if tc.err {
				if err == nil {
					t.Fatalf("ParseScaled(%q, %d) = %d, want error", tc.in, tc.scale, got)
				}
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("ParseScaled(%q, %d) error = %v, want ErrMalformed", tc.in, tc.scale, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseScaled(%q, %d) unexpected error: %v", tc.in, tc.scale, err)
			}
			if got != tc.want {
				t.Fatalf("ParseScaled(%q, %d) = %d, want %d", tc.in, tc.scale, got, tc.want)
			}
		})
	}
}

func TestFormatScaled(t *testing.T) {
	tests := []struct {
		v     int64
		scale int
		want  string
	}{
		{12345, 2, "123.45"},
		{15000, 4, "1.5000"},
		{5, 4, "0.0005"},
		{0, 2, "0.00"},
		{123, 0, "123"},
		{999999999, 8, "9.99999999"},
		{-5, 2, "-0.05"}, // defensive: not a book value, but must not be garbage
		{5, -1, "5"},     // invalid scale must not panic
	}
	for _, tc := range tests {
		if got := FormatScaled(tc.v, tc.scale); got != tc.want {
			t.Errorf("FormatScaled(%d, %d) = %q, want %q", tc.v, tc.scale, got, tc.want)
		}
	}
}

// A canonical string (exactly scale fractional digits) survives parse then format.
func TestScaledRoundTrip(t *testing.T) {
	cases := []struct {
		s     string
		scale int
	}{
		{"123.45", 2},
		{"1.5000", 4},
		{"0.0005", 4},
		{"0.00", 2},
		{"123", 0},
	}
	for _, c := range cases {
		v, err := ParseScaled(c.s, c.scale)
		if err != nil {
			t.Fatalf("ParseScaled(%q, %d): %v", c.s, c.scale, err)
		}
		if got := FormatScaled(v, c.scale); got != c.s {
			t.Errorf("round-trip %q scale %d -> %q", c.s, c.scale, got)
		}
	}
}
