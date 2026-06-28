package market

import "testing"

func TestSideString(t *testing.T) {
	if got := Bid.String(); got != "bid" {
		t.Errorf("Bid.String() = %q, want bid", got)
	}
	if got := Ask.String(); got != "ask" {
		t.Errorf("Ask.String() = %q, want ask", got)
	}
	if got := Unknown.String(); got != "unknown" {
		t.Errorf("Unknown.String() = %q, want unknown", got)
	}
	if got := Side(99).String(); got != "unknown" {
		t.Errorf("Side(99).String() = %q, want unknown", got)
	}
}

func TestSideZeroValueIsUnknown(t *testing.T) {
	var s Side // zero value
	if s != Unknown {
		t.Errorf("zero-value Side = %v, want Unknown", s)
	}
}

func TestLevelIsDelete(t *testing.T) {
	if !(Level{Price: 100, Size: 0}).IsDelete() {
		t.Error("zero size must be a delete")
	}
	if (Level{Price: 100, Size: 5}).IsDelete() {
		t.Error("nonzero size must not be a delete")
	}
	if (Level{Price: 100, Size: -1}).IsDelete() { // only exactly zero deletes, not "<= 0"
		t.Error("a negative size must not be classified as a delete")
	}
}

func TestParsePriceAndSize(t *testing.T) {
	p, err := ParsePrice("123.45", 2)
	if err != nil || p != 12345 {
		t.Fatalf("ParsePrice = %d, %v; want 12345", p, err)
	}
	if got := p.Format(2); got != "123.45" {
		t.Errorf("Price.Format(2) = %q, want 123.45", got)
	}
	s, err := ParseSize("1.5", 4)
	if err != nil || s != 15000 {
		t.Fatalf("ParseSize = %d, %v; want 15000", s, err)
	}
	if got := s.Format(4); got != "1.5000" {
		t.Errorf("Size.Format(4) = %q, want 1.5000", got)
	}
}

func TestParsePriceSizeErrors(t *testing.T) {
	if _, err := ParsePrice("nope", 2); err == nil {
		t.Error("ParsePrice on malformed input must error")
	}
	if _, err := ParseSize("1.234", 2); err == nil {
		t.Error("ParseSize over-precision must error")
	}
}
