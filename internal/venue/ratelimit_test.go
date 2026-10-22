package venue

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLogEveryAllowsOnePerInterval(t *testing.T) {
	now := time.Unix(100, 0)
	l := NewLogEvery(time.Second, func() time.Time { return now })
	allowed := 0
	for range 100 { // a burst inside one second
		if l.Allow() {
			allowed++
		}
	}
	if allowed != 1 {
		t.Fatalf("%d lines in one second, want 1", allowed)
	}
	now = now.Add(999 * time.Millisecond)
	if l.Allow() {
		t.Fatal("allowed 999 ms after the last line")
	}
	now = now.Add(time.Millisecond)
	if !l.Allow() {
		t.Fatal("not allowed exactly one interval after the last line")
	}
	if l.Allow() {
		t.Fatal("a second line right after the new interval started")
	}
}

func TestNewLogEveryNilClockUsesTimeNow(t *testing.T) {
	l := NewLogEvery(time.Hour, nil)
	if !l.Allow() || l.Allow() {
		t.Fatal("want the first event allowed and the second held back")
	}
}

func TestCutRunes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short", "abc", 5, "abc"},
		{"exact", "abc", 3, "abc"},
		{"cut", "abcdef", 3, "abc"},
		{"empty", "", 3, ""},
		{"zero", "abc", 0, ""},
		{"negative", "abc", -1, ""},
		{"multi-byte kept whole", "žluťoučký", 4, "žluť"},
		{"invalid bytes replaced", "a\xffb", 5, "a�b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CutRunes(tc.in, tc.n)
			if got != tc.want {
				t.Fatalf("CutRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
		})
	}
	long := strings.Repeat("é", 500)
	if got := CutRunes(long, 200); utf8.RuneCountInString(got) != 200 {
		t.Fatalf("got %d runes, want 200", utf8.RuneCountInString(got))
	}
}
