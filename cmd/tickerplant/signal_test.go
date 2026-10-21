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
