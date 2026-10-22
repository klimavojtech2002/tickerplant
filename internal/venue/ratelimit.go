package venue

import (
	"strings"
	"time"
	"unicode/utf8"
)

// LogEvery lets one event through per interval, so a feed that sends thousands of bad
// frames a second produces a line a second instead of a flood. It keeps no count of what
// it held back: the caller's own counter is the truth and the log is a sample.
//
// It is not safe for concurrent use; an adapter calls it only from the goroutine that
// reads its frames.
type LogEvery struct {
	interval time.Duration
	now      func() time.Time
	next     time.Time
}

// NewLogEvery returns a limiter that allows one event per interval. A nil now uses
// time.Now.
func NewLogEvery(interval time.Duration, now func() time.Time) *LogEvery {
	if now == nil {
		now = time.Now
	}
	return &LogEvery{interval: interval, now: now}
}

// Allow reports whether an event may be logged now, and if so starts the next interval.
func (l *LogEvery) Allow() bool {
	t := l.now()
	if t.Before(l.next) {
		return false
	}
	l.next = t.Add(l.interval)
	return true
}

// CutRunes returns s with invalid UTF-8 replaced and at most n runes, for text that came
// from the venue and goes into a log line.
func CutRunes(s string, n int) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	if n < 0 {
		n = 0
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
