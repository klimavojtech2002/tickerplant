package source

import (
	"errors"
	"fmt"
	"testing"
)

var errCause = errors.New("cause")

func TestTransient(t *testing.T) {
	if Transient(nil) != nil {
		t.Fatal("Transient(nil) must be nil")
	}
	e := Transient(fmt.Errorf("wrapped: %w", errCause))
	if !IsTransient(e) {
		t.Fatal("a marked error must be transient")
	}
	if !errors.Is(e, errCause) {
		t.Fatal("the cause must stay reachable through the marker")
	}
	if e.Error() != "wrapped: cause" {
		t.Fatalf("Error() = %q, want the cause's text unchanged", e.Error())
	}
	// Wrapping a marked error again keeps it marked.
	if !IsTransient(fmt.Errorf("outer: %w", e)) {
		t.Fatal("a wrapped transient error must stay transient")
	}
}

func TestPlainErrorIsNotTransient(t *testing.T) {
	if IsTransient(errCause) || IsTransient(nil) || IsTransient(fmt.Errorf("x: %w", errCause)) {
		t.Fatal("an unmarked error must not be transient")
	}
}
