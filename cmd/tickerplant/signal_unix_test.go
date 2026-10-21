//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// With TICKERPLANT_AS_MAIN set, the test binary runs main, so the test below can start a
// real tickerplant process and signal it.
func TestMain(m *testing.M) {
	if os.Getenv("TICKERPLANT_AS_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A SIGTERM must end a paced run promptly with exit code 0 and the "interrupted" line.
func TestSIGTERMEndsRun(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-steps", "1000000", "-pace", "1h", "-every", "0")
	cmd.Env = append(os.Environ(), "TICKERPLANT_AS_MAIN=1")
	var stderr bytes.Buffer
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	ready := make(chan struct{})
	go func() { // copy stderr, and note when the process has started the run
		sc := bufio.NewScanner(pr)
		signalled := false
		for sc.Scan() {
			stderr.WriteString(sc.Text() + "\n")
			if !signalled && strings.Contains(sc.Text(), "msg=mode") {
				signalled = true
				close(ready)
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("the process never logged its mode line")
	}
	time.Sleep(200 * time.Millisecond) // let it reach the pace wait
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
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
