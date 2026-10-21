//go:build !windows

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// lockedBuffer is a bytes.Buffer that the child's stderr copier can write while the test
// polls it. exec waits for that copier before Wait returns, so the final read is complete.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A SIGTERM must end a paced run promptly with exit code 0 and the "interrupted" line.
func TestSIGTERMEndsRun(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-steps", "1000000", "-pace", "1h", "-every", "0")
	cmd.Env = append(os.Environ(), "TICKERPLANT_AS_MAIN=1")
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(stderr.String(), "msg=mode") { // the run has started
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("the process never logged its mode line")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let it reach the pace wait

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("exit status %d, want 0\n%s", ee.ExitCode(), stderr.String())
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("took %v to stop after SIGTERM, want under 2s", took)
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the process ignored SIGTERM")
	}
	if !strings.Contains(stderr.String(), "interrupted, shutting down") {
		t.Fatalf("no interrupted line:\n%s", stderr.String())
	}
}
