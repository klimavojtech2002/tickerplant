package main

import (
	"os"
	"syscall"
	"testing"
)

// docker stop sends SIGTERM, and a process running as PID 1 ignores it unless it handles
// it, so the list must name it. The list is checked here on every OS; the signal is
// delivered to a real process in signal_unix_test.go.
func TestShutdownSignalsIncludeInterruptAndSIGTERM(t *testing.T) {
	has := func(want os.Signal) bool {
		for _, s := range shutdownSignals {
			if s == want {
				return true
			}
		}
		return false
	}
	if !has(os.Interrupt) || !has(syscall.SIGTERM) {
		t.Fatalf("shutdownSignals = %v, want os.Interrupt and SIGTERM", shutdownSignals)
	}
}

// With TICKERPLANT_AS_MAIN set, the test binary runs main, so the test below can start a
// real tickerplant process and signal it.
func TestMain(m *testing.M) {
	if os.Getenv("TICKERPLANT_AS_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
