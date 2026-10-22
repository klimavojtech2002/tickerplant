package main

import (
	"io"
	"testing"

	"github.com/klimavojtech2002/tickerplant/internal/book"
	"github.com/klimavojtech2002/tickerplant/internal/delivery"
	"github.com/klimavojtech2002/tickerplant/internal/httpapi"
)

// quietEngine satisfies httpapi.Engine with zero counters; only the construction of the
// handler matters here.
type quietEngine struct{}

func (quietEngine) View() *book.View        { return nil }
func (quietEngine) Gaps() int               { return 0 }
func (quietEngine) Resyncs() int            { return 0 }
func (quietEngine) Disconnects() int        { return 0 }
func (quietEngine) WouldCrosses() int       { return 0 }
func (quietEngine) ChecksumMismatches() int { return 0 }
func (quietEngine) SnapshotRejects() int    { return 0 }
func (quietEngine) SnapshotErrors() int     { return 0 }
func (quietEngine) Ready() bool             { return true }
func (quietEngine) Events() []book.Event    { return nil }

// The flag and the handler apply one rule: an origin the command line accepts is one the
// handler accepts, and the other way round.
func TestFlagAndHandlerAgreeOnCORSOrigin(t *testing.T) {
	hub := delivery.New(1)
	defer hub.Close()
	for _, origin := range []string{
		"", "*", "https://app.example", "http://localhost:3000", "http://[::1]:3000", "http://127.0.0.1:8080",
		"ftp://x", "null", "HTTPS://app.example", "https://App.example", "https://app.example/", "https://app.example?x=1",
		"https://user@app.example", "http://:3000", "http://host:", "http://host:0", "https://app.example:443",
		"http://bücher.example", "http://::1", "http://host;x",
	} {
		_, flagErr := parseFlags([]string{"-cors-origin", origin}, io.Discard)
		_, newErr := httpapi.New(httpapi.Config{Hub: hub, Engine: quietEngine{}, CORSOrigin: origin})
		if (flagErr == nil) != (newErr == nil) {
			t.Errorf("origin %q: the flag says %v, the handler says %v", origin, flagErr, newErr)
		}
	}
}
